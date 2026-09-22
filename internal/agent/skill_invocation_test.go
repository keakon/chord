package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

// writeTempSkill writes a SKILL.md under root/name and returns its metadata.
func writeTempSkill(t *testing.T, root, name, body string, manual bool) *skill.Meta {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	front := "---\nname: " + name + "\ndescription: test skill for " + name + "\n"
	if manual {
		front += "disable-model-invocation: true\n"
	}
	front += "---\n\n" + body + "\n"
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(front), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	meta, err := skill.LoadMeta(path)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	return meta
}

func TestParseUserSkillCommand(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{in: "/skill", wantOK: false},
		{in: "/skill   ", wantOK: false},
		{in: "/skills go-expert", wantOK: false},
		{in: "hello /skill go-expert", wantOK: false},
		{in: "/skill go-expert", wantName: "go-expert", wantOK: true},
		{in: "/skill go-expert fix the bug", wantName: "go-expert", wantArgs: "fix the bug", wantOK: true},
		{in: "  /skill   go-expert   a  b  ", wantName: "go-expert", wantArgs: "a  b", wantOK: true},
		{in: "/skill\tgo-expert\targs here", wantName: "go-expert", wantArgs: "args here", wantOK: true},
	}
	for _, tc := range cases {
		name, args, ok := parseUserSkillCommand(tc.in)
		if ok != tc.wantOK || name != tc.wantName || args != tc.wantArgs {
			t.Fatalf("parseUserSkillCommand(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, name, args, ok, tc.wantName, tc.wantArgs, tc.wantOK)
		}
	}
}

func TestResolveUserSkillInvocationDecisionTable(t *testing.T) {
	states := skill.InvocationStates([]*skill.Meta{
		{Name: "model-skill"},
		{Name: "manual-skill", DisableModelInvocation: true},
		{Name: "denied-skill"},
	}, permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameSkill, Pattern: "denied-skill", Action: permission.ActionDeny},
	}, nil)

	cases := []struct {
		name    string
		wantErr bool
		// wantReason is the machine-readable state reason on refusal.
		wantReason string
	}{
		{name: "model-skill"},
		{name: "manual-skill"},
		{name: "denied-skill", wantErr: true, wantReason: skill.ReasonDeniedByRuleset},
		{name: "missing-skill", wantErr: true, wantReason: skill.ReasonNotFound},
		{name: "", wantErr: true},
	}
	for _, tc := range cases {
		state, err := resolveUserSkillInvocation(states, tc.name)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("resolveUserSkillInvocation(%q) = nil error, want refusal", tc.name)
			}
			if tc.wantReason != "" && state.Reason != tc.wantReason {
				t.Fatalf("refusal for %q reason = %q, want %q", tc.name, state.Reason, tc.wantReason)
			}
			continue
		}
		if err != nil {
			t.Fatalf("resolveUserSkillInvocation(%q) error = %v", tc.name, err)
		}
		if !state.UserLoadable || state.Meta.Name != tc.name {
			t.Fatalf("resolved state for %q = %+v, want loadable %q", tc.name, state, tc.name)
		}
	}
}

