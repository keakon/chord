package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestRestoredSubAgentBuilderTaskDescPrefersUserAuthoredMessage(t *testing.T) {
	b := newRestoredSubAgentBuilder("sub-1")
	b.attachTranscript([]message.Message{
		{Role: message.RoleUser, Content: "compaction checkpoint body", IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "  Refactor the parser  "},
		{Role: message.RoleUser, Content: "job finished", Kind: message.KindBackgroundResult},
	})
	if b.state.TaskDesc != "Refactor the parser" {
		t.Fatalf("TaskDesc = %q, want the trimmed user-authored task", b.state.TaskDesc)
	}
}

func TestRestoredSubAgentBuilderTaskDescEmptyWithoutUserAuthoredMessage(t *testing.T) {
	b := newRestoredSubAgentBuilder("sub-1")
	b.attachTranscript([]message.Message{
		{Role: message.RoleUser, Content: "compaction checkpoint body", IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "job finished", Kind: message.KindBackgroundResult},
	})
	if b.state.TaskDesc != "" {
		t.Fatalf("TaskDesc = %q, want empty when only synthetic messages exist", b.state.TaskDesc)
	}
}
