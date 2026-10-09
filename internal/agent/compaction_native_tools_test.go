package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func TestCompactionPreservesNativeSearchEvidence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	native := &message.NativeToolHistory{Target: "sample/test-model", RequestIDs: []string{"request-1"}, OutcomeUnknown: true, Authorization: message.NativeToolAuthorization{Tool: toolname.WebSearch},
		Items: []json.RawMessage{json.RawMessage(`{"type":"text","text":"Sample fact","citations":[{"type":"web_search_result_location","url":"https://example.invalid/a","title":"Sample","cited_text":"Source fact"}]}`), json.RawMessage(`{"type":"thinking","signature":"opaque-replay"}`)},
		Calls: []message.HostedCall{{ID: "search-1", Input: json.RawMessage(`{"query":"sample"}`), Result: json.RawMessage(`[]`)}},
	}
	head := []message.Message{{Role: message.RoleUser, Content: "Find sample facts"}, {Role: message.RoleAssistant, Content: "Sample fact", NativeTools: native}}
	input, err := a.buildCompactionInputWithOptions(head, 32000, nil, nil, compactionAnchors{})
	if err != nil {
		t.Fatal(err)
	}
	path, _, _, err := a.exportCompactionHistory(head, head, 1, nil, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, transcript := range []string{input.Transcript, string(archive)} {
		for _, want := range []string{"sample", "https://example.invalid/a", "Source fact", "outcome unknown"} {
			if !strings.Contains(transcript, want) {
				t.Fatalf("missing %q in native evidence: %s", want, transcript)
			}
		}
		if strings.Contains(transcript, "opaque-replay") {
			t.Fatal("opaque replay data entered the readable compaction history")
		}
	}
}
