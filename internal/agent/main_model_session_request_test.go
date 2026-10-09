package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestMainResponsesFirstRequestPinsSessionIdentity(t *testing.T) {
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
	c.SetSessionID("")
	if _, err := a.callLLMForRequest(context.Background(), []message.Message{{Role: message.RoleUser, Content: "hello"}}, 0); err != nil {
		t.Fatal(err)
	}
	if b["prompt_cache_key"] != want {
		t.Fatalf("first body pck=%v want %s", b["prompt_cache_key"], want)
	}
	md, ok := b["client_metadata"].(map[string]any)
	if !ok || md["session_id"] != want {
		t.Fatalf("first body metadata=%v", md)
	}
}

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
			c.SetSessionID(filepath.Base(a.sessionDir))
			a.stateMu.Unlock()
		}
	})
	wg.Go(func() {
		<-start
		for range 10000 {
			a.ensureLLMSessionID(c)
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
