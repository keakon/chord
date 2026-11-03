package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

func skillFullResult(t *testing.T, sk *skill.Skill, args string) string {
	t.Helper()
	result := tools.FormatSkillInvocationResult(sk, args)
	if _, ok := tools.SkillResultBodyDigest(result); !ok {
		t.Fatalf("fixture result has no body digest: %q", result)
	}
	return result
}

func skillDigest(t *testing.T, result string) string {
	t.Helper()
	digest, ok := tools.SkillResultBodyDigest(result)
	if !ok {
		t.Fatalf("result has no body digest: %q", result)
	}
	return digest
}

func TestSkillBodyVisibleInMessagesTurnAndCheckpointBoundaries(t *testing.T) {
	root := t.TempDir()
	sk, err := skill.LoadSkill(writeTempSkill(t, root, "code-review", "review the diff", false).Location)
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	full := skillFullResult(t, sk, "")
	digest := skillDigest(t, full)
	call := message.ToolCall{ID: "s1", Name: tools.NameSkill}
	assistant := message.Message{Role: message.RoleAssistant, RequestBatch: 4, ToolCalls: []message.ToolCall{call}}
	result := message.Message{Role: message.RoleTool, ToolCallID: call.ID, Content: full, ToolStatus: message.ToolStatusSuccess}
	user := message.Message{Role: message.RoleUser, Content: "start"}

	cases := []struct {
		name         string
		messages     []message.Message
		currentBatch uint64
		want         bool
	}{
		{
			name:         "loaded earlier in the same turn",
			messages:     []message.Message{user, assistant, result},
			currentBatch: 4,
			want:         true,
		},
		{
			name:         "synthetic user messages do not end the turn",
			messages:     []message.Message{user, {Role: message.RoleUser, Kind: message.KindStreamContinue, Content: "continue"}, assistant, result},
			currentBatch: 4,
			want:         true,
		},
		{
			name:         "a newer user message starts a new turn",
			messages:     []message.Message{user, assistant, result, {Role: message.RoleUser, Content: "next request"}},
			currentBatch: 4,
			want:         false,
		},
		{
			name:     "a checkpoint replaced the head that held the body",
			messages: []message.Message{user, assistant, result, {Role: message.RoleUser, IsCompactionSummary: true, Content: "[Context Summary]"}},
			want:     false,
		},
		{
			name:         "an aged copy is already summarized on this request",
			messages:     []message.Message{user, assistant, result},
			currentBatch: 6,
			want:         false,
		},
		{
			name:     "a summarized copy carries no digest",
			messages: []message.Message{user, assistant, {Role: message.RoleTool, ToolCallID: call.ID, Content: "[Older skill output summarized for this request; the full text stays readable at the address below]\n<name>code-review</name>", ToolStatus: message.ToolStatusSuccess}},
			want:     false,
		},
		{
			name:     "a failed load never counts",
			messages: []message.Message{user, assistant, {Role: message.RoleTool, ToolCallID: call.ID, Content: full, ToolStatus: message.ToolStatusError}},
			want:     false,
		},
		{
			name:     "the short already-loaded result never counts as the body",
			messages: []message.Message{user, assistant, {Role: message.RoleTool, ToolCallID: call.ID, Content: tools.FormatSkillAlreadyLoadedResult(sk, ""), ToolStatus: message.ToolStatusSuccess}},
			want:     false,
		},
		{
			name: "an aged copy does not hide a newer fresh copy",
			messages: []message.Message{
				user,
				{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "old", Name: tools.NameSkill}}},
				{Role: message.RoleTool, ToolCallID: "old", Content: full, ToolStatus: message.ToolStatusSuccess},
				{Role: message.RoleAssistant, RequestBatch: 4, ToolCalls: []message.ToolCall{{ID: "new", Name: tools.NameSkill}}},
				{Role: message.RoleTool, ToolCallID: "new", Content: full, ToolStatus: message.ToolStatusSuccess},
			},
			currentBatch: 4,
			want:         true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skillBodyVisibleInMessages(tc.messages, digest, tc.currentBatch, 3); got != tc.want {
				t.Fatalf("skillBodyVisibleInMessages = %v, want %v", got, tc.want)
			}
		})
	}
}

