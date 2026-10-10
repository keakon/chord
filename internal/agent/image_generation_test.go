package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestImageGenerationPathPermissions(t *testing.T) {
	node := parsePermissionNode(t, `"*": allow
read:
  "secret/*": deny
write:
  "locked/*": deny
generate_image: allow
`)
	rules := permission.ParsePermission(&node)
	for _, tc := range []struct {
		raw  string
		want permission.Action
	}{
		{`{"prompt":"tree","operation":"generate"}`, permission.ActionAllow},
		{`{"prompt":"tree","operation":"edit","reference_images":["public.png","secret/input.png"]}`, permission.ActionDeny},
		{`{"prompt":"tree","operation":"generate","output_path":"locked/output.png"}`, permission.ActionDeny},
		{`{"prompt":"tree","operation":"generate","output_path":"public/output.png"}`, permission.ActionAllow},
		{`{bad`, permission.ActionDeny},
	} {
		got := evaluateToolPermissionInDir(rules, tools.NameGenerateImage, json.RawMessage(tc.raw), permission.PathScope{})
		if got.Action != tc.want {
			t.Fatalf("%s => %s want %s", tc.raw, got.Action, tc.want)
		}
	}
	node = parsePermissionNode(t, `"*": allow
read: ask
write: ask
generate_image: allow
`)
	got := evaluateImageGenerationPermission(permission.ParsePermission(&node), json.RawMessage(`{"prompt":"tree","operation":"edit","reference_images":["input.png"],"output_path":"output.png"}`), permission.PathScope{}, "")
	if got.Action != permission.ActionAsk || len(got.NeedsApprovalPaths) != 2 {
		t.Fatalf("decision=%+v", got)
	}
	node = parsePermissionNode(t, `"*": allow
generate_image: deny
`)
	if got := evaluateImageGenerationPermission(permission.ParsePermission(&node), json.RawMessage(`{"prompt":"tree"}`), permission.PathScope{}, ""); got.Action != permission.ActionDeny {
		t.Fatal("disabled image tool allowed")
	}
}

func TestGeneratedOriginalSurvivesTextOnlyCaller(t *testing.T) {
	parts, dropped := toolResultPartsForCapability("saved image", []message.ContentPart{{Type: message.ContentPartImage, ArtifactID: "sha256-sample", ImagePath: "sample.png"}}, stubInputCapability{"image": false})
	if len(parts) != 2 || dropped.Images != 0 {
		t.Fatal("generated original was discarded for text-only caller")
	}
}

