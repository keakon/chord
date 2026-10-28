package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestMainResponsesFirstRequestPinsSessionIdentity(t *testing.T) {
	t.Parallel()
	a := newReadyTestMainAgent(t)
	var b map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: server.URL + "/v1/responses", Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}}}}, []string{"test-key"})
	p, err := llm.NewResponsesProviderWithClient(cfg, server.Client(), "")
	if err != nil {
		t.Fatal(err)
	}
	c := llm.NewClient(cfg, p, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(c, "test-model", 128000, "sample/test-model")
	want := filepath.Base(a.sessionDir)
	if c.SessionKey() != want {
		t.Fatalf("swap session=%q", c.SessionKey())
	}
	// Simulate a freshly installed client whose identity was not initialized.
	c.SetSessionIdentity(llm.SessionIdentity{})
	if _, err := a.callLLMForRequest(context.Background(), llmRequestInput{Messages: []message.Message{{Role: message.RoleUser, Content: "hello"}}}, 0); err != nil {
		t.Fatal(err)
	}
	// The wire carries the Codex-shaped client identity derived from the local
	// session key, never the session directory name itself.
	pck, _ := b["prompt_cache_key"].(string)
	if pck == "" || pck == want {
		t.Fatalf("first body pck=%v, want a derived id instead of the local key %q", pck, want)
	}
	if !uuidv7Re.MatchString(pck) {
		t.Fatalf("first body pck=%v, want a UUIDv7 client identity", pck)
	}
	md, ok := b["client_metadata"].(map[string]any)
	if !ok || md["session_id"] != pck {
		t.Fatalf("first body metadata=%v, want session_id=%q", md, pck)
	}
}

var uuidv7Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestLLMSessionIdentityConcurrentDirectoryChange(t *testing.T) {
	a := &MainAgent{sessionDir: "/sessions/session-a"}
	c := &llm.Client{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		for i := range 10000 {
			a.stateMu.Lock()
			a.sessionDir = []string{"/sessions/session-a", "/sessions/session-b"}[i%2]
			c.SetSessionIdentity(llm.SessionIdentity{Key: filepath.Base(a.sessionDir)})
			a.stateMu.Unlock()
		}
	})
	wg.Go(func() {
		<-start
		for range 10000 {
			a.ensureLLMSessionIdentity(c)
			a.stateMu.RLock()
			if got, want := c.SessionKey(), filepath.Base(a.sessionDir); got != want {
				t.Errorf("session key=%q, want current directory %q", got, want)
			}
			a.stateMu.RUnlock()
		}
	})
	close(start)
	wg.Wait()
}