// TestBuildUserSkillInvocationPairMatchesModelShape pins the synthesized pair
// to the model path's helpers: the same argument encoding and result
// formatter, so restore, compaction and the TUI cannot tell the two apart
// except through the provenance origin that marks the explicit request.
func TestBuildUserSkillInvocationPairMatchesModelShape(t *testing.T) {
	root := t.TempDir()
	meta := writeTempSkill(t, root, "go-expert", "Arg is: ${CHORD_SKILL_ARGS}", false)
	sk, err := skill.LoadSkill(meta.Location)
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}

	assistant, toolMsg, err := buildUserSkillInvocationPair(sk, "run the tests", "user-skill-1")
	if err != nil {
		t.Fatalf("buildUserSkillInvocationPair: %v", err)
	}

	if assistant.Role != message.RoleAssistant || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant message = %+v, want one tool call", assistant)
	}
	call := assistant.ToolCalls[0]
	if call.ID != "user-skill-1" || call.Name != tools.NameSkill {
		t.Fatalf("tool call = %+v, want user-skill-1/%s", call, tools.NameSkill)
	}
	wantArgs, err := tools.SkillCallArguments("go-expert", "run the tests")
	if err != nil {
		t.Fatalf("SkillCallArguments: %v", err)
	}
	if string(call.Args) != string(wantArgs) {
		t.Fatalf("tool call args = %s, want %s", call.Args, wantArgs)
	}

	if toolMsg.Role != message.RoleTool || toolMsg.ToolCallID != "user-skill-1" {
		t.Fatalf("tool message = %+v, want paired tool result", toolMsg)
	}
	if toolMsg.ToolStatus != message.ToolStatusSuccess {
		t.Fatalf("tool status = %q, want success", toolMsg.ToolStatus)
	}
	if want := tools.FormatSkillInvocationResult(sk, "run the tests"); toolMsg.Content != want {
		t.Fatalf("tool content = %q, want the shared formatter output %q", toolMsg.Content, want)
	}
	if !strings.Contains(toolMsg.Content, "Arg is: run the tests") {
		t.Fatalf("tool content should substitute ${CHORD_SKILL_ARGS}:\n%s", toolMsg.Content)
	}

	for _, m := range []message.Message{assistant, toolMsg} {
		if m.Provenance == nil || m.Provenance.Origin != message.OriginUser {
			t.Fatalf("message %+v should carry origin %q", m, message.OriginUser)
		}
	}
}

func TestRestoreInvokedSkillsFromUserSkillPair(t *testing.T) {
	root := t.TempDir()
	meta := writeTempSkill(t, root, "manual-skill", "Manual workflow.", true)
	sk, err := skill.LoadSkill(meta.Location)
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	assistant, toolMsg, err := buildUserSkillInvocationPair(sk, "", "user-skill-9")
	if err != nil {
		t.Fatalf("buildUserSkillInvocationPair: %v", err)
	}

	got := rebuildInvokedSkillsFromMessages([]message.Message{assistant, toolMsg}, []*skill.Meta{meta})
	if len(got) != 1 || got[0].Name != "manual-skill" || !got[0].Invoked {
		t.Fatalf("restored invoked skills = %+v, want manual-skill", got)
	}
}

func TestMainAgentAppendUserSkillInvocation(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{writeTempSkill(t, projectRoot, "go-expert", "Follow the workflow.", false)})

	pair := a.appendUserSkillInvocation(message.Message{Role: message.RoleUser, Content: "/skill go-expert run the tests"})
	if len(pair) != 2 {
		t.Fatalf("appended pair = %+v, want assistant+tool", pair)
	}
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 2 || msgs[0].Role != message.RoleAssistant || msgs[1].Role != message.RoleTool {
		t.Fatalf("context messages = %+v, want one assistant/tool pair", msgs)
	}
	if !strings.Contains(msgs[1].Content, "run the tests") {
		t.Fatalf("tool result should carry the request args:\n%s", msgs[1].Content)
	}
	if got := a.InvokedSkills(); len(got) != 1 || got[0].Name != "go-expert" {
		t.Fatalf("InvokedSkills() = %+v, want go-expert", got)
	}
}

// TestMainAgentAppendUserSkillInvocationManualOnly covers the two faces: a
// manual-only skill never enters the model catalog, yet the user's explicit
// request still appends the same pair.
func TestMainAgentAppendUserSkillInvocationManualOnly(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{writeTempSkill(t, projectRoot, "manual-skill", "Manual workflow.", true)})

	if got := a.ListSkills(); len(got) != 0 {
		t.Fatalf("ListSkills() = %+v, want a manual-only skill hidden from the model", got)
	}
	if _, err := a.LoadSkill("manual-skill"); err == nil {
		t.Fatal("the model path must refuse a manual-only skill")
	}

	pair := a.appendUserSkillInvocation(message.Message{Role: message.RoleUser, Content: "/skill manual-skill"})
	if len(pair) != 2 {
		t.Fatalf("appended pair = %+v, want assistant+tool", pair)
	}
	if got := a.InvokedSkills(); len(got) != 1 || got[0].Name != "manual-skill" {
		t.Fatalf("InvokedSkills() = %+v, want manual-skill", got)
	}
}

