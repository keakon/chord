package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

func skillInvocationMessages(callID, skillName string) []message.Message {
	return []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameSkill,
			Args: []byte(`{"name":"` + skillName + `"}`),
		}}},
		{
			Role:       message.RoleTool,
			ToolCallID: callID,
			Content:    "<skill>\n<name>" + skillName + "</name>\n\nfull instructions\n</skill>",
			ToolStatus: string(ToolResultStatusSuccess),
		},
	}
}

func checkpointMessageWithSkills(names []string, omitted int) message.Message {
	summary := ensureCheckpointSkillsSection("## Next Step\n- keep going", names, omitted)
	return message.Message{
		Role:                message.RoleUser,
		IsCompactionSummary: true,
		Content: buildCompactionCheckpointMessage(
			summary,
			[]string{"history-1.md"},
			message.CompactionSummaryModeTruncateOnly,
			nil,
		),
	}
}

// A skill's instructions live only in its tool result, so archiving the head
// must leave the names behind — otherwise the continuation cannot tell a
// workflow was ever in effect, let alone re-load it.
func TestCheckpointSkillsSectionRecordsArchivedInvocations(t *testing.T) {
	head := []message.Message{{Role: message.RoleUser, Content: "review the diff"}}
	head = append(head, skillInvocationMessages("s1", "code-review")...)
	head = append(head, skillInvocationMessages("s2", "dataviz")...)

	names, omitted := collectCheckpointSkillNames(head)
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0", omitted)
	}
	if strings.Join(names, ",") != "code-review,dataviz" {
		t.Fatalf("names = %#v, want [code-review dataviz]", names)
	}

	section := ensureCheckpointSkillsSection("## Progress\n- read the diff", names, omitted)
	for _, want := range []string{
		"## Progress",
		checkpointSkillsHeading,
		"- code-review",
		"- dataviz",
		"no longer in context",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("section missing %q:\n%s", want, section)
		}
	}
}

// A failed skill call never loaded any instructions, so it must not be
// recorded as one that did.
func TestCheckpointSkillsSectionIgnoresFailedInvocation(t *testing.T) {
	head := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{
			ID:   "s1",
			Name: tools.NameSkill,
			Args: []byte(`{"name":"missing-skill"}`),
		}}},
		{
			Role:       message.RoleTool,
			ToolCallID: "s1",
			Content:    `skill "missing-skill" not found`,
			ToolStatus: string(ToolResultStatusError),
		},
	}
	if names, _ := collectCheckpointSkillNames(head); len(names) != 0 {
		t.Fatalf("names = %#v, want none", names)
	}
	if section := ensureCheckpointSkillsSection("## Progress\n- nothing", nil, 0); strings.Contains(section, checkpointSkillsHeading) {
		t.Fatalf("empty skill set should render no section:\n%s", section)
	}
}

// Compaction is recursive: the second run archives the checkpoint the first
// one wrote, and by then the original skill tool calls are gone. The names
// have to be merged out of the prior checkpoint or they decay one compaction
// at a time. The omission count travels the same way, so an overflow stays
// visible instead of being silently forgotten.
func TestCheckpointSkillsSectionSurvivesRecursiveCompaction(t *testing.T) {
	head := []message.Message{checkpointMessageWithSkills([]string{"code-review"}, 2)}
	head = append(head, message.Message{Role: message.RoleUser, Content: "now chart it"})
	head = append(head, skillInvocationMessages("s9", "dataviz")...)

	names, omitted := collectCheckpointSkillNames(head)
	if strings.Join(names, ",") != "code-review,dataviz" {
		t.Fatalf("names = %#v, want [code-review dataviz]", names)
	}
	if omitted != 2 {
		t.Fatalf("omitted = %d, want the carried 2", omitted)
	}
}

