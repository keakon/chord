package mcp

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestRefreshOperationPublishesStartAndTerminalState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		auth   AuthState
	}{
		{"success", http.StatusOK, `{"access_token":"access-after","token_type":"Bearer","expires_in":3600}`, AuthReady},
		{"rejected", http.StatusBadRequest, `{"error":"invalid_grant"}`, AuthReauthRequired},
	} {
		t.Run(tc.name, func(t *testing.T) { testRefreshOperationTerminal(t, tc.status, tc.body, tc.auth) })
	}
}
func testRefreshOperationTerminal(t *testing.T, status int, body string, wantAuth AuthState) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	control, _, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	target := refreshTestTarget()
	target.Config.Name = target.Ref.ServerName
	store := NewCredentialStore(t.TempDir())
	identity := seedRefreshTest(t, store, target, time.Now())
	mutateTransportCredential(t, store, target, func(slot *CredentialSlot) { slot.Token.ExpiresAt = time.Now().Add(time.Hour).UTC() })
	service := NewAuthService(AuthOptions{Store: store, HTTPClient: control, Discoverer: refreshTestDiscovery()})
	defer func() { unblock(); service.Close() }()
	manager := NewPendingManagerWithClientInfo([]Target{target}, testClientInfo, service)
	defer manager.Close()
	manager.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		return NewClientWithInfo(target.Config.Name, readyFakeTransport(), testClientInfo), nil
	}
	if err := manager.ConnectOne(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	events := make(chan RuntimeSnapshot, 16)
	manager.SetOnChange(func(view RuntimeSnapshot) { events <- view })
	mutateTransportCredential(t, store, target, func(slot *CredentialSlot) { slot.Token.ExpiresAt = time.Now().Add(-time.Second).UTC() })
	done := make(chan error, 1)
	go func() { _, err := service.Acquire(t.Context(), target, identity); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	var active RuntimeSnapshot
	select {
	case active = <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("missing refresh start")
	}
	if len(active.Servers[0].Operations) != 1 || active.Servers[0].Operations[0].Kind != OperationRefresh {
		t.Fatalf("start=%+v", active)
	}
	unblock()
	if err := <-done; (err == nil) != (wantAuth == AuthReady) {
		t.Fatal(err)
	}
	select {
	case terminal := <-events:
		if len(terminal.Servers[0].Operations) != 0 || terminal.Servers[0].Auth != wantAuth || terminal.Version <= active.Version {
			t.Fatalf("terminal=%+v", terminal)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing refresh terminal")
	}
	manager.Close()
	service.mu.Lock()
	observers := len(service.refreshObservers)
	service.mu.Unlock()
	if observers != 0 {
		t.Fatal("closed manager retained refresh subscription")
	}
}
