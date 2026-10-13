package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type probeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f probeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProbeRefreshRestartsEntireHandshake(t *testing.T) {
	var refreshes, initializes, listings atomic.Int32
	control, _, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		_, _ = io.WriteString(w, `{"access_token":"access-after","token_type":"Bearer","expires_in":3600}`)
	}))
	target := refreshTestTarget()
	target.Config.Name = target.Ref.ServerName
	store := NewCredentialStore(t.TempDir())
	seedRefreshTest(t, store, target, time.Now())
	mutateTransportCredential(t, store, target, func(slot *CredentialSlot) { slot.Token.ExpiresAt = time.Now().Add(time.Hour).UTC() })
	business := &http.Client{Transport: probeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}
		body := `{}`
		switch req.Method {
		case "initialize":
			count := initializes.Add(1)
			if r.Header.Get(mcpSessionHeader) != "" {
				t.Error("new handshake reused old session")
			}
			token := "Bearer access-before"
			if count == 2 {
				token = "Bearer access-after"
			}
			if r.Header.Get("Authorization") != token {
				t.Error("incorrect handshake credential")
			}
			response.Header.Set(mcpSessionHeader, fmt.Sprintf("session-%d", count))
			body = `{"protocolVersion":"` + protocolVersion + `"}`
		case "notifications/initialized":
			response.StatusCode = http.StatusAccepted
			return response, nil
		case "tools/list":
			if listings.Add(1) == 1 {
				response.StatusCode = http.StatusUnauthorized
				return response, nil
			}
			if r.Header.Get(mcpSessionHeader) != "session-2" {
				t.Error("listing used retired session")
			}
			body = `{"tools":[]}`
		default:
			t.Errorf("unexpected probe method: %s", req.Method)
		}
		response.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, body)))
		return response, nil
	})}
	service := NewAuthService(AuthOptions{Store: store, HTTPClient: control, Discoverer: refreshTestDiscovery(), MCPHTTPClient: business})
	defer service.Close()
	result := service.Probe(t.Context(), target, ProbeOptions{UseSavedCredential: true, AllowRefresh: true})
	if !result.OK || result.Catalog != CatalogReady || initializes.Load() != 2 || listings.Load() != 2 || refreshes.Load() != 1 {
		t.Fatalf("result=%+v requests=%d/%d/%d", result, initializes.Load(), listings.Load(), refreshes.Load())
	}
	if len(result.Stages) != 7 {
		t.Fatalf("stages=%v", result.Stages)
	}
	for _, stage := range result.Stages {
		if stage.State == ProbePending {
			t.Fatalf("unfinished stage: %+v", stage)
		}
		if stage.State != ProbeSkipped && (stage.StartedAt.IsZero() || stage.FinishedAt.Before(stage.StartedAt)) {
			t.Fatalf("invalid stage timing: %+v", stage)
		}
	}
	if result.Stages[4].Stage != StageRefresh || result.Stages[4].State != ProbeOK {
		t.Fatalf("refresh stage=%+v", result.Stages[4])
	}
}

func TestEmptyCatalogReadyDoesNotRepeatListing(t *testing.T) {
	tr := readyFakeTransport()
	tr.onMethod("tools/list", toolsListResult{Tools: []MCPToolDef{}})
	manager, catalog, target := managerFixture(t, false, tr)
	if err := manager.ConnectOne(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	before := tr.requestCount()
	for range 2 {
		if tools, err := catalog.DiscoverAllTools(t.Context()); err != nil || len(tools) != 0 {
			t.Fatalf("tools=%v err=%v", tools, err)
		}
		tools, snapshot := manager.ToolSnapshot()
		if len(tools) != 0 || snapshot.Servers[0].Catalog != CatalogReady {
			t.Fatalf("empty directory is not ready: %+v", snapshot)
		}
	}
	if tr.requestCount() != before {
		t.Fatal("empty directory was refetched")
	}
}