// The section is carried by name merge, so the prior-checkpoint carry must not
// bring a second copy of it along: two lists of the same skills, one of them
// stale, is worse guidance than one.
func TestCheckpointSkillsSectionNotDuplicatedByPriorCheckpointCarry(t *testing.T) {
	prior := checkpointMessageWithSkills([]string{"code-review"}, 0)
	carry := latestPriorCheckpointBody([]message.Message{prior})
	if carry == "" {
		t.Fatal("prior checkpoint body should still carry its other sections")
	}
	if strings.Contains(carry, checkpointSkillsHeading) {
		t.Fatalf("carry must not duplicate the skills section:\n%s", carry)
	}

	names, omitted := collectCheckpointSkillNames([]message.Message{prior})
	summary := ensureCheckpointSkillsSection("## Progress\n- continued", names, omitted)
	summary = appendPriorCheckpointCarry(summary, carry)
	if got := strings.Count(summary, checkpointSkillsHeading); got != 1 {
		t.Fatalf("skills section appears %d times, want exactly 1:\n%s", got, summary)
	}
}

// The record is a runtime fact: whatever a summarizer wrote under the same
// heading is replaced, not merged, so a model cannot drop a name or invent one.
func TestEnsureCheckpointSkillsSectionReplacesSummarizerCopy(t *testing.T) {
	summarizerWrote := "## Progress\n- worked\n\n" + checkpointSkillsHeading +
		"\nSome prose the model wrote.\n- invented-skill\n\n## Next Step\n- go on"

	out := ensureCheckpointSkillsSection(summarizerWrote, []string{"code-review"}, 0)
	if strings.Contains(out, "invented-skill") {
		t.Errorf("summarizer-authored skill list must not survive:\n%s", out)
	}
	if got := strings.Count(out, checkpointSkillsHeading); got != 1 {
		t.Errorf("skills section appears %d times, want exactly 1:\n%s", got, out)
	}
	for _, want := range []string{"## Progress", "## Next Step", "- code-review"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCheckpointSkillsSectionCapsAndReportsOverflow(t *testing.T) {
	var head []message.Message
	total := checkpointMaxSkillNames + 3
	for i := range total {
		head = append(head, skillInvocationMessages(
			"call-"+string(rune('a'+i)),
			"skill-"+string(rune('a'+i)),
		)...)
	}
	names, omitted := collectCheckpointSkillNames(head)
	if len(names) != checkpointMaxSkillNames {
		t.Fatalf("kept %d names, want the cap %d", len(names), checkpointMaxSkillNames)
	}
	if omitted != total-checkpointMaxSkillNames {
		t.Fatalf("omitted = %d, want %d", omitted, total-checkpointMaxSkillNames)
	}
	section := renderCheckpointSkillsSection(names, omitted)
	if !strings.Contains(section, "3 more omitted") {
		t.Fatalf("section should report the overflow:\n%s", section)
	}
	// The note must parse back, so the overflow survives the next compaction.
	parsedNames, parsedOmitted := parseCheckpointSkillNames(
		buildCompactionCheckpointMessage(section, []string{"history-1.md"}, message.CompactionSummaryModeTruncateOnly, nil),
	)
	if len(parsedNames) != checkpointMaxSkillNames || parsedOmitted != 3 {
		t.Fatalf("parsed %d names / omitted %d, want %d / 3", len(parsedNames), parsedOmitted, checkpointMaxSkillNames)
	}
}

// The sidebar's invoked state must mean "this skill's instructions are in
// context", so a durable compaction that archives the tool result has to clear
// it — and a skill loaded in the preserved tail has to stay. The checkpoint's
// own name list must not resurrect the flag: it records names, not
// instructions.
func TestApplyCompactionDraftResetsInvokedSkills(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetSkills([]*skill.Meta{
		{Name: "code-review", Description: "review code", Location: filepath.Join(projectRoot, "code-review", "SKILL.md")},
		{Name: "dataviz", Description: "charts", Location: filepath.Join(projectRoot, "dataviz", "SKILL.md")},
	})

	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "review then chart"})
	for _, msg := range skillInvocationMessages("s1", "code-review") {
		a.ctxMgr.Append(msg)
	}
	headSplit := len(a.ctxMgr.Snapshot())
	for _, msg := range skillInvocationMessages("s2", "dataviz") {
		a.ctxMgr.Append(msg)
	}
	a.MarkSkillInvokedByName("code-review")
	a.MarkSkillInvokedByName("dataviz")
	if got := len(a.InvokedSkills()); got != 2 {
		t.Fatalf("invoked skills before compaction = %d, want 2", got)
	}

	checkpoint := checkpointMessageWithSkills([]string{"code-review"}, 0)
	draft := &compactionDraft{
		NewMessages:    []message.Message{checkpoint},
		HeadSplit:      headSplit,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    message.CompactionSummaryModeTruncateOnly,
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
		Manual:         true,
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}

	invoked := a.InvokedSkills()
	if len(invoked) != 1 || invoked[0].Name != "dataviz" {
		var got []string
		for _, meta := range invoked {
			got = append(got, meta.Name)
		}
		t.Fatalf("invoked skills after compaction = %#v, want only [dataviz]", got)
	}
	// The archived skill is still discoverable, just no longer loaded: the
	// listing the model routes on must not shrink.
	if len(a.ListSkills()) != 2 {
		t.Fatalf("available skills = %d, want both still listed", len(a.ListSkills()))
	}
}

