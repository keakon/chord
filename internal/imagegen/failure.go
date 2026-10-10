package imagegen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/chord/internal/httpheader"
)

const (
	FailureAuthentication = "authentication"
	FailureQuota          = "quota_exhausted"
	FailureRateLimit      = "rate_limit"
	FailureInvalidRequest = "invalid_request"
	FailureProvider       = "provider_error"
)

var zeroQuotaLimit = regexp.MustCompile(`(?i)\blimit:\s*0(?:[,;\s]|$)`)

// FailureDetails retains bounded protocol facts and redacted error diagnostics.
type FailureDetails struct {
	Message           string               `json:"message,omitempty"`
	Param             string               `json:"param,omitempty"`
	Request           *RequestDiagnostics  `json:"request,omitempty"`
	Response          *ResponseDiagnostics `json:"response,omitempty"`
	Category          string               `json:"category"`
	HTTPStatus        int                  `json:"http_status,omitempty"`
	Code              string               `json:"code,omitempty"`
	Type              string               `json:"type,omitempty"`
	Status            string               `json:"status,omitempty"`
	Reason            string               `json:"reason,omitempty"`
	ZeroQuota         bool                 `json:"zero_quota,omitempty"`
	RetryAfterSeconds *float64             `json:"retry_after_seconds,omitempty"`
	SkippedTargets    []string             `json:"skipped_targets,omitempty"`
}

func (d FailureDetails) RetryAfter() time.Duration {
	if d.RetryAfterSeconds == nil {
		return 0
	}
	return time.Duration(min(max(*d.RetryAfterSeconds, 0), 86400) * float64(time.Second))
}

func (d FailureDetails) String() string {
	var text strings.Builder
	text.WriteString("category=")
	text.WriteString(d.Category)
	for _, field := range []struct{ name, value string }{{"code", d.Code}, {"type", d.Type}, {"status", d.Status}, {"reason", d.Reason}, {"param", d.Param}} {
		if field.value != "" {
			text.WriteByte(' ')
			text.WriteString(field.name)
			text.WriteByte('=')
			text.WriteString(field.value)
		}
	}
	if d.Message != "" {
		text.WriteString(" message=")
		text.WriteString(strconv.Quote(d.Message))
	}
	if d.RetryAfterSeconds != nil {
		text.WriteString(fmt.Sprintf(" retry_after=%.3fs", *d.RetryAfterSeconds))
	}
	if len(d.SkippedTargets) > 0 {
		text.WriteString("; excluded image targets: ")
		text.WriteString(strings.Join(d.SkippedTargets, "; "))
	}
	switch d.Category {
	case FailureAuthentication:
		text.WriteString("; update image API credentials; do not wait or automatically retry")
	case FailureQuota:
		text.WriteString("; image quota or billing must be updated; do not wait or automatically retry")
	case FailureRateLimit:
		text.WriteString("; report retry timing; do not repeatedly regenerate or use shell sleep")
	}
	return text.String()
}

func classifyImageHTTPFailure(status int, headers http.Header, data []byte, secrets []string) *Failure {
	var payload struct {
		Error struct {
			Code    any             `json:"code"`
			Type    string          `json:"type"`
			Status  string          `json:"status"`
			Message string          `json:"message"`
			Param   json.RawMessage `json:"param"`
			Details []struct {
				Type       string `json:"@type"`
				Reason     string `json:"reason"`
				RetryDelay string `json:"retryDelay"`
				Violations []struct {
					QuotaValue json.RawMessage `json:"quotaValue"`
				} `json:"violations"`
			} `json:"details"`
		} `json:"error"`
	}
	parsed := json.Unmarshal(data, &payload) == nil
	code, kind, statusText := fmt.Sprint(payload.Error.Code), payload.Error.Type, payload.Error.Status
	reason := ""
	d := FailureDetails{Category: FailureProvider, HTTPStatus: status}
	d.Code = safeDiagnosticToken(fmt.Sprint(payload.Error.Code), secrets)
	d.Type = safeDiagnosticToken(payload.Error.Type, secrets)
	d.Status = safeDiagnosticToken(payload.Error.Status, secrets)
	var param string
	_ = json.Unmarshal(payload.Error.Param, &param)
	d.Param = safeDiagnosticToken(param, secrets)
	d.Message = safeImageErrorMessage(payload.Error.Message, secrets)
	d.Response = responseDiagnostics(headers, len(data), secrets)
	// Some services omit the proto3 zero quota value from structured details.
	d.ZeroQuota = statusText == "RESOURCE_EXHAUSTED" && zeroQuotaLimit.MatchString(payload.Error.Message)
	if delay, ok := httpheader.ParseRetryAfter(headers.Get("Retry-After")); ok {
		d.RetryAfterSeconds = new(min(delay, 24*time.Hour).Seconds())
	}
	for _, detail := range payload.Error.Details {
		switch detail.Type {
		case "type.googleapis.com/google.rpc.RetryInfo":
			if d.RetryAfterSeconds == nil {
				if delay, err := time.ParseDuration(detail.RetryDelay); err == nil && delay >= 0 {
					d.RetryAfterSeconds = new(min(delay, 24*time.Hour).Seconds())
				}
			}
		case "type.googleapis.com/google.rpc.ErrorInfo":
			reason = detail.Reason
			d.Reason = safeDiagnosticToken(reason, secrets)
		case "type.googleapis.com/google.rpc.QuotaFailure":
			for _, violation := range detail.Violations {
				value := strings.TrimSpace(string(violation.QuotaValue))
				d.ZeroQuota = d.ZeroQuota || value == "0" || value == `"0"`
			}
		}
	}
	googleCredential := statusText == "UNAUTHENTICATED" || reason == "API_KEY_INVALID"
	credential := parsed && ((status == 401 || status == 403) && (code == "invalid_api_key" || kind == "authentication_error" || googleCredential) || status == 400 && googleCredential)
	limit := parsed && status == 429 && (code == "insufficient_quota" || code == "rate_limit_exceeded" || statusText == "RESOURCE_EXHAUSTED")
	hardQuota := parsed && (status == 429 || status == 403) && (code == "insufficient_quota" || reason == "BILLING_DISABLED" || reason == "BILLING_NOT_ACTIVE" || d.ZeroQuota)
	invalid := parsed && status == 400 && (code == "invalid_request_error" || kind == "invalid_request_error" || statusText == "INVALID_ARGUMENT")
	state := StateUnknown
	switch {
	case credential:
		state, d.Category = StateRejected, FailureAuthentication
	case hardQuota:
		state, d.Category = StateRejected, FailureQuota
	case limit:
		state, d.Category = StateRejected, FailureRateLimit
	case invalid:
		state, d.Category = StateRejected, FailureInvalidRequest
	}
	id := headers.Get("x-request-id")
	if id == "" {
		id = headers.Get("request-id")
	}
	return &Failure{State: state, RequestID: safeDiagnosticToken(id, secrets), RetryKey: credential || limit || hardQuota, Details: d, Cause: fmt.Errorf("HTTP %d (provider rejected or failed image request)", status)}
}