// TestMainAgentLoadSkillRefusesHiddenDeniedAndMissingIdentically pins the
// model-facing decision table: a manual-only skill, a ruleset-denied skill and
// an unknown name all fail with the same wording, so the model cannot probe
// whether a skill it may not load exists.
func TestMainAgentLoadSkillRefusesHiddenDeniedAndMissingIdentically(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{
		writeTempSkill(t, projectRoot, "manual-skill", "Manual workflow.", true),
		writeTempSkill(t, projectRoot, "denied-skill", "Denied workflow.", false),
	})
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameSkill, Pattern: "denied-skill", Action: permission.ActionDeny},
	}

	refusals := make([]string, 0, 3)
	for _, name := range []string{"manual-skill", "denied-skill", "missing-skill"} {
		_, err := a.LoadSkill(name)
		if err == nil {
			t.Fatalf("LoadSkill(%q) = nil error, want refusal", name)
		}
		// The refusal names the requested skill, so normalize the name away to
		// compare wording rather than identity.
		refusals = append(refusals, strings.Replace(err.Error(), name, "<name>", 1))
	}
	if refusals[0] != refusals[1] || refusals[1] != refusals[2] {
		t.Fatalf("refusals should share one wording, got %q", refusals)
	}
	if !strings.Contains(refusals[0], "not found") {
		t.Fatalf("refusal = %q, want not-found wording", refusals[0])
	}
}

func TestMainAgentAppendUserSkillInvocationRefusalsAppendNothing(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{writeTempSkill(t, projectRoot, "go-expert", "Follow the workflow.", false)})

	for _, content := range []string{"/skill missing-skill", "/skill", "/skill go-expert"} {
		before := len(a.ctxMgr.Snapshot())
		pair := a.appendUserSkillInvocation(message.Message{Role: message.RoleUser, Content: content})
		if content == "/skill go-expert" {
			if len(pair) != 2 {
				t.Fatalf("%q should load, got %+v", content, pair)
			}
			continue
		}
		if pair != nil {
			t.Fatalf("appendUserSkillInvocation(%q) = %+v, want no pair", content, pair)
		}
		if got := len(a.ctxMgr.Snapshot()); got != before {
			t.Fatalf("appendUserSkillInvocation(%q) changed context length %d -> %d", content, before, got)
		}
	}
}

// A catalog with nothing model-invocable removes the tool outright, so the
// refusal wording never becomes a name-probing oracle: the model has no skill
// tool to call at all. The tool's own IsAvailable() reads the model face, so
// this is a property of the live surface, not of the tool.
func TestVisibleLLMToolsDropsSkillWhenCatalogIsManualOnly(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	rs := permissionRuleset(t, "\"*\": allow\n")

	a.SetSkills([]*skill.Meta{writeTempSkill(t, projectRoot, "manual-skill", "Manual workflow.", true)})
	reg := tools.NewRegistry()
	reg.Register(tools.NewSkillTool(a))
	if visible := visibleLLMTools(reg, rs, func(string) bool { return false }, toolPermissionContext{}); containsToolNamed(visible, tools.NameSkill) {
		t.Fatal("a manual-only catalog must not expose the skill tool")
	}

	a.SetSkills([]*skill.Meta{
		writeTempSkill(t, projectRoot, "manual-skill", "Manual workflow.", true),
		writeTempSkill(t, projectRoot, "go-expert", "Follow the workflow.", false),
	})
	reg = tools.NewRegistry()
	reg.Register(tools.NewSkillTool(a))
	if visible := visibleLLMTools(reg, rs, func(string) bool { return false }, toolPermissionContext{}); !containsToolNamed(visible, tools.NameSkill) {
		t.Fatal("one model-invocable skill must expose the skill tool")
	}
}