// The observed duplicate happened because the checkpoint told the model the
// instructions were gone and the tool answered the reload with a second full
// copy. With both fixed, a reload in the same turn must return only the
// envelope and point at the copy already in context.
func TestSkillInvocationStatesOnlyMarkVisibleBodiesLoaded(t *testing.T) {
	root := t.TempDir()
	meta := writeTempSkill(t, root, "code-review", "review the diff", false)
	sk, err := skill.LoadSkill(meta.Location)
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	view := skillActivationView{Catalog: []*skill.Meta{meta}}
	call := message.ToolCall{ID: "skill-1", Name: tools.NameSkill, Args: []byte(`{"name":"code-review"}`)}
	assistant := message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{call}}

	fullHistory := []message.Message{
		{Role: message.RoleUser, Content: "review the change"},
		assistant,
		{Role: message.RoleTool, ToolCallID: call.ID, Content: skillFullResult(t, sk, ""), ToolStatus: message.ToolStatusSuccess},
	}
	states := skillInvocationStatesFromVisibleBodies(fullHistory, view, 0, 0)
	if len(states) != 1 || !states[0].BodyPresent {
		t.Fatalf("full body should be marked loaded: %+v", states)
	}

	envelopeHistory := []message.Message{
		{Role: message.RoleUser, IsCompactionSummary: true, Content: "[Context Summary]"},
		assistant,
		{Role: message.RoleTool, ToolCallID: call.ID, Content: tools.FormatSkillAlreadyLoadedResult(sk, ""), ToolStatus: message.ToolStatusSuccess},
	}
	states = skillInvocationStatesFromVisibleBodies(envelopeHistory, view, 0, 0)
	if len(states) != 1 || states[0].BodyPresent {
		t.Fatalf("name-only already-loaded envelope must not be marked loaded: %+v", states)
	}
}

func TestMainAgentSkillReloadShortCircuitsVisibleBody(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	meta := writeTempSkill(t, projectRoot, "code-review", "review the diff carefully", false)
	a.SetSkills([]*skill.Meta{meta})
	tool := tools.NewSkillTool(a)
	raw := []byte(`{"name":"code-review"}`)

	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "review the change"})
	first, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if !strings.Contains(first, "review the diff carefully") {
		t.Fatalf("first load should return the full body: %q", first)
	}
	digest := skillDigest(t, first)

	a.ctxMgr.Append(message.Message{
		Role:         message.RoleAssistant,
		RequestBatch: 1,
		ToolCalls:    []message.ToolCall{{ID: "skill-1", Name: tools.NameSkill, Args: append([]byte(nil), raw...)}},
	})
	a.ctxMgr.Append(message.Message{
		Role:       message.RoleTool,
		ToolCallID: "skill-1",
		Content:    first,
		ToolStatus: message.ToolStatusSuccess,
	})
	a.requestBatches.reserve(a.sessionEpoch, 0)
	if !a.HasSkillBodyInContext(digest) {
		t.Fatal("the full body from this turn should be reported visible")
	}

	second, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if !strings.Contains(second, "<already_loaded>") {
		t.Fatalf("reload should short-circuit, got %q", second)
	}
	if strings.Contains(second, "review the diff carefully") {
		t.Fatalf("reload must not repeat the body: %q", second)
	}

	// A new user turn moves past the earlier work, so the tool must hand out
	// the body again rather than point at context the model should ignore.
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "now review the follow-up"})
	if a.HasSkillBodyInContext(digest) {
		t.Fatal("a body from a previous turn must not count as visible")
	}
	third, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("third Execute: %v", err)
	}
	if !strings.Contains(third, "review the diff carefully") {
		t.Fatalf("a new turn should get the full body again, got %q", third)
	}
}
