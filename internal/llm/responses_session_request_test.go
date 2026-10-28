package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesSessionChangeRebuildsCachedWireBody(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: server.URL + "/v1/responses"}, []string{"test-key"})
	p := &ResponsesProvider{provider: cfg, client: server.Client()}
	msgs := []message.Message{{Role: message.RoleUser, Content: "hello"}}
	for _, sid := range []string{"", "session-a", "session-b"} {
		_, err := p.CompleteStream(context.Background(), "test-key", "test-model", "", msgs, nil, 128, RequestTuning{SessionKey: sid}, func(message.StreamDelta) {})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, sid := range []string{"session-a", "session-b"} {
		want := deriveResponsesWireIdentity(sid, 0, false).sessionID
		b := bodies[i+1]
		if b["prompt_cache_key"] != want {
			t.Fatalf("body %d pck=%v, want the derived id %q", i+1, b["prompt_cache_key"], want)
		}
		md, ok := b["client_metadata"].(map[string]any)
		if !ok || md["session_id"] != want {
			t.Fatalf("body %d metadata=%v, want session_id %q", i+1, md, want)
		}
	}
}

// A durable compaction advances the window number, which is carried inside
// client_metadata. The single-entry body reuse cache must not hand the request
// after compaction the body stamped with the previous window.
func TestResponsesWindowChangeRebuildsCachedWireBody(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: server.URL + "/v1/responses"}, []string{"test-key"})
	p := &ResponsesProvider{provider: cfg, client: server.Client()}
	msgs := []message.Message{{Role: message.RoleUser, Content: "hello"}}
	for _, window := range []uint64{0, 1} {
		_, err := p.CompleteStream(context.Background(), "test-key", "test-model", "", msgs, nil, 128, RequestTuning{SessionKey: "session-a", WindowNumber: window}, func(message.StreamDelta) {})
		if err != nil {
			t.Fatal(err)
		}
	}
	thread := deriveResponsesWireIdentity("session-a", 0, false)
	for i, window := range []uint64{0, 1} {
		md, ok := bodies[i]["client_metadata"].(map[string]any)
		if !ok {
			t.Fatalf("body %d has no client_metadata: %v", i, bodies[i])
		}
		if got := bodies[i]["prompt_cache_key"]; got != thread.sessionID {
			t.Fatalf("body %d pck=%v, want the thread id to stay %q across compaction", i, got, thread.sessionID)
		}
		if got := md[responsesClientMetadataWindowID]; got != thread.sessionID+":"+strconv.FormatUint(window, 10) {
			t.Fatalf("body %d window_id = %v, want window %d", i, got, window)
		}
	}
}
