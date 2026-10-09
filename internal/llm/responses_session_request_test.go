package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
		b := bodies[i+1]
		if b["prompt_cache_key"] != sid {
			t.Fatalf("body %d pck=%v", i+1, b["prompt_cache_key"])
		}
		md, ok := b["client_metadata"].(map[string]any)
		if !ok || md["session_id"] != sid {
			t.Fatalf("body %d metadata=%v", i+1, md)
		}
	}
}