// A second load of the same skill is a fresh body substituted with the new
// args rather than a dedup hit: only the draft-level retry is idempotent.
// Invoked state stays one entry, overwritten by name.
func TestMainAgentRepeatSkillInvocationSubstitutesPerCallArgs(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{writeTempSkill(t, projectRoot, "go-expert", "Args were ${CHORD_SKILL_ARGS}.", false)})

	first := a.appendUserSkillInvocation(message.Message{Role: message.RoleUser, Content: "/skill go-expert first"})
	second := a.appendUserSkillInvocation(message.Message{Role: message.RoleUser, Content: "/skill go-expert second"})
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("each load should append a pair, got %d and %d messages", len(first), len(second))
	}
	if !strings.Contains(first[1].Content, "<args>first</args>") || !strings.Contains(first[1].Content, "Args were first.") {
		t.Fatalf("first body should carry its own args:\n%s", first[1].Content)
	}
	if !strings.Contains(second[1].Content, "<args>second</args>") || !strings.Contains(second[1].Content, "Args were second.") {
		t.Fatalf("second body should carry its own args:\n%s", second[1].Content)
	}
	if first[0].ToolCalls[0].ID == second[0].ToolCalls[0].ID {
		t.Fatalf("each load needs its own tool call id, both were %q", first[0].ToolCalls[0].ID)
	}
	if got := a.InvokedSkills(); len(got) != 1 || got[0].Name != "go-expert" {
		t.Fatalf("InvokedSkills() = %+v, want one go-expert entry", got)
	}
	if got := len(a.ctxMgr.Snapshot()); got != 4 {
		t.Fatalf("context length = %d, want two pairs", got)
	}
}

func TestSubAgentAppendUserSkillInvocationIsIsolated(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	meta := writeTempSkill(t, root, "manual-skill", "Manual workflow.", true)
	parent.SetSkills([]*skill.Meta{meta})
	parent.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {
			Name:       "worker",
			Mode:       config.AgentModeSubAgent,
			Permission: parsePermissionNode(t, "skill:\n  '*': allow\n"),
		},
	})

	sub.appendUserSkillInvocation(message.Message{Role: message.RoleUser, Content: "/skill manual-skill"})

	msgs := sub.ctxMgr.Snapshot()
	if len(msgs) != 2 || msgs[0].Role != message.RoleAssistant || msgs[1].Role != message.RoleTool {
		t.Fatalf("subagent context = %+v, want one assistant/tool pair", msgs)
	}
	if got := sub.InvokedSkills(); len(got) != 1 || got[0].Name != "manual-skill" {
		t.Fatalf("subagent InvokedSkills() = %+v, want manual-skill", got)
	}
	if got := parent.InvokedSkills(); len(got) != 0 {
		t.Fatalf("main agent should stay untouched by a subagent load: %+v", got)
	}
}

// TestRecordCommittedUserMessageInjectsSkillPairOnce pins the turn-start hook:
// the committed line stays the literal command the model sees, and the context
// gains exactly one pair, so a message cannot collect a second body by riding
// both this hook and the queued-input merge.
func TestRecordCommittedUserMessageInjectsSkillPairOnce(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{writeTempSkill(t, projectRoot, "go-expert", "Workflow for ${CHORD_SKILL_ARGS}.", false)})

	const line = "/skill go-expert run the tests"
	if got, _ := a.expandSlashCommandForModel(line, nil); got != line {
		t.Fatalf("model-facing content = %q, want the literal command", got)
	}
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: line})

	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 3 {
		t.Fatalf("context = %+v, want the user line and exactly one pair", msgs)
	}
	if msgs[0].Role != message.RoleUser || msgs[0].Content != line {
		t.Fatalf("committed user message = %+v, want the literal command", msgs[0])
	}
	if msgs[1].Role != message.RoleAssistant || msgs[2].Role != message.RoleTool {
		t.Fatalf("pair roles = %q/%q, want assistant/tool", msgs[1].Role, msgs[2].Role)
	}
	if !strings.Contains(msgs[2].Content, "Workflow for run the tests.") {
		t.Fatalf("tool result should carry the body with the request args:\n%s", msgs[2].Content)
	}
	if got := a.InvokedSkills(); len(got) != 1 || got[0].Name != "go-expert" {
		t.Fatalf("InvokedSkills() = %+v, want go-expert", got)
	}
}