// A subagent's emergency compression drops history the same way, so it owes
// the same two things: the names in its checkpoint and an honest state
// afterwards.
func TestSubAgentCheckpointRecordsLoadedSkills(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	defer sub.cancel()

	if got := subAgentCheckpointSkills(sub, nil); got != "none" {
		t.Fatalf("skills line with nothing loaded = %q, want \"none\"", got)
	}
	if names, omitted := parseSubAgentCheckpointSkillNames(buildSubAgentStructuredCheckpoint(sub, nil, 7, "proactive", "archives/sub-0.md")); len(names) != 0 || omitted != 0 {
		t.Fatalf("parsing a skill-free checkpoint = %v / %d, want none", names, omitted)
	}

	sub.MarkSkillInvoked(&skill.Meta{Name: "code-review"})
	line := subAgentCheckpointSkills(sub, nil)
	if !strings.Contains(line, "code-review") || !strings.Contains(line, "call `skill` again") {
		t.Fatalf("skills line = %q, want the name plus the re-load hint", line)
	}
	checkpoint := buildSubAgentStructuredCheckpoint(sub, nil, 7, "proactive", "archives/sub-1.md")
	if !strings.Contains(checkpoint, "- Skills loaded earlier: code-review") {
		t.Fatalf("checkpoint missing the skills line:\n%s", checkpoint)
	}
}

// Chained compressions must not erode the recorded names. The invoked state
// is recomputed from the survivors after every compression, so the second
// checkpoint can only keep a skill whose result the first one archived by
// merging the previous checkpoint's line — and the previous checkpoint itself
// always falls in the dropped prefix.
func TestSubAgentCheckpointSkillsSurviveChainedCompression(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	defer sub.cancel()

	sub.MarkSkillInvoked(&skill.Meta{Name: "alpha"})
	msgs := []message.Message{{Role: message.RoleUser, Content: "start"}}
	msgs = append(msgs, skillInvocationMessages("s1", "alpha")...)
	msgs = append(msgs, subAgentCheckpointFiller(20)...)
	first, ok := sub.compactContextForTarget(msgs, estimateMessagesTokens(sub.ctxMgr, msgs)/2, "proactive")
	if !ok {
		t.Fatal("first compression did not run")
	}
	if checkpoint := first[1].Content; !strings.Contains(checkpoint, "alpha") {
		t.Fatalf("first checkpoint should record alpha:\n%s", checkpoint)
	}

	sub.MarkSkillInvoked(&skill.Meta{Name: "dataviz"})
	msgs = append([]message.Message{}, sub.ctxMgr.Snapshot()...)
	msgs = append(msgs, skillInvocationMessages("s2", "dataviz")...)
	msgs = append(msgs, subAgentCheckpointFiller(20)...)
	second, ok := sub.compactContextForTarget(msgs, estimateMessagesTokens(sub.ctxMgr, msgs)/2, "proactive")
	if !ok {
		t.Fatal("second compression did not run")
	}
	// The regression is only meaningful if the first checkpoint really was
	// dropped; otherwise its text would satisfy the name assertions below.
	if got := countSubAgentCheckpoints(second); got != 1 {
		t.Fatalf("second compression kept %d checkpoints, want 1", got)
	}
	checkpoint := second[1].Content
	for _, want := range []string{"alpha", "dataviz"} {
		if !strings.Contains(checkpoint, want) {
			t.Fatalf("chained checkpoint lost %q:\n%s", want, checkpoint)
		}
	}
}

