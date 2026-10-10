package agent

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/llm"
)

func TestImageCredentialRejectionRotatesAndKeepsChatKeys(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{{"google invalid key", 400, `{"error":{"status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID"}]}}`}, {"authentication", 401, `{"error":{"code":"invalid_api_key"}}`}, {"quota", 429, `{"error":{"code":"insufficient_quota"}}`}} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			counts := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := r.Header.Get("Authorization")
				mu.Lock()
				counts[key]++
				mu.Unlock()
				if key == "Bearer key-1" {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				_, _ = w.Write([]byte(`{"data":[{"url":"https://example.invalid/image.png"}]}`))
			}))
			defer server.Close()
			target, _ := imagegen.ResolveTarget(imagegen.PresetOpenAI, "gpt-image-1.5", server.URL)
			target.Provider = "sample"
			provider := config.ProviderConfig{KeyRotation: config.KeyRotationOnFailure}
			a := &MainAgent{globalConfig: config.DefaultConfig(), governor: newResourceGovernor(config.OrchestrationConfig{})}
			backend, err := NewImageGenerationBackend(a, target, time.Minute, provider, []string{"key-1", "key-2"}, "")
			if err != nil {
				t.Fatal(err)
			}
			chat := llm.NewProviderConfig("sample", provider, []string{"key-1", "key-2"})
			for range 2 {
				if _, err = backend.Run(t.Context(), imagegen.Request{Prompt: "A tree", Operation: imagegen.Generate}, func() error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if counts["Bearer key-1"] != 1 || counts["Bearer key-2"] != 2 {
				t.Fatalf("counts=%v", counts)
			}
			key, _, err := chat.SelectKeyWithContext(t.Context())
			if err != nil || key != "key-1" {
				t.Fatalf("chat key=%q err=%v", key, err)
			}
		})
	}
}

func TestImageRateLimitUsesServerCooldown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
	}))
	defer server.Close()
	target, _ := imagegen.ResolveTarget(imagegen.PresetOpenAI, "gpt-image-1.5", server.URL)
	target.Provider = "sample"
	a := &MainAgent{globalConfig: config.DefaultConfig(), governor: newResourceGovernor(config.OrchestrationConfig{})}
	backend, err := NewImageGenerationBackend(a, target, time.Minute, config.ProviderConfig{RetryBackoff: config.RetryBackoffFixed, RetryDelayMS: new(30000)}, []string{"key"}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Run(t.Context(), imagegen.Request{Prompt: "A tree"}, func() error { return nil })
	f, ok := errors.AsType[*imagegen.Failure](err)
	if !ok || f.Details.Category != imagegen.FailureRateLimit || f.Details.RetryAfter() != 7*time.Second {
		t.Fatal(err)
	}
	_, _, err = backend.(*imageGenerationBackend).provider.SelectKeyWithContext(t.Context())
	cooling, ok := errors.AsType[*llm.AllKeysCoolingError](err)
	if !ok || cooling.RetryAfter < 6*time.Second || cooling.RetryAfter > 7*time.Second {
		t.Fatalf("cooling=%+v err=%v", cooling, err)
	}
}

func TestImageDuplicateKeysPreserveHardRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"insufficient_quota"}}`))
	}))
	defer server.Close()
	target, _ := imagegen.ResolveTarget(imagegen.PresetOpenAI, "gpt-image-1.5", server.URL)
	target.Provider = "sample"
	a := &MainAgent{globalConfig: config.DefaultConfig(), governor: newResourceGovernor(config.OrchestrationConfig{})}
	backend, err := NewImageGenerationBackend(a, target, time.Minute, config.ProviderConfig{}, []string{"key", "key"}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Run(t.Context(), imagegen.Request{Prompt: "A tree"}, func() error { return nil })
	f, ok := errors.AsType[*imagegen.Failure](err)
	if !ok || f.State != imagegen.StateRejected || f.Details.Category != imagegen.FailureQuota {
		t.Fatalf("failure=%+v err=%v", f, err)
	}
}
