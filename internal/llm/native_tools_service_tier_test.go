package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestNativeRequestTierFollowsTargetAcrossContinuations(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tier      config.ServiceTier
		supported bool
		fallback  bool
	}{
		{"standard", config.ServiceTierStandard, false, false},
		{"fast", config.ServiceTierFast, true, false},
		{"slow", config.ServiceTierSlow, true, false},
		{"unsupported", config.ServiceTierFast, false, false},
		{"fallback fast", config.ServiceTierFast, true, true},
		{"fallback unsupported", config.ServiceTierFast, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := config.ModelConfig{NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchMessages, APIURL: "https://example.invalid/messages", Preauthorized: true}}
			if tc.supported {
				model.SupportedServiceTiers = []config.ServiceTier{tc.tier}
			}
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: model.NativeWebSearch.APIURL, Models: map[string]config.ModelConfig{"test-model": model}}, []string{"key"})
			impl := &scriptedProvider{calls: []scriptedCall{
				{resp: &message.Response{Content: "Searching.", StopReason: "pause_turn", Hosted: &message.HostedObservation{Items: []json.RawMessage{json.RawMessage(`{"type":"text","text":"Searching."}`)}}}},
				{resp: &message.Response{Content: "Complete.", StopReason: "end_turn"}},
			}}
			client := NewClient(provider, impl, "test-model", 1024, "")
			if tc.fallback {
				first := NewProviderConfig("first", config.ProviderConfig{Type: config.ProviderTypeMessages, Models: map[string]config.ModelConfig{"test-model": {SupportedServiceTiers: []config.ServiceTier{tc.tier}}}}, []string{"key"})
				client.Close()
				client = NewClient(first, &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 503}}}}, "test-model", 1024, "")
				client.SetFallbackModels([]FallbackModel{{ProviderConfig: provider, ProviderImpl: impl, ModelID: "test-model", MaxTokens: 1024}})
			}
			defer client.Close()
			client.SetServiceTier(tc.tier)
			client.SetStreamRetryRounds(1)
			want := config.ServiceTierStandard
			if tc.supported {
				want = tc.tier
			}
			begins, finishes := 0, 0
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true },
				Begin: func(_ context.Context, record NativeRequestRecord) (string, error) {
					if record.ServiceTier != want || record.Target != "sample/test-model" || record.Continuation != begins {
						t.Errorf("request target=%s tier=%s continuation=%d; want tier=%s continuation=%d", record.Target, record.ServiceTier, record.Continuation, want, begins)
					}
					begins++
					return fmt.Sprintf("request-%d", begins), nil
				},
				Finish: func(_ string, outcome message.NativeRequestOutcome, _ *message.Response, err error) error {
					finishes++
					if err != nil || outcome != message.NativeRequestCompleted {
						t.Errorf("outcome=%s err=%v", outcome, err)
					}
					return nil
				},
			}
			_, err := client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Search sample records."}}, nil, nil, CompleteStreamOptions{NativeTools: policy})
			if err != nil || begins != 2 || finishes != 2 {
				t.Fatalf("begins=%d finishes=%d err=%v", begins, finishes, err)
			}
		})
	}
}
