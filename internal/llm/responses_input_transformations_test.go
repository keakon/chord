package llm

import (
	"bytes"
	"strings"
	"testing"

	"github.com/keakon/golog"

	"github.com/keakon/chord/internal/logtest"
)

func TestResponsesReasoningRemovalDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields string
		want   string
	}{
		{name: "missing", want: "unavailable"},
		{name: "null", fields: `,"input_transformations":null`, want: "unavailable"},
		{name: "wrong_type", fields: `,"input_transformations":{}`, want: "unavailable"},
		{name: "empty", fields: `,"input_transformations":[]`, want: "reported reasoning_items_removed=0 removals_by_reason=map[]"},
		{
			name: "group_reasons",
			fields: `,"input_transformations":[
				{"type":"reasoning","action":"removed","reason":"prefix_mismatch","id":"rs-sample"},
				{"type":"reasoning","action":"removed","reason":"prefix_mismatch"},
				{"type":"reasoning","action":"removed","reason":"turn_boundary"},
				{"type":"reasoning","action":"removed","reason":"future_reason"},
				{"type":"reasoning","action":"removed","reason":7},
				{"type":"reasoning","action":"removed"},
				{"type":"reasoning","action":"changed","reason":"prefix_mismatch"},
				{"type":"message","action":"removed","reason":"prefix_mismatch"},
				null, 42
			]`,
			want: "reported reasoning_items_removed=6 removals_by_reason=map[other:3 prefix_mismatch:2 turn_boundary:1]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logtest.WithDefault(logtest.NewLogger(&logs, golog.DebugLevel), func() {
				event := `{"type":"response.completed","response":{"id":"resp-sample","output":[]` + strings.ReplaceAll(tc.fields, "\n", "") + `}}`
				resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream([]string{event}), nil, nil, nil, "", false, false, false)
				if err != nil {
					t.Fatalf("parse responses SSE: %v", err)
				}
				if resp.ProviderResponseID != "resp-sample" || resp.StopReason != "stop" {
					t.Fatalf("unexpected response: %+v", resp)
				}
			})
			want := `responses: reasoning replay response_id="resp-sample" input_transformations=` + tc.want
			if !strings.Contains(logs.String(), want+"\n") {
				t.Errorf("logs = %q, want %q", logs.String(), want)
			}
			if strings.Contains(logs.String(), "rs-sample") || strings.Contains(logs.String(), "future_reason") {
				t.Errorf("logs contain raw input transformations: %q", logs.String())
			}
		})
	}
}
