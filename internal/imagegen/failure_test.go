package imagegen

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestImageHTTPFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		status                int
		body, category, state string
		retry                 bool
		zero                  bool
	}{
		{"non-string diagnostic param", 401, `{"error":{"code":"invalid_api_key","param":{"field":"image"}}}`, FailureAuthentication, StateRejected, true, false},
		{"google invalid key", 400, `{"error":{"status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID"}]}}`, FailureAuthentication, StateRejected, true, false},
		{"google unauthenticated", 400, `{"error":{"status":"UNAUTHENTICATED"}}`, FailureAuthentication, StateRejected, true, false},
		{"gateway server credentials", 503, `{"error":{"status":"UNAUTHENTICATED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID"}]}}`, FailureProvider, StateUnknown, false, false},
		{"not found", 404, `{"error":{"message":"not found"}}`, FailureProvider, StateUnknown, false, false},
		{"credentials", 401, `{"error":{"code":"invalid_api_key"}}`, FailureAuthentication, StateRejected, true, false},
		{"quota", 429, `{"error":{"code":"insufficient_quota"}}`, FailureQuota, StateRejected, true, false},
		{"rate", 429, `{"error":{"code":"rate_limit_exceeded"}}`, FailureRateLimit, StateRejected, true, false},
		{"google zero", 429, `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaValue":"0"}]}]}}`, FailureQuota, StateRejected, true, true},
		{"google omitted zero", 429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded, limit: 0, model: sample"}}`, FailureQuota, StateRejected, true, true},
		{"google nonzero", 429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded, limit: 10"}}`, FailureRateLimit, StateRejected, true, false},
		{"billing", 403, `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"BILLING_DISABLED"}]}}`, FailureQuota, StateRejected, true, false},
		{"invalid", 400, `{"error":{"status":"INVALID_ARGUMENT"}}`, FailureInvalidRequest, StateRejected, false, false},
		{"unstructured rate", 429, `overloaded`, FailureProvider, StateUnknown, false, false},
		{"unknown structured rate", 429, `{"error":{"message":"try again"}}`, FailureProvider, StateUnknown, false, false},
		{"server", 503, `{"error":{"code":"rate_limit_exceeded"}}`, FailureProvider, StateUnknown, false, false},
		{"permission", 403, `{"error":{"status":"PERMISSION_DENIED"}}`, FailureProvider, StateUnknown, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyImageHTTPFailure(tc.status, http.Header{}, []byte(tc.body), nil)
			if got.State != tc.state || got.RetryKey != tc.retry || got.Details.Category != tc.category || got.Details.ZeroQuota != tc.zero {
				t.Fatalf("failure=%+v details=%+v", got, got.Details)
			}
		})
	}
}

func TestImageFailureRetryAdviceAndRedaction(t *testing.T) {
	body := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","code":"private-token","type":"https://example.invalid/?secret=token","message":"secret request body","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"4.5s"},{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"private-reason"}]}}`)
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{{"", 4500 * time.Millisecond}, {"invalid", 4500 * time.Millisecond}, {"0", 0}, {"7", 7 * time.Second}, {"999999", 24 * time.Hour}} {
		headers := http.Header{}
		headers.Set("Retry-After", tc.header)
		got := classifyImageHTTPFailure(429, headers, body, []string{"private-token", "private-reason", "secret request body"})
		if got.Details.RetryAfterSeconds == nil || got.Details.RetryAfter() != tc.want {
			t.Fatalf("header=%q details=%+v", tc.header, got.Details)
		}
		data, err := json.Marshal(got.Details)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data) + got.Error()
		for _, secret := range []string{"private-token", "private-reason", "secret request body", "example.invalid"} {
			if strings.Contains(text, secret) {
				t.Fatalf("leaked %q", secret)
			}
		}
	}
}
