package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestResponsesClientContract400SkipsReplayAndKeyRotation(t *testing.T) {
	for _, signal := range []struct {
		name string
		err  *APIError
	}{
		{"code", &APIError{StatusCode: 400, Code: invalidResponsesRequestCode, Message: "request rejected"}},
		{"message", &APIError{StatusCode: 400, Type: "new_api_error", Message: invalidCodexRequestMessage}},
	} {
		for _, replay := range []bool{false, true} {
			for _, fallback := range []bool{false, true} {
				name := signal.name + "/plain"
				if replay {
					name = signal.name + "/replay"
				}
				if fallback {
					name += "/fallback"
				}
				t.Run(name, func(t *testing.T) {
					cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, Models: map[string]config.ModelConfig{"test-model": replayAllModel()}}, []string{"key-a", "key-b"})
					disableRetryDelayForTest(cfg)
					impl := &replayRejectingProvider{scriptedErrs: []error{signal.err, signal.err, signal.err, signal.err}}
					c := NewClient(cfg, impl, "test-model", 4096, "sys")
					fallbackImpl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok"}}}}
					if fallback {
						fallbackCfg := testProviderConfig("alternate", "alternate-model")
						c.SetFallbackModels([]FallbackModel{{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "alternate-model", MaxTokens: 4096, ContextLimit: 128000}})
					}
					msgs := []message.Message{{Role: message.RoleUser, Content: "hello"}}
					if replay {
						msgs = crossProviderReplayMessages()
						msgs[1].Provenance.ProviderID = cfg.Name()
						msgs[1].Provenance.ModelID = "test-model"
					}
					resp, err := c.CompleteStream(context.Background(), msgs, nil, nil)
					if fallback {
						if err != nil || resp == nil || resp.Content != "ok" {
							t.Fatalf("fallback response=%+v error=%v", resp, err)
						}
						if got := fallbackImpl.CallCount(); got != 1 {
							t.Fatalf("fallback calls=%d, want 1", got)
						}
					} else if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != 400 {
						t.Fatalf("error=%v, want APIError 400", err)
					}
					if got := len(impl.attempts); got != 1 {
						t.Fatalf("primary calls=%d, want 1", got)
					}
					cfg.mu.Lock()
					for _, key := range cfg.keyStates {
						if key.CooldownCount != 0 || !key.CooldownEnd.IsZero() || key.Recovering {
							t.Errorf("key unexpectedly cooled or recovering: cooldown=%d recovering=%t", key.CooldownCount, key.Recovering)
						}
					}
					cfg.mu.Unlock()
					if got := c.replayCompatLevelFor(cfg.Name(), "test-model", "", lastUserMessageIndex(msgs)); got != modelcompat.ReplayCompatNative {
						t.Fatalf("replay level=%v, want native", got)
					}
				})
			}
		}
	}
}

func TestResponsesClientContractErrorGuidance(t *testing.T) {
	for _, e := range []*APIError{
		{StatusCode: 400, Code: invalidResponsesRequestCode, Message: "request rejected"},
		{StatusCode: 400, Message: invalidCodexRequestMessage},
	} {
		for _, s := range []string{e.Message, "Responses endpoint", "request overrides", "prompt_cache_key", "client_metadata", "session headers"} {
			if !strings.Contains(e.Error(), s) {
				t.Fatalf("error=%q, missing %q", e.Error(), s)
			}
		}
	}
	plain := &APIError{StatusCode: 400, Message: "bad request"}
	if got := plain.Error(); got != "API error 400: bad request" {
		t.Fatalf("unrelated error=%q", got)
	}
	if isRetriable(&APIError{StatusCode: 500, Code: invalidResponsesRequestCode}) {
		t.Fatal("structured request refusal should not rotate keys even with a server status")
	}
	if !isRetriable(&APIError{StatusCode: 500, Message: invalidCodexRequestMessage}) {
		t.Fatal("server failure text alone must remain retryable")
	}
}
