package llm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesProviderWebSocketPromptCacheCompat(t *testing.T) {
	for _, tc := range []struct {
		name           string
		compat         *config.ResponsesCompatConfig
		wantOptions    bool
		wantBreakpoint bool
	}{
		{name: "defaults", wantOptions: true, wantBreakpoint: true},
		{name: "options off", compat: &config.ResponsesCompatConfig{SendPromptCacheOptions: new(false)}, wantBreakpoint: true},
		{name: "breakpoints off", compat: &config.ResponsesCompatConfig{SendPromptCacheBreakpoint: new(false)}},
		{name: "breakpoints off options on", compat: &config.ResponsesCompatConfig{SendPromptCacheBreakpoint: new(false), SendPromptCacheOptions: new(true)}},
		{name: "both off", compat: &config.ResponsesCompatConfig{SendPromptCacheBreakpoint: new(false), SendPromptCacheOptions: new(false)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, incremental := range []bool{false, true} {
				t.Run(map[bool]string{false: "full generation", true: "incremental generation"}[incremental], func(t *testing.T) {
					prewarmID := ""
					if incremental {
						prewarmID = "prewarm-a"
					}
					srv := newCodexWSCaptureServer(t, []string{prewarmID, "response-a"})
					defer srv.Close()
					cfg := NewProviderConfig("sample", config.ProviderConfig{
						Type:               config.ProviderTypeResponses,
						Preset:             config.ProviderPresetCodex,
						APIURL:             srv.ResponsesURL(),
						ResponsesWebsocket: new(true),
						Compat:             &config.ProviderCompatConfig{Responses: tc.compat},
					}, []string{"oauth-key"})
					defer cfg.Close()
					cfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{
						"oauth-key": {CredentialIndex: 0, AccountID: "account-a", Expires: 32503680000000},
					}, "")
					r := &ResponsesProvider{provider: cfg, client: srv.server.Client()}
					defer r.resetCodexWebSocketChain("test_cleanup")
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					_, err := r.CompleteStream(ctx, "oauth-key", "gpt-6.1-sol", "", []message.Message{
						{Role: message.RoleUser, Content: "request"},
						{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "hint"},
					}, nil, 0, RequestTuning{SessionKey: "session-a"}, func(message.StreamDelta) {})
					if err != nil {
						t.Fatal(err)
					}
					requests := srv.Requests()
					if len(requests) != 2 || requests[0]["generate"] != false || requests[1]["generate"] == false {
						t.Fatalf("want prewarm and generation, got %+v", requests)
					}
					for i, body := range requests {
						if _, has := body["prompt_cache_options"]; has != tc.wantOptions {
							t.Fatalf("options present=%v, want %v", has, tc.wantOptions)
						}
						wantBreakpoint := tc.wantBreakpoint && (i == 0 || !incremental)
						if got := responsesBreakpointCount(body); (got > 0) != wantBreakpoint {
							t.Fatalf("request %d breakpoints=%d, want present %v", i, got, wantBreakpoint)
						}
					}
					if incremental {
						// Prewarm already sent the full input; generation sends an empty delta.
						if len(requests[1]["input"].([]any)) != 0 || requests[1]["previous_response_id"] != prewarmID {
							t.Fatalf("incremental generation did not reuse prewarm: %+v", requests[1])
						}
					} else if len(requests[1]["input"].([]any)) == 0 {
						t.Fatal("generation omitted full input without a prewarm response ID")
					}
				})
			}
		})
	}
}
