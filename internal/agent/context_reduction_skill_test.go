package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func skillEnvelopeContent(root, body string) string {
	return "<skill>\n" +
		"<name>simplify-codebase</name>\n" +
		"<path>" + root + "/SKILL.md</path>\n" +
		"<root>" + root + "</root>\n" +
		"<relative_paths_base>" + root + "</relative_paths_base>\n" +
		"<notes>Relative paths from the skill content resolve against &lt;root&gt;.</notes>\n\n" +
		body + "\n</skill>"
}

func skillTestBody() string {
	var b strings.Builder
	b.WriteString("# Simplify Codebase\n\n")
	b.WriteString(strings.Repeat("Reduce accidental complexity in the inspected surface.\n", 40))
	b.WriteString("\n## Select mode and scope\n\nPick the authority mode first.\n")
	b.WriteString("\n## Decide and act\n\nSelect the strongest authorized cut.\n")
	return b.String()
}

// A loaded skill body is instruction state: once it ages out, the summary has
// to keep the envelope anchors the workflow's relative paths resolve against
// (the name alone does not name a copy — several load roots can define it), and
// the outline has to keep the workflow's shape.
func TestSkillResultSummarizesAsInstructionsKeepingAnchors(t *testing.T) {
	policy := defaultContextReductionPolicy()
	root := t.TempDir()
	content := skillEnvelopeContent(root, skillTestBody())
	ctx := requestReductionContext{
		ToolName:    tools.NameSkill,
		Content:     content,
		ToolStatus:  string(ToolResultStatusSuccess),
		Age:         policy.StaleAgeTurns,
		ToolResults: policy.MinToolResultsPrune,
		Policy:      policy,
	}
	verdict := classifyRequestReduction(ctx)
	if verdict.Class != requestReductionSkill {
		t.Fatalf("skill result classified as %q, want %q", verdict.Class, requestReductionSkill)
	}
	reduced, rule, ok := reduceRequestToolOutput(verdict.Class, ctx)
	if !ok || rule != string(requestReductionSkill) {
		t.Fatalf("reduction = (%q, %q, %v), want rule %q", reduced, rule, ok, requestReductionSkill)
	}
	for _, want := range []string{
		"<name>simplify-codebase</name>",
		"<path>" + root + "/SKILL.md</path>",
		"<root>" + root + "</root>",
		"<relative_paths_base>" + root + "</relative_paths_base>",
		"- Decide and act",
	} {
		if !strings.Contains(reduced, want) {
			t.Fatalf("skill summary lost %q: %q", want, reduced)
		}
	}
	if len(reduced) >= len(content) {
		t.Fatalf("skill summary bytes = %d, want less than original %d", len(reduced), len(content))
	}
}

// A skill body that happens to look like search hits or source listings must
// not take a content-shape summary: those drop the anchors, and the body is
// still the workflow the model is executing.
func TestSkillResultSkipsContentShapeReduction(t *testing.T) {
	policy := defaultContextReductionPolicy()
	root := t.TempDir()
	var body strings.Builder
	body.WriteString("# Simplify Codebase\n\n")
	for i := range 200 {
		fmt.Fprintf(&body, "internal/agent/file%d.go:12: match line\n", i)
	}
	content := skillEnvelopeContent(root, body.String())
	if !looksLikeSearchResultContent(content) {
		t.Fatal("test fixture must look like a search result for non-skill content")
	}
	ctx := requestReductionContext{
		ToolName:    tools.NameSkill,
		Content:     content,
		ToolStatus:  string(ToolResultStatusSuccess),
		Age:         policy.ReadLikeAgeTurns,
		ToolResults: policy.MinToolResultsPrune + 1,
		Policy:      policy,
	}
	if verdict := classifyRequestReduction(ctx); verdict.Class != requestReductionNone {
		t.Fatalf("skill result reduced at the read-like gate as %q", verdict.Class)
	}
	ctx.Age = policy.StaleAgeTurns
	if verdict := classifyRequestReduction(ctx); verdict.Class != requestReductionSkill {
		t.Fatalf("skill result classified as %q at the stale gate, want %q", verdict.Class, requestReductionSkill)
	}
}

// The summary drops the instructions, so the archive address has to stay: the
// envelope text is not rebuildable by re-running anything else.
func TestSkillInstructionSummaryKeepsFullTextRecoverable(t *testing.T) {
	archiveDir := t.TempDir()
	content := skillEnvelopeContent(t.TempDir(), skillTestBody())
	ctx := reductionContextForArchive(t, tools.NameSkill, content, archiveDir)
	reduced, rule, ok := reduceRequestToolOutput(requestReductionSkill, ctx)
	if !ok || rule != string(requestReductionSkill) {
		t.Fatalf("reduction = (%q, %q, %v), want rule %q", reduced, rule, ok, requestReductionSkill)
	}
	refs := tools.ExtractArtifactReferences(reduced)
	if len(refs) == 0 {
		t.Fatalf("skill summary dropped the only copy without an address: %q", reduced)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(refs[0], tools.ArtifactReferencePrefix), ".")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("archived skill text unreadable: %v", err)
	}
	if string(data) != content {
		t.Fatalf("archived skill text differs: got %d bytes want %d", len(data), len(content))
	}
}

