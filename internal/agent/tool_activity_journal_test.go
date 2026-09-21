package agent

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestToolActivityJournalRequiredCoversConsumingJobRead(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.JobOutputTool{})
	registry.Register(tools.GrepTool{})
	registry.Register(tools.JobKillTool{})
	journaled := func(name, args string) bool {
		return toolActivityJournalRequired(registry, message.ToolCall{Name: name, Args: json.RawMessage(args)})
	}

	if !journaled(tools.NameJobOutput, `{"job_id":"job-1"}`) {
		t.Fatal("job_output must be journaled: its read consumes the job cursor and can claim the completion notification")
	}
	if journaled(tools.NameGrep, `{"pattern":"TODO","paths":["."]}`) {
		t.Fatal("a re-runnable read must stay out of the started journal")
	}
	if !journaled(tools.NameJobKill, `{"job_id":"job-1"}`) {
		t.Fatal("job_kill mutates job state and must be journaled")
	}
}

func TestToolActivityJournalRequiredCoversShellReadOnly(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	journaled := func(args string) bool {
		return toolActivityJournalRequired(registry, message.ToolCall{Name: tools.NameShell, Args: json.RawMessage(args)})
	}

	// Even a provably read-only shell is journaled: a misclassified write
	// must restore as outcome_unknown, never as not_started ("safe to retry").
	if !journaled(`{"command":"git log --oneline -20"}`) {
		t.Fatal("read-only shell must be journaled")
	}
	if !journaled(`{"command":"rg --no-config -n pat --glob '*.go'"}`) {
		t.Fatal("read-only rg shell must be journaled")
	}
	if !journaled(`{"command":"go test ./..."}`) {
		t.Fatal("mutating shell must be journaled")
	}
}