// TestConsumePendingUserMessagesInjectsSkillPairOnce pins the in-turn merge
// hook: a queued /skill line injects its pair once, into both the durable
// context and the request surface, and a retry that re-enters the boundary
// reuses that pair instead of appending a second copy.
func TestConsumePendingUserMessagesInjectsSkillPairOnce(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{writeTempSkill(t, projectRoot, "go-expert", "Workflow for ${CHORD_SKILL_ARGS}.", false)})
	const line = "/skill go-expert run the tests"
	a.pendingUserMessages = []pendingUserMessage{{DraftID: "draft-1", Content: line, FromUser: true}}

	request := a.consumePendingUserMessagesForRequest(nil, 0)
	if len(request) != 3 {
		t.Fatalf("request = %+v, want the user line and exactly one pair", request)
	}
	if request[0].Role != message.RoleUser || request[1].Role != message.RoleAssistant || request[2].Role != message.RoleTool {
		t.Fatalf("request order = %q/%q/%q, want user/assistant/tool", request[0].Role, request[1].Role, request[2].Role)
	}
	if request[0].Content != line {
		t.Fatalf("request user content = %q, want the literal command", request[0].Content)
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("pending queue = %d entries, want drained", got)
	}
	if got := len(a.ctxMgr.Snapshot()); got != 3 {
		t.Fatalf("context = %d messages, want exactly one pair", got)
	}

	again := a.consumePendingUserMessagesForRequest(request, 0)
	if len(again) != 3 || len(a.ctxMgr.Snapshot()) != 3 {
		t.Fatalf("retry rebuilt %d messages with context %d, want the same single pair",
			len(again), len(a.ctxMgr.Snapshot()))
	}
}

// TestQueuedSkillDraftRetryInjectsOnePair pins the draft-level idempotency the
// load contract leans on: re-submitting the same draft (retry or duplicate
// upsert) leaves one queue entry and therefore one body.
func TestQueuedSkillDraftRetryInjectsOnePair(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{writeTempSkill(t, projectRoot, "go-expert", "Workflow for ${CHORD_SKILL_ARGS}.", false)})

	submitted := pendingUserMessage{DraftID: "draft-7", Content: "/skill go-expert run the tests", FromUser: true}
	a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, submitted)
	a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, submitted)
	if got := len(a.pendingUserMessages); got != 1 {
		t.Fatalf("queue = %d entries, want the repeated draft upserted in place", got)
	}

	request := a.consumePendingUserMessagesForRequest(nil, 0)
	if len(request) != 3 {
		t.Fatalf("request = %+v, want user/assistant/tool", request)
	}
	if got := len(a.ctxMgr.Snapshot()); got != 3 {
		t.Fatalf("context = %d messages, want one pair for one draft", got)
	}
}

// TestFailedSkillLineStaysWithoutPair pins the failure policy: the user's line
// is committed as typed, no pair is appended, and the refusal reaches the user
// instead of being silently dropped.
func TestFailedSkillLineStaysWithoutPair(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{writeTempSkill(t, projectRoot, "go-expert", "Workflow.", false)})

	const line = "/skill missing-skill run it"
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: line})

	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 1 {
		t.Fatalf("context = %+v, want only the committed user line", msgs)
	}
	if msgs[0].Role != message.RoleUser || msgs[0].Content != line {
		t.Fatalf("committed message = %+v, want the original line", msgs[0])
	}
	if got := a.InvokedSkills(); len(got) != 0 {
		t.Fatalf("InvokedSkills() = %+v, want none after a failed load", got)
	}
	if toast := waitForToastEvent(t, a.Events(), "missing-skill"); toast.Level != "warn" {
		t.Fatalf("refusal toast level = %q, want warn", toast.Level)
	}
}
