package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestParseRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
		valid bool
	}{
		{"120", 2 * time.Minute, true},
		{" 2 \t", 2 * time.Second, true},
		{"0", 0, true},
		{"9223372036854775807", time.Duration(math.MaxInt64), true},
		{"9223372036854775808", 0, false},
		{"-1", 0, false},
		{"+2", 0, false},
		{"1.5", 0, false},
		{"", 0, false},
		{"invalid", 0, false},
		{"2\r\n", 0, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got, valid := parseRetryAfter(tc.value)
			if got != tc.want || valid != tc.valid {
				t.Fatalf("parseRetryAfter(%q) = (%v, %v), want (%v, %v)", tc.value, got, valid, tc.want, tc.valid)
			}
		})
	}
	deadline := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	got, valid := parseRetryAfter(deadline.Format(http.TimeFormat))
	if !valid || got <= 0 || got > 2*time.Minute {
		t.Fatalf("future HTTP date = (%v, %v)", got, valid)
	}
	if delay, valid := parseRetryAfter(time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)); !valid || delay != 0 {
		t.Fatalf("past HTTP date = (%v, %v), want (0, true)", delay, valid)
	}
}

func TestHTTPErrorParsersShareRetryAdvice(t *testing.T) {
	for _, parser := range []struct {
		name  string
		parse func(int, http.Header, []byte) *APIError
	}{
		{"openai", parseOpenAIHTTPErrorFromBytes},
		{"anthropic", parseHTTPErrorFromBytes},
		{"gemini", parseGeminiHTTPErrorFromBytes},
	} {
		t.Run(parser.name, func(t *testing.T) {
			err := parser.parse(http.StatusServiceUnavailable, http.Header{"Retry-After": {" 7 "}}, []byte(`{"error":{"message":"temporarily unavailable"}}`))
			if err.RetryAfter != 7*time.Second || err.StatusCode != http.StatusServiceUnavailable || err.Origin != APIErrorOriginHTTPResponse {
				t.Fatalf("HTTP error = %+v", err)
			}
		})
	}
}

func TestResponseErrorHeaders(t *testing.T) {
	raw := json.RawMessage(`{"retry-after":["7"],"x-number":12,"x-text":"value","bad name":"ignored","x-object":{"a":1},"x-bool":true,"x-null":null,"x-newline":"1\r\n2","x-mixed":[1,"2"]}`)
	headers := responseErrorHeaders(raw)
	if headers.Get("Retry-After") != "7" || headers.Get("X-Number") != "12" || headers.Get("X-Text") != "value" || len(headers) != 3 {
		t.Fatalf("headers = %#v", headers)
	}
	for _, value := range []string{"null", "true", `"text"`, "[]", "{"} {
		if got := responseErrorHeaders(json.RawMessage(value)); len(got) != 0 {
			t.Fatalf("malformed headers %s = %#v", value, got)
		}
	}
}