func subAgentCheckpointFiller(n int) []message.Message {
	out := make([]message.Message, 0, n)
	for range n {
		out = append(out, message.Message{Role: message.RoleUser, Content: strings.Repeat("filler ", 200)})
	}
	return out
}

func countSubAgentCheckpoints(messages []message.Message) int {
	count := 0
	for _, msg := range messages {
		if strings.Contains(msg.Content, subAgentCheckpointSkillsPrefix) {
			count++
		}
	}
	return count
}

// The omission count has to parse back so an overflow survives the next
// compression instead of being forgotten one checkpoint at a time.
func TestSubAgentCheckpointSkillsCarryOverflow(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	defer sub.cancel()

	total := checkpointMaxSkillNames + 3
	for i := range total {
		sub.MarkSkillInvoked(&skill.Meta{Name: fmt.Sprintf("skill-%02d", i)})
	}
	checkpoint := buildSubAgentStructuredCheckpoint(sub, nil, 9, "proactive", "archives/sub-1.md")
	names, omitted := parseSubAgentCheckpointSkillNames(checkpoint)
	if len(names) != checkpointMaxSkillNames || omitted != 3 {
		t.Fatalf("parsed %d names / omitted %d, want %d / 3", len(names), omitted, checkpointMaxSkillNames)
	}
}

func TestSubAgentCheckpointSkillsDeduplicatesCarriedOverflow(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	defer sub.cancel()

	total := checkpointMaxSkillNames + 1
	for i := range total {
		sub.MarkSkillInvoked(&skill.Meta{Name: fmt.Sprintf("skill-%02d", i)})
	}
	first := buildSubAgentStructuredCheckpoint(sub, nil, 9, "proactive", "archives/sub-1.md")
	names, omitted := parseSubAgentCheckpointSkillNames(first)
	if len(names) != checkpointMaxSkillNames || omitted != 1 {
		t.Fatalf("first checkpoint parsed %d names / omitted %d, want %d / 1", len(names), omitted, checkpointMaxSkillNames)
	}

	// The overflowed name becomes visible again before the next checkpoint.
	sub.MarkSkillInvoked(&skill.Meta{Name: fmt.Sprintf("skill-%02d", total-1)})
	names, omitted = collectSubAgentCheckpointSkillNames(sub, []message.Message{
		{
			Role:                message.RoleUser,
			IsCompactionSummary: true,
			Content:             first,
		},
	})
	if len(names) != checkpointMaxSkillNames || omitted != 1 {
		t.Fatalf("recomputed %d names / omitted %d, want %d / 1", len(names), omitted, checkpointMaxSkillNames)
	}
}

func TestSubAgentRestoreInvokedSkillsFollowsRemainingMessages(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	defer sub.cancel()

	sub.MarkSkillInvoked(&skill.Meta{Name: "code-review"})
	sub.MarkSkillInvoked(&skill.Meta{Name: "dataviz"})

	remaining := []message.Message{{Role: message.RoleUser, Content: "[system] SubAgent context checkpoint: 12 earlier messages were removed for proactive."}}
	remaining = append(remaining, skillInvocationMessages("s2", "dataviz")...)
	sub.restoreInvokedSkills(remaining)

	if got := sub.invokedSkillNamesSnapshot(); strings.Join(got, ",") != "dataviz" {
		t.Fatalf("invoked skills after compression = %#v, want only [dataviz]", got)
	}
}
