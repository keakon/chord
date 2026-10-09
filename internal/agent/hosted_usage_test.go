package agent

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestHostedUsageCountsWireAttemptsAndFailedConsumption(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	provider := &hostedScriptProvider{respond: func(i int, _ context.Context) (*message.Response, error) {
		if i == 0 {
			return &message.Response{Usage: &message.TokenUsage{InputTokens: 7, OutputTokens: 3}}, io.ErrUnexpectedEOF
		}
		resp := hostedWebSearchResponse()
		resp.Hosted.Calls = append(resp.Hosted.Calls, message.HostedCall{ID: "call-2", Name: tools.NameWebSearch, Result: json.RawMessage(`[]`)})
		resp.Usage = &message.TokenUsage{InputTokens: 11, OutputTokens: 5}
		return resp, nil
	}}
	setHostedTestPool(a, hostedRetryTarget("sample", []string{"key"}, provider))
	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(e analytics.UsageEvent) { events = append(events, e) })
	_, err := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(t.Context(), tools.NameWebSearch, map[string]any{"query": "sample"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events=%+v", events)
	}
	if events[0].UsageRaw.InputTokens != 7 || events[1].UsageRaw.InputTokens != 11 {
		t.Fatalf("consumption=%+v", events)
	}
	if events[0].Diagnostic["model_requests"] != "1" || events[0].Diagnostic["request_failed"] != "true" || events[1].Diagnostic["retry"] != "true" || events[1].Diagnostic["observed_tool_calls"] != "2" {
		t.Fatalf("diagnostics=%+v / %+v", events[0].Diagnostic, events[1].Diagnostic)
	}
}
