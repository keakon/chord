package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestBuildSessionContextReminder_Empty(t *testing.T) {
	if got := buildSessionContextReminder("", time.Time{}); got != "" {
		t.Fatalf("expected empty for empty inputs, got %q", got)
	}
}

func TestBuildSessionContextReminder_OnlyDate(t *testing.T) {
	now := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	got := buildSessionContextReminder("", now)
	if got == "" {
		t.Fatal("expected non-empty reminder")
	}
	if !strings.Contains(got, "# currentDate") {
		t.Errorf("missing currentDate section: %q", got)
	}
	if !strings.Contains(got, "Today's date is 2026-04-17") {
		t.Errorf("missing date line: %q", got)
	}
	if strings.Contains(got, "AGENTS.md") {
		t.Errorf("should not mention AGENTS.md when AGENTS.md empty: %q", got)
	}
}

func TestBuildSessionContextReminder_WithAgentsMD(t *testing.T) {
	now := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	got := buildSessionContextReminder("project rules body", now)
	if got == "" {
		t.Fatal("expected non-empty reminder")
	}
	if !strings.HasPrefix(got, "# AGENTS.md instructions\n") {
		t.Errorf("AGENTS.md block must declare its identity on the first line: %q", got)
	}
	if !strings.Contains(got, "<INSTRUCTIONS>") || !strings.Contains(got, "</INSTRUCTIONS>") {
		t.Errorf("missing <INSTRUCTIONS> bounding markers: %q", got)
	}
	if !strings.Contains(got, "complete applicable AGENTS.md content for this workspace session") {
		t.Errorf("missing AGENTS.md source line: %q", got)
	}
	if !strings.Contains(got, "walking from the current working directory up to the project root") || !strings.Contains(got, "project-root-to-current-working-directory order") {
		t.Errorf("missing AGENTS.md loading scope: %q", got)
	}
	if !strings.Contains(got, "Each loaded section is labeled with its path relative to the current working directory") {
		t.Errorf("missing AGENTS.md section path labeling: %q", got)
	}
	if !strings.Contains(got, "internal workspace guidance injected before the first real user message") || !strings.Contains(got, "may not appear in the visible transcript") {
		t.Errorf("missing internal injection visibility boundary: %q", got)
	}
	if !strings.Contains(got, "<INSTRUCTIONS>\n") || !strings.Contains(got, "\nproject rules body\n") || !strings.Contains(got, "project rules body\n</INSTRUCTIONS>") {
		t.Errorf("AGENTS.md body must be bounded inside <INSTRUCTIONS>: %q", got)
	}
	if !strings.Contains(got, "# currentDate") {
		t.Errorf("missing currentDate section: %q", got)
	}
	if !strings.Contains(got, "durable workspace context and system-provided guidance, not ordinary user content") {
		t.Errorf("missing durability reminder: %q", got)
	}
	if strings.Contains(got, "may or may not be relevant") {
		t.Errorf("AGENTS.md reminder should not weaken repository instructions as optional context: %q", got)
	}
}

func TestCallLLMInjectsAgentsMDReminderIntoFirstProviderRequest(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.cachedAgentsMD = "# Repo Rules\n- Follow repository rules before scanning."
	a.refreshSystemPrompt()
	a.refreshSessionContextReminder()

	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{Content: "ok", StopReason: "stop"}}}}
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "")

	_, err := a.callLLM(t.Context(), []message.Message{{Role: "user", Content: "analyze hardcoded behavior"}})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}
	if len(provider.seenMessages) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(provider.seenMessages))
	}
	seen := provider.seenMessages[0]
	if len(seen) < 2 {
		t.Fatalf("provider messages = %#v, want reminder plus user message", seen)
	}
	if seen[0].Role != "user" || !strings.Contains(seen[0].Content, "# AGENTS.md instructions") || !strings.Contains(seen[0].Content, "<INSTRUCTIONS>") || !strings.Contains(seen[0].Content, "# Repo Rules") {
		t.Fatalf("first provider message missing AGENTS.md reminder: %#v", seen[0])
	}
	if !strings.Contains(seen[0].Content, "may not appear in the visible transcript") {
		t.Fatalf("first provider message should explain transcript visibility boundary: %#v", seen[0])
	}
	if got := seen[1].Content; !strings.Contains(got, "analyze hardcoded behavior") {
		t.Fatalf("actual user message = %q, want original content preserved after reminder", got)
	}
}

func TestInjectMetaUserReminder_PrependsBeforeFirstUser(t *testing.T) {
	content := "<system-reminder>hi</system-reminder>"
	msgs := []message.Message{
		{Role: "assistant", Content: "prev"},
		{Role: "user", Content: "actual"},
	}
	out := injectMetaUserReminder(msgs, content)
	if len(out) != 3 {
		t.Fatalf("len: want 3, got %d", len(out))
	}
	if out[1].Content != content {
		t.Errorf("reminder not inserted before first user: %+v", out)
	}
	if out[2].Content != "actual" {
		t.Errorf("actual user message displaced: %+v", out[2])
	}
}

func TestInjectMetaUserReminder_EmptyContentNoop(t *testing.T) {
	msgs := []message.Message{{Role: "user", Content: "hi"}}
	out := injectMetaUserReminder(msgs, "")
	if len(out) != 1 || out[0].Content != "hi" {
		t.Errorf("unexpected mutation: %+v", out)
	}
}

func TestInjectMetaUserReminder_EmptyMessagesReturnsReminderOnly(t *testing.T) {
	content := "<system-reminder>x</system-reminder>"
	out := injectMetaUserReminder(nil, content)
	if len(out) != 1 || out[0].Content != content {
		t.Errorf("unexpected: %+v", out)
	}
}
