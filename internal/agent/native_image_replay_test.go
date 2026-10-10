package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestNativeImageStatelessReplayPreservesOriginal(t *testing.T) {
	dir, cwd, ref := imageAuthorizationFixture(t)
	path, err := tools.ResolveImageArtifactPath(dir, ref, cwd)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"type": message.HostedCallKindImageGeneration, "id": "image-1", "result": base64.StdEncoding.EncodeToString(data), "status": "completed", "background": "transparent", "output_format": "png", "quality": "high"})
	resp := &message.Response{Hosted: &message.HostedObservation{Items: []json.RawMessage{raw}, Calls: []message.HostedCall{{ID: "image-1", Kind: message.HostedCallKindImageGeneration, Status: message.HostedCallStatusCompleted, Result: raw}}}}
	policy := &llm.NativeToolPolicy{}
	(&MainAgent{}).configureNativeImagePolicy(policy, nil, nil, recovery.NativeRequestJournal{SessionDir: dir}, identity.MainAgentID)
	if err := policy.ProjectResponse(t.Context(), resp); err != nil {
		t.Fatal(err)
	}
	var request struct {
		Store bool              `json:"store"`
		Input []json.RawMessage `json:"input"`
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-2\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg-2\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Image received\"}]}]}}\n\n")
	}))
	defer srv.Close()
	provider := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 32000, Output: 1024}}}}, []string{"test-key"})
	impl, err := llm.NewResponsesProvider(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient(provider, impl, "test-model", 1024, "")
	defer client.Close()
	history := &message.NativeToolHistory{Target: "sample/test-model", Protocol: config.ProviderTypeResponses, APIURL: srv.URL, Items: resp.Hosted.Items, Calls: resp.Hosted.Calls}
	before, _ := json.Marshal(history)
	_, err = client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleAssistant, NativeTools: history}, {Role: message.RoleUser, Content: "Edit the generated image"}}, nil, nil, llm.CompleteStreamOptions{NativeTools: policy})
	if err != nil {
		t.Fatal(err)
	}
	if request.Store {
		t.Fatal("unexpected stored request")
	}
	found := false
	for _, raw := range request.Input {
		var item struct {
			Type, Result, Status, Background string
			OutputFormat                     string `json:"output_format"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		if item.Type == message.HostedCallKindImageGeneration {
			found = true
			if item.Result != base64.StdEncoding.EncodeToString(data) || item.Status != "completed" || item.Background != "transparent" || item.OutputFormat != "png" {
				t.Fatalf("original or metadata changed: %s", raw)
			}
		}
	}
	if !found {
		t.Fatal("native image missing from request")
	}
	after, _ := json.Marshal(history)
	if string(before) != string(after) {
		t.Fatal("request projection mutated canonical history")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err = client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleAssistant, NativeTools: history}, {Role: message.RoleUser, Content: "Edit the image"}}, nil, nil, llm.CompleteStreamOptions{NativeTools: policy})
	if !llm.IsNativeToolError(err) || requests.Load() != 1 {
		t.Fatalf("missing original did not stop replay: requests=%d err=%v", requests.Load(), err)
	}
}

func TestNativeImageFallbackUsesSubAgentPermission(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.ruleset = permissionRuleset(t, "generate_image: deny\n")
	sub.setRuleset(permissionRuleset(t, "generate_image: allow\n"))
	parent.subs.add(sub)
	backend := &imageGenerationBackend{agent: parent, timeout: time.Minute}
	sub.tools.Register(&tools.GenerateImageTool{Backend: backend})
	if !nativeToolPermitted(sub.tools, sub.currentRuleset(), nil, tools.NameGenerateImage) {
		t.Fatal("sub native tool not permitted")
	}
	if err := backend.Check(tools.WithAgentID(sub.turn.Ctx, sub.instanceID), imagegen.Request{}); err != nil {
		t.Fatal("sub local tool not permitted", err)
	}
	policy := &llm.NativeToolPolicy{Begin: func(context.Context, llm.NativeRequestRecord) (string, error) { return "request-1", nil }}
	parent.configureNativeImagePolicy(policy, sub.turn, sub.tools, recovery.NativeRequestJournal{SessionDir: parent.sessionDir, AgentID: sub.taskID, TurnID: sub.turn.ID}, sub.instanceID)
	_, err := policy.Begin(sub.turn.Ctx, llm.NativeRequestRecord{Authorization: message.NativeToolAuthorization{Tool: tools.NameGenerateImage}})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.ImageFallback() {
		t.Fatal("authorized sub fallback incorrectly denied by main agent rules")
	}
}