func TestImageBackendConfigurationAndKeySafety(t *testing.T) {
	cfg := config.ImageGenerationConfig{Enabled: true}
	target, err := imagegen.ResolveTarget(imagegen.PresetCompatible, "test-image", "https://example.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	target.Provider = "sample"
	provider := config.ProviderConfig{Type: config.ProviderTypeResponses}
	for _, p := range []config.ProviderConfig{{Preset: config.ProviderPresetCodex}, {TokenURL: "https://example.invalid/token"}} {
		if _, err := NewImageGenerationBackend(nil, target, cfg.Timeout(), p, []string{"key"}, ""); err == nil {
			t.Fatal("OAuth image target accepted")
		}
	}
	if _, err := NewImageGenerationBackend(nil, target, cfg.Timeout(), provider, nil, ""); err == nil {
		t.Fatal("missing credentials accepted")
	}
	a := &MainAgent{}
	// Resolve the caller through the main agent's normal effective permission path.
	a.globalConfig = config.DefaultConfig()
	a.governor = newResourceGovernor(config.OrchestrationConfig{})
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 1 {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key"}}`))
		} else {
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"error":{"type":"server_error"}}`))
		}
	}))
	defer server.Close()
	target.BaseURL = server.URL
	b, err := NewImageGenerationBackend(a, target, cfg.Timeout(), provider, []string{"key-1", "key-2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Target().Model != "test-image" || b.Timeout() != cfg.Timeout() {
		t.Fatal("wrong image target")
	}
	_, err = b.Run(t.Context(), imagegen.Request{Prompt: "tree", Operation: imagegen.Generate}, func() error { return nil })
	failure, ok := errors.AsType[*imagegen.Failure](err)
	if !ok || failure.State != imagegen.StateUnknown || failure.Provider != target.Provider || failure.Model != target.Model || count != 2 {
		t.Fatalf("failure=%+v count=%d", failure, count)
	}
	if a.governor.snapshot().LLMActive != 0 {
		t.Fatal("image request leaked admission slot")
	}
	ctx := tools.WithAgentID(t.Context(), "missing-agent")
	if err := b.Check(ctx, imagegen.Request{Prompt: "tree"}); err == nil {
		t.Fatal("retired sub-agent bypassed caller check")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.Check(ctx, imagegen.Request{}); err == nil {
		t.Fatal("cancelled image request accepted")
	}
	// A durable barrier failure is before dispatch and releases the resource slot.
	target.BaseURL = server.URL
	b2, err := NewImageGenerationBackend(a, target, cfg.Timeout(), provider, []string{"key-3"}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = b2.Run(t.Context(), imagegen.Request{Prompt: "tree"}, func() error { return fmt.Errorf("manifest write failed") })
	if failure, ok := errors.AsType[*imagegen.Failure](err); !ok || failure.State != imagegen.StateNotSent || count != 2 {
		t.Fatal(err)
	}
}

func TestImageResultPublicationFailsClosedOnCanonicalPersistence(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.installRecoveryManager(newBrokenPathRecoveryManager(t))
	event := ToolResultEvent{CallID: "image-call", Name: tools.NameGenerateImage, Status: ToolResultStatusSuccess, Result: "saved", Payload: "saved"}
	a.persistImageResult(a.recoveryManager(), "main", message.Message{Role: message.RoleTool, ToolCallID: "image-call", Content: "saved"}, event, a.notePersistenceFailure)
	a.flushPersist()
	found := false
	for _, evt := range drainAgentEvents(a.Events()) {
		if result, ok := evt.(ToolResultEvent); ok && result.CallID == event.CallID {
			found = true
			if result.Status == ToolResultStatusSuccess || result.RecoveryState != message.ToolRecoveryStateOutcomeUnknown {
				t.Fatal("failed canonical persistence published success")
			}
		}
	}
	if !found || !a.persistenceDegraded() {
		t.Fatal("missing persistence failure terminal")
	}
}

func TestImageRecoveryFailureProjection(t *testing.T) {
	for _, tc := range []struct{ state, want string }{
		{imagegen.StateUnknown, message.ToolRecoveryStateOutcomeUnknown},
		{imagegen.StateNotSent, message.ToolRecoveryStateNotStarted},
		{imagegen.StateRejected, message.ToolRecoveryStateNotStarted},
		{imagegen.StateCompleted, ""},
	} {
		err := fmt.Errorf("image operation: %w", &imagegen.Failure{State: tc.state, Cause: context.Canceled})
		if got := imageToolRecoveryState(err); got != tc.want {
			t.Fatalf("state=%s got=%s want=%s", tc.state, got, tc.want)
		}
	}
}

func TestGeneratedSubAgentResultPreservesDeliveryPayload(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	rm := recovery.NewRecoveryManager(parent.sessionDir)
	parent.installRecoveryManager(rm)
	t.Cleanup(rm.Close)
	sub.turn.PendingToolCalls.Store(2)
	summary := `{"operation_id":"sample-operation","state":"saved","images":[],"manifest":"artifact:images/operations/sample.json","billing_state":"unknown"}`
	sub.handleToolResult(&toolResult{
		CallID: "image-call", Name: tools.NameGenerateImage, TurnID: sub.turn.ID,
		Result: "Image summary is stored in a text artifact", Payload: summary,
		Notes: []string{"A separate result note"},
	})
	parent.flushPersist()
	found := false
	for _, evt := range drainAgentEvents(parent.Events()) {
		if event, ok := evt.(ToolResultEvent); ok && event.CallID == "image-call" {
			found = true
			if event.Payload != summary || event.Status != ToolResultStatusSuccess || len(event.Notes) != 1 {
				t.Fatalf("delivery payload lost: %+v", event)
			}
		}
	}
	if !found {
		t.Fatal("missing image terminal")
	}
	msgs, err := rm.LoadMessages(sub.instanceID)
	if err != nil || len(msgs) != 1 || msgs[0].ToolPayload != summary {
		t.Fatal("durable image summary lost", err)
	}
}
