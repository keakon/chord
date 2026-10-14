package llm

import (
	"strings"
	"testing"
	"time"
)

func TestCodexWebSocketSessionIdentityControlsConnectionReuse(t *testing.T) {
	for _, prewarm := range []bool{false, true} {
		name := "without_prewarm"
		if prewarm {
			name = "with_prewarm"
		}
		t.Run(name, func(t *testing.T) {
			responseIDs := []string{"response-1", "response-2", "response-3", "response-4", "response-5", "response-6", "response-7", "response-8", "response-9", "response-10"}
			srv := newCodexWSCaptureServer(t, responseIDs)
			defer srv.Close()
			r := &ResponsesProvider{}
			defer r.resetCodexWebSocketChain("test_finished")
			input := []responsesInputItem{{Type: "message", Role: "user", Content: "hello"}}
			requestCount, connectionCount := 0, 0
			for _, step := range []struct {
				key   string
				reuse bool
			}{
				{key: "session-a"},
				{key: " session-a ", reuse: true},
				{key: "session-b"},
				{key: ""},
				{key: "  ", reuse: true},
				{key: "session-a"},
			} {
				sid := strings.TrimSpace(step.key)
				req := testCodexWSResponsesRequest("sample/test-model", input)
				// Leave prompt_cache_key out of the signature: connection isolation
				// must hold even when request compatibility options omit that field.
				_, incremental, err := r.completeStreamCodexWebSocket(t.Context(), srv.ResponsesURL(), "test-key", req.Model, req, input, nil, time.Now(), codexWSCompleteOptions{SkipPrewarm: !prewarm, SessionKey: step.key})
				if err != nil {
					t.Fatal(err)
				}
				if incremental != (step.reuse || prewarm) {
					t.Fatalf("session %q: incremental = %v", step.key, incremental)
				}
				firstRequest := requestCount
				requestCount++
				if !step.reuse {
					connectionCount++
					if prewarm {
						requestCount++
					}
				}
				headers := srv.HandshakeHeaders()
				if len(headers) != connectionCount {
					t.Fatalf("session %q: connections = %d, want %d", step.key, len(headers), connectionCount)
				}
				for _, header := range []string{"session-id", "thread-id", "x-client-request-id"} {
					if got := headers[len(headers)-1].Get(header); got != sid {
						t.Fatalf("session %q: %s = %q", step.key, header, got)
					}
				}
				requests := srv.Requests()
				if len(requests) != requestCount {
					t.Fatalf("requests = %d, want %d", len(requests), requestCount)
				}
				for i := firstRequest; i < requestCount; i++ {
					body := requests[i]
					key, _ := body["prompt_cache_key"].(string)
					if key != sid {
						t.Fatalf("request %d: prompt_cache_key = %q, want %q", i, key, sid)
					}
					if !step.reuse && i == firstRequest {
						assertCodexWSFullInputWithoutPreviousResponseID(t, body, len(input))
					} else if body["previous_response_id"] != responseIDs[i-1] {
						t.Fatalf("request %d: previous_response_id = %v, want %s", i, body["previous_response_id"], responseIDs[i-1])
					}
				}
			}
		})
	}
}
