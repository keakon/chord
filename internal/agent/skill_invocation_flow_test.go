package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/skill"
)

func TestHandleToolResultMarksSkillInvokedOnlyOnSuccess(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills(nil)
	a.newTurn()

	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{
			{ID: "skill-1", Name: "skill", Args: []byte(`{"name":"missing-skill"}`)},
		},
	}
	a.ctxMgr.Append(assistant)
	a.turn.PendingToolCalls.Store(1)
	a.turn.TotalToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "skill-1", Name: "skill", ArgsJSON: `{"name":"missing-skill"}`})

	a.handleToolResult(Event{Type: EventToolResult, TurnID: a.turn.ID, Payload: &ToolResultPayload{
		CallID:   "skill-1",
		Name:     "skill",
		ArgsJSON: `{"name":"missing-skill"}`,
		Result:   "",
		Error:    context.Canceled,
		TurnID:   a.turn.ID,
	}})

	if got := a.InvokedSkills(); len(got) != 0 {
		t.Fatalf("InvokedSkills() after failed skill = %#v, want none", got)
	}
}

func TestHandleToolResultMarksSuccessfulVisibleSkillInvoked(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Location: "/tmp/go-expert/SKILL.md", RootDir: "/tmp/go-expert"}})
	a.newTurn()

	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{
			{ID: "skill-1", Name: "skill", Args: []byte(`{"name":"go-expert"}`)},
		},
	}
	a.ctxMgr.Append(assistant)
	a.turn.PendingToolCalls.Store(1)
	a.turn.TotalToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "skill-1", Name: "skill", ArgsJSON: `{"name":"go-expert"}`})

	a.MarkSkillInvokedByName("go-expert")
	if got := a.InvokedSkills(); len(got) != 1 {
		t.Fatalf("precondition InvokedSkills() = %#v, want one invoked skill", got)
	}
	a.skillsMu.Lock()
	a.invokedSkills = make(map[string]*skill.Meta)
	a.skillsMu.Unlock()

	a.handleToolResult(Event{Type: EventToolResult, TurnID: a.turn.ID, Payload: &ToolResultPayload{
		CallID:   "skill-1",
		Name:     "skill",
		ArgsJSON: `{"name":"go-expert"}`,
		Result:   "<skill>...</skill>",
		TurnID:   a.turn.ID,
	}})

	got := a.InvokedSkills()
	if len(got) != 1 {
		t.Fatalf("InvokedSkills() after successful skill = %#v, want one", got)
	}
	if got[0].Name != "go-expert" || !got[0].Invoked || !got[0].Discovered {
		t.Fatalf("successful invoked skill = %#v, want discovered invoked go-expert", got[0])
	}
}
