package agent

import (
	"encoding/json"
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
	a.newTurn("")
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
