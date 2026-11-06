package llm

import (
	"io"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestSSEStatusBeforeFirstEvent(t *testing.T) {
	for _, body := range []string{": ping\n\n", "data: partial", ""} {
		var statuses []string
		reader := newLLMSSEReader(strings.NewReader(body), func(delta message.StreamDelta) {
			if delta.Status != nil {
				statuses = append(statuses, delta.Status.Type)
			}
		}, sseMaxTokenSize, nil)
		_, err := reader.Next()
		if err != io.EOF {
			t.Fatalf("error = %v, want EOF", err)
		}
		reader.Next()
		if body == "" {
			if len(statuses) != 0 {
				t.Fatalf("empty body statuses = %v", statuses)
			}
		} else if len(statuses) != 1 || statuses[0] != message.StatusDeltaWaitingToken {
			t.Fatalf("body=%q statuses=%v, want one waiting-token status", body, statuses)
		}
	}
}