func TestResponsesSSEErrorsRetainRetryAdvice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event string
		want  time.Duration
	}{
		{"failed nested", `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"unavailable","headers":{"rEtRy-AfTeR":"7"}}}}`, 7 * time.Second},
		{"error nested", `{"type":"error","error":{"code":"rate_limit_exceeded","message":"unavailable","headers":{"Retry-After":12}}}`, 12 * time.Second},
		{"error frame", `{"type":"error","code":"rate_limit_exceeded","message":"unavailable","headers":{"Retry-After":["3"]}}`, 3 * time.Second},
		{"nested wins", `{"type":"error","error":{"message":"unavailable","headers":{"Retry-After":"4"}},"headers":{"Retry-After":"9"}}`, 4 * time.Second},
		{"zero wins", `{"type":"error","error":{"message":"unavailable","headers":{"Retry-After":"0"}},"headers":{"Retry-After":"9"}}`, 0},
		{"invalid", `{"type":"response.failed","response":{"error":{"message":"unavailable","headers":{"Retry-After":"invalid"}}}}`, 0},
		{"malformed", `{"type":"response.failed","response":{"error":{"message":"unavailable","headers":true}}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream([]string{tc.event}), nil, nil, nil, "", false, false, false)
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.RetryAfter != tc.want || apiErr.StatusCode != 0 || apiErr.Origin != APIErrorOriginSSEEvent {
				t.Fatalf("parsed SSE error = %#v, want status-less error with %v advice", err, tc.want)
			}
		})
	}
}

func TestCodexWebSocketRetryAdvicePrecedence(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{`"5"`, 5 * time.Second},
		{`5`, 5 * time.Second},
		{`["5"]`, 5 * time.Second},
		{`"0"`, 0},
		{`"invalid"`, 90 * time.Second},
		{`{"delay":5}`, 90 * time.Second},
		{`true`, 90 * time.Second},
	} {
		t.Run(tc.value, func(t *testing.T) {
			frame := fmt.Sprintf(`{"type":"error","status":429,"error":{"type":"rate_limit_exceeded","message":"unavailable","resets_in_seconds":90},"headers":{"retry-after":%s,"x-codex-primary-used-percent":50}}`, tc.value)
			err, headers := parseCodexWebSocketErrorJSON([]byte(frame))
			if err == nil || err.RetryAfter != tc.want || headers.Get("X-Codex-Primary-Used-Percent") != "50" {
				t.Fatalf("WS error = %+v, headers = %#v", err, headers)
			}
		})
	}
}

func TestServerDirectedRetryCooldownPolicies(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *APIError
		want time.Duration
	}{
		{"SSE rate limit", &APIError{Origin: APIErrorOriginSSEEvent, Code: "rate_limit_exceeded"}, 2 * time.Second},
		{"WS overload", &APIError{Origin: APIErrorOriginWebSocketEvent, Code: "server_is_overloaded"}, 2 * time.Second},
		{"HTTP overload", &APIError{Origin: APIErrorOriginHTTPResponse, StatusCode: 503}, 2 * time.Second},
		{"request", &APIError{Origin: APIErrorOriginSSEEvent, Code: "invalid_request_error"}, 0},
		{"context", &APIError{Origin: APIErrorOriginSSEEvent, Code: "context_length_exceeded"}, 0},
		{"upstream interrupted", &APIError{Origin: APIErrorOriginSSEEvent, Code: "upstream_connection_error"}, 0},
		{"policy", &APIError{StatusCode: 400, Origin: APIErrorOriginHTTPResponse, Code: "invalid_request_error"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, RetryAfterMaxS: new(2), RetryBackoff: config.RetryBackoffNone}, []string{"key-a"})
			before := isRetriable(tc.err)
			tc.err.RetryAfter = time.Hour
			if isRetriable(tc.err) != before {
				t.Fatal("retry advice changed error classification")
			}
			result := markKeyCooldown(context.Background(), cfg, "key-a", "test-model", fmt.Errorf("provider: %w", tc.err))
			if result.cooldownApplied != (tc.want > 0) {
				t.Fatalf("cooldownApplied = %v, want delay %v", result.cooldownApplied, tc.want)
			}
			if tc.want > 0 {
				remaining := time.Until(cfg.keyStates[0].CooldownEnd)
				if remaining < tc.want-time.Second || remaining > tc.want {
					t.Fatalf("cooldown = %v, want bounded %v", remaining, tc.want)
				}
			}
		})
	}
	// Explicit auth statuses still run credential handling, regardless of advice.
	auth := &APIError{StatusCode: 401, Origin: APIErrorOriginWebSocketEvent, RetryAfter: time.Hour}
	if delay := serverDirectedRetryCooldown(nil, auth); delay != 0 {
		t.Fatalf("auth error entered generic retry cooldown: %v", delay)
	}
}

func TestCompleteStreamRetryAdviceRotatesToHealthyKey(t *testing.T) {
	for _, visible := range []bool{false, true} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("visible=%v/replay=%v", visible, replay), func(t *testing.T) {
				cfg := testCompatibleResponsesProviderConfigWithKeys("sample", "test-model", []string{"key-a", "key-b"})
				apiErr, err := parseResponsesProviderErrorEvent("response.failed", []byte(`{"response":{"error":{"code":"server_is_overloaded","message":"temporarily unavailable","headers":{"Retry-After":"60"}}}}`))
				if err != nil {
					t.Fatal(err)
				}
				first := scriptedCall{err: apiErr}
				if visible {
					first.streams = []message.StreamDelta{{Type: message.StreamDeltaText, Text: "partial"}}
				}
				impl := new(recordingProvider)
				impl.calls = []scriptedCall{first, {resp: &message.Response{Content: "complete", StopReason: "stop"}}}
				client := NewClient(cfg, impl, "test-model", 4096, "sys")
				client.SetStreamRetryRounds(1)
				messages := []message.Message{{Role: message.RoleUser, Content: "inspect the sample"}}
				if replay {
					messages = crossProviderReplayMessages()
				}
				resp, err := client.CompleteStream(context.Background(), messages, nil, func(message.StreamDelta) {})
				if err != nil || resp == nil || resp.Content != "complete" {
					t.Fatalf("CompleteStream = (%+v, %v)", resp, err)
				}
				if !reflect.DeepEqual(impl.apiKeys, []string{"key-a", "key-b"}) {
					t.Fatalf("attempted keys = %v, want healthy-key rotation without replay probes", impl.apiKeys)
				}
				if delay := time.Until(cfg.keyStates[0].CooldownEnd); delay < 55*time.Second || delay > 60*time.Second {
					t.Fatalf("failed key cooldown = %v, want server-directed minute", delay)
				}
				if !cfg.keyStates[1].CooldownEnd.IsZero() {
					t.Fatal("healthy key was cooled by another key's retry advice")
				}
			})
		}
	}
}

func TestRetryAdviceDoesNotCoolHintlessServerErrors(t *testing.T) {
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses}, []string{"key"})
	err := &APIError{StatusCode: http.StatusServiceUnavailable, Message: "temporarily unavailable"}
	if markKeyCooldown(context.Background(), cfg, "key", "test-model", err).cooldownApplied {
		t.Fatal("hint-less 5xx changed existing cooldown policy")
	}
}
