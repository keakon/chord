package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesPhysicalRequestTiming(t *testing.T) {
	for _, scenario := range []string{"first request", "chain reset", "incremental retry", "http fallback", "retry then http", "no visible output"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := NewProviderConfig("openai", config.ProviderConfig{
					Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex,
					APIURL: "https://example.invalid/v1/responses", ResponsesWebsocket: new(true),
					Compress: config.RequestCompressionOff,
					Models:   map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
				}, []string{"test-key"})
				cfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{},
					map[string]OAuthKeySetup{"test-key": {CredentialIndex: 0, AccountID: "test-account", Expires: 32503680000000}}, "")
				wsCalls, httpCalls := 0, 0
				provider := &ResponsesProvider{provider: cfg, client: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					httpCalls++
					time.Sleep(time.Second)
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req,
						Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
							"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-test\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":100}}}\n\n"))}, nil
				})}}
				provider.codexWSCompleteFn = func(ctx context.Context, _, _, _ string, _ *responsesRequest, _ []responsesInputItem, cb StreamCallback, _ time.Time, _ codexWSCompleteOptions) (*message.Response, bool, error) {
					wsCalls++
					if (scenario != "first request" && wsCalls == 1) || scenario == "retry then http" {
						time.Sleep(2 * time.Second)
						cb(message.StreamDelta{Type: message.StreamDeltaText, Text: "discarded"})
						if scenario == "chain reset" || scenario == "no visible output" {
							return nil, false, &APIError{StatusCode: http.StatusBadRequest, Message: "No tool call found for function call output with call_id call_sample."}
						}
						return nil, scenario == "incremental retry" || (scenario == "retry then http" && wsCalls == 1), errors.New("connection closed")
					}
					// Connection setup precedes the transport submission observer.
					time.Sleep(250 * time.Millisecond)
					markRequestSent(ctx)
					time.Sleep(500 * time.Millisecond)
					if scenario != "no visible output" {
						cb(message.StreamDelta{Type: message.StreamDeltaText, Text: "hello"})
					}
					time.Sleep(250 * time.Millisecond)
					return &message.Response{Content: "hello", StopReason: "end_turn", Usage: &message.TokenUsage{OutputTokens: 100}}, false, nil
				}
				client := NewClient(cfg, provider, "test-model", 1024, "")
				resp, err := client.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hello"}}, nil, func(message.StreamDelta) {})
				if err != nil {
					t.Fatal(err)
				}
				wantFirst := 750 * time.Millisecond
				if scenario == "no visible output" {
					wantFirst = 0
				}
				if scenario == "http fallback" || scenario == "retry then http" {
					wantFirst = time.Second
					if httpCalls != 1 {
						t.Fatalf("HTTP calls = %d, want 1", httpCalls)
					}
				} else if httpCalls != 0 {
					t.Fatalf("unexpected HTTP calls: %d", httpCalls)
				}
				wantWS := 2
				if scenario == "first request" || scenario == "http fallback" {
					wantWS = 1
				}
				if wsCalls != wantWS {
					t.Fatalf("WS calls = %d, want %d", wsCalls, wantWS)
				}
				if resp.RequestNs != int64(time.Second) || resp.FirstOutputNs != int64(wantFirst) {
					t.Fatalf("timing = %v/%v, want 1s/%v", time.Duration(resp.RequestNs), time.Duration(resp.FirstOutputNs), wantFirst)
				}
			})
		})
	}
}

func TestResponsesTerminalDrainReturnsWithoutUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := NewProviderConfig("provider", config.ProviderConfig{
			Type:   config.ProviderTypeResponses,
			APIURL: "https://example.invalid/v1/responses",
			Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
		}, []string{"test-key"})
		provider, err := NewResponsesProviderWithClient(cfg, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			time.Sleep(100 * time.Millisecond)
			reader, writer := io.Pipe()
			go func() {
				defer writer.Close()
				_, _ = io.WriteString(writer,
					"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"item-1\",\"call_id\":\"call-1\",\"name\":\"lookup\"}}\n\n"+
						"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"q\\\":\\\"example\\\"}\"}\n\n"+
						"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"item-1\",\"call_id\":\"call-1\",\"name\":\"lookup\",\"status\":\"completed\"}}\n\n")
				<-req.Context().Done()
				_ = writer.CloseWithError(req.Context().Err())
			}()
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req, Body: reader}, nil
		})}, "")
		if err != nil {
			t.Fatal(err)
		}
		client := NewClient(cfg, provider, "test-model", 1024, "")
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		start := time.Now()
		resp, err := client.CompleteStream(ctx, []message.Message{{Role: message.RoleUser, Content: "hello"}}, nil, func(message.StreamDelta) {})
		if err != nil {
			t.Fatal(err)
		}
		if resp.StopReason != "tool_calls" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "lookup" {
			t.Fatalf("response = %+v, want the completed tool call", resp)
		}
		if resp.Usage != nil {
			t.Fatalf("usage = %+v, want no fabricated token counts", resp.Usage)
		}
		wantDuration := 100*time.Millisecond + TerminalDrainChunkTimeout
		if elapsed := time.Since(start); elapsed != wantDuration || resp.RequestNs != int64(wantDuration) {
			t.Fatalf("elapsed/request = %v/%v, want terminal drain return after %v", elapsed, time.Duration(resp.RequestNs), wantDuration)
		}
		if resp.FirstOutputNs != int64(100*time.Millisecond) {
			t.Fatalf("first output = %v, want measured 100ms latency", time.Duration(resp.FirstOutputNs))
		}
	})
}
