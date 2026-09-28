package lsp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"
)

func TestClientRegistersServerRequestHandlers(t *testing.T) {
	fake := &fakePowernapClient{}
	c := &Client{client: fake, openFiles: make(map[string]int32)}
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for _, method := range []string{"workspace/diagnostic/refresh", "client/registerCapability", "client/unregisterCapability"} {
		if _, ok := fake.registeredHandlers[method]; !ok {
			t.Fatalf("%s handler should be registered", method)
		}
	}
}

func TestHandleDiagnosticRefreshOnlyAcknowledges(t *testing.T) {
	result, err := handleDiagnosticRefresh(context.Background(), "workspace/diagnostic/refresh", json.RawMessage("null"))
	if err != nil || result != nil {
		t.Fatalf("handleDiagnosticRefresh() = (%v, %v), want (nil, nil)", result, err)
	}
}

func TestNotifyDidSaveFollowsDynamicRegistration(t *testing.T) {
	fake := &fakePowernapClient{}
	c := &Client{client: fake, openFiles: make(map[string]int32)}
	ctx := context.Background()
	const path = "/tmp/project/main.go"

	if err := c.NotifyDidSave(ctx, path, "package main"); err != nil {
		t.Fatalf("NotifyDidSave() error = %v", err)
	}
	if len(fake.didSaveURIs) != 0 {
		t.Fatalf("didSave sent without any save capability: %v", fake.didSaveURIs)
	}

	register := json.RawMessage(`{"registrations":[
		{"id":"fmt","method":"textDocument/formatting"},
		{"id":"save-1","method":"textDocument/didSave","registerOptions":{"documentSelector":null,"includeText":true}}
	]}`)
	if result, err := c.handleRegisterCapability(ctx, "client/registerCapability", register); err != nil || result != nil {
		t.Fatalf("handleRegisterCapability() = (%v, %v), want (nil, nil)", result, err)
	}
	if err := c.NotifyDidSave(ctx, path, "package main"); err != nil {
		t.Fatalf("NotifyDidSave() error = %v", err)
	}
	if len(fake.didSaveURIs) != 1 {
		t.Fatalf("didSave URIs = %v, want one after dynamic registration", fake.didSaveURIs)
	}
	if text := fake.didSaveTexts[0]; text == nil || *text != "package main" {
		t.Fatalf("didSave text = %v, want the document text the registration asked for", text)
	}

	unregister := json.RawMessage(`{"unregisterations":[{"id":"save-1","method":"textDocument/didSave"}]}`)
	if _, err := c.handleUnregisterCapability(ctx, "client/unregisterCapability", unregister); err != nil {
		t.Fatalf("handleUnregisterCapability() error = %v", err)
	}
	if err := c.NotifyDidSave(ctx, path, "package main"); err != nil {
		t.Fatalf("NotifyDidSave() error = %v", err)
	}
	if len(fake.didSaveURIs) != 1 {
		t.Fatalf("didSave sent after the registration was withdrawn: %v", fake.didSaveURIs)
	}
}

func TestSaveOptionsResolvesStaticCapabilityOnce(t *testing.T) {
	fake := &countingSaveOptionsClient{saveOptions: &protocol.SaveOptions{}}
	c := &Client{client: fake, openFiles: make(map[string]int32)}
	for range 3 {
		if err := c.NotifyDidSave(context.Background(), "/tmp/project/main.go", "package main"); err != nil {
			t.Fatalf("NotifyDidSave() error = %v", err)
		}
	}
	if fake.saveOptionCalls != 1 {
		t.Fatalf("SaveOptions calls = %d, want 1", fake.saveOptionCalls)
	}
	if len(fake.didSaveURIs) != 3 {
		t.Fatalf("didSave URIs = %v, want 3", fake.didSaveURIs)
	}
}

type countingSaveOptionsClient struct {
	fakePowernapClient
	saveOptionCalls int
}

func (f *countingSaveOptionsClient) SaveOptions() (protocol.SaveOptions, bool) {
	f.saveOptionCalls++
	return f.fakePowernapClient.SaveOptions()
}

func TestNotifyDidSaveFiltersDynamicRegistrations(t *testing.T) {
	for _, tc := range []struct {
		name, selector, path string
		want                 bool
	}{
		{"null", `null`, "/tmp/project/main.go", true},
		{"empty", `[]`, "/tmp/project/main.go", false},
		{"language", `[{"language":"go"}]`, "/tmp/project/main.go", true},
		{"other-language", `[{"language":"python"}]`, "/tmp/project/main.go", false},
		{"scheme", `[{"scheme":"untitled"}]`, "/tmp/project/main.go", false},
		{"pattern", `[{"language":"go","scheme":"file","pattern":"**/*.{go,mod}"}]`, "/tmp/project/main.go", true},
		{"other-pattern", `[{"pattern":"**/generated/*.go"}]`, "/tmp/project/main.go", false},
		{"or", `[{"language":"python"},{"pattern":"**/*.go"}]`, "/tmp/project/main.go", true},
		{"wildcard", `[{"language":"*","scheme":"*"}]`, "/tmp/project/main.go", true},
		{"notebook", `[{"notebook":"sample","language":"go"}]`, "/tmp/project/main.go", false},
		{"invalid-pattern", `[{"pattern":"["}]`, "/tmp/project/main.go", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakePowernapClient{}
			c := &Client{client: fake}
			ctx := context.Background()
			params := json.RawMessage(`{"registrations":[{"id":"save","method":"textDocument/didSave","registerOptions":{"documentSelector":` + tc.selector + `,"includeText":true}}]}`)
			if _, err := c.handleRegisterCapability(ctx, "client/registerCapability", params); err != nil {
				t.Fatal(err)
			}
			if err := c.NotifyDidSave(ctx, tc.path, "sample"); err != nil {
				t.Fatal(err)
			}
			if got := len(fake.didSaveURIs) != 0; got != tc.want {
				t.Fatalf("notified = %v, want %v", got, tc.want)
			}
			if tc.want && (fake.didSaveTexts[0] == nil || *fake.didSaveTexts[0] != "sample") {
				t.Fatal("missing requested text")
			}
		})
	}
}

func TestNotifyDidSaveDoesNotMergeUnmatchedIncludeText(t *testing.T) {
	fake := &fakePowernapClient{saveOptions: &protocol.SaveOptions{}}
	c := &Client{client: fake}
	ctx := context.Background()
	params := json.RawMessage(`{"registrations":[{"id":"python-save","method":"textDocument/didSave","registerOptions":{"documentSelector":[{"language":"python"}],"includeText":true}}]}`)
	if _, err := c.handleRegisterCapability(ctx, "client/registerCapability", params); err != nil {
		t.Fatal(err)
	}
	if err := c.NotifyDidSave(ctx, "/tmp/project/main.go", "package main"); err != nil {
		t.Fatal(err)
	}
	if len(fake.didSaveTexts) != 1 || fake.didSaveTexts[0] != nil {
		t.Fatalf("texts = %v, want one notification without text", fake.didSaveTexts)
	}
}
