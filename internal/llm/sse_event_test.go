package llm

import "testing"

func TestSSEDataLineTerminatesEvent(t *testing.T) {
	terminal := map[string]struct{}{
		"response.completed": {},
		"message_stop":       {},
	}

	cases := []struct {
		name          string
		data          string
		eventTypeHint string
		want          bool
	}{
		{name: "done sentinel", data: "[DONE]", want: true},
		{name: "combined terminal", data: `{"type":"response.completed","response":{"id":"r"}}`, want: true},
		{name: "standard terminal", eventTypeHint: "message_stop", data: `{"type":"message_stop"}`, want: true},
		{name: "combined non terminal", data: `{"type":"response.output_text.delta","delta":"x"}`, want: false},
		{name: "standard non terminal", eventTypeHint: "message_delta", data: `{"type":"message_delta"}`, want: false},
		{name: "invalid json", eventTypeHint: "message_stop", data: `{`, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sseDataLineTerminatesEvent([]byte(tc.data), tc.eventTypeHint, terminal)
			if got != tc.want {
				t.Fatalf("sseDataLineTerminatesEvent() = %v, want %v", got, tc.want)
			}
		})
	}
}