// A body small enough that the anchors alone rival it stays complete instead of
// trading the instructions for a same-size marker.
func TestSkillInstructionSummaryRejectedWhenNotSmaller(t *testing.T) {
	// An envelope that is little more than its own anchors: the summary has to
	// restate them plus a marker line, so it cannot come out shorter.
	content := "<skill>\n<name>tiny</name>\n<path>/s/SKILL.md</path>\n<root>/s</root>\n" +
		"<relative_paths_base>/s</relative_paths_base>\n</skill>"
	ctx := requestReductionContext{ToolName: tools.NameSkill, Content: content}
	if reduced, rule, ok := reduceRequestToolOutput(requestReductionSkill, ctx); ok {
		t.Fatalf("tiny skill result reduced to (%q, %q)", reduced, rule)
	}
}

// A failed load carries no envelope, so it keeps the ordinary error handling
// instead of being summarized as a workflow.
func TestFailedSkillLoadIsNotInstructionState(t *testing.T) {
	if isSkillInstructionResult(requestReductionContext{ToolName: tools.NameSkill, Content: "skill not found"}) {
		t.Fatal("a skill result without an envelope must not classify as instructions")
	}
	if isSkillInstructionResult(requestReductionContext{ToolName: tools.NameRead, Content: "<root>/tmp</root>"}) {
		t.Fatal("only skill results classify as instructions")
	}
}

func TestSkillWorkflowHeadingsSkipFencedCode(t *testing.T) {
	body := "# Title\n\n```bash\n# not a heading\nnpm run build\n```\n\n" +
		"## Phase one\n### Nested\n####### seven hashes\n#hashtag\n\n~~~\n# also not a heading\n~~~\n"
	want := []string{"Title", "Phase one", "Nested"}
	if got := skillWorkflowHeadings(body); !slices.Equal(got, want) {
		t.Fatalf("skillWorkflowHeadings() = %q, want %q", got, want)
	}
}

func TestMarkdownHeadingTextRejectsNonHeadings(t *testing.T) {
	for _, tc := range []struct {
		line string
		want string
		ok   bool
	}{
		{line: "# Title", want: "Title", ok: true},
		{line: "  ##   Spaced  ", want: "Spaced", ok: true},
		{line: "#hashtag"},
		{line: "###### six", want: "six", ok: true},
		{line: "####### seven"},
		{line: "#"},
		{line: "not a heading"},
	} {
		got, ok := markdownHeadingText(strings.TrimSpace(tc.line))
		if ok != tc.ok || got != tc.want {
			t.Fatalf("markdownHeadingText(%q) = (%q, %v), want (%q, %v)", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}

// The full pipeline: an aged skill result reaches the LLM as anchors plus the
// workflow outline, and the text it drops stays readable at its archive
// address. The unit tests above pin the renderer; this pins the request the
// model actually gets.
func TestPrepareMessagesForLLM_SummarizesAgedSkillKeepingAnchors(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		MinToolResultsPrune: 1,
		StaleAgeTurns:       1,
		StaleOutputBytes:    40,
	}}}
	a.newTurn()
	root := t.TempDir()
	content := skillEnvelopeContent(root, skillTestBody())
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "sk1", Name: tools.NameSkill, Args: json.RawMessage(`{"name":"simplify-codebase"}`)}}},
		{Role: message.RoleTool, ToolCallID: "sk1", ToolStatus: message.ToolStatusSuccess, Content: content},
		{Role: message.RoleUser, Content: "u2"},
		{Role: message.RoleUser, Content: "u3"},
	}
	setTestRequestBatch(a, msgs, 2)
	prepared := a.prepareMessagesForLLM(msgs)
	var got string
	for _, msg := range prepared {
		if msg.Role == message.RoleTool && msg.ToolCallID == "sk1" {
			got = msg.Content
		}
	}
	if got == "" {
		t.Fatal("prepared messages dropped the skill result")
	}
	for _, want := range []string{
		"<path>" + root + "/SKILL.md</path>",
		"<root>" + root + "</root>",
		"Workflow headings:",
		"- Decide and act",
		tools.ArtifactReferencePrefix,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prepared skill content lost %q: %q", want, got)
		}
	}
	if len(got) >= len(content) {
		t.Fatalf("prepared skill content bytes = %d, want less than original %d", len(got), len(content))
	}
}
