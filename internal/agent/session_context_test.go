package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestBuildSessionContextReminder_Empty(t *testing.T) {
	if got := buildSessionContextReminder(SessionEnvSnapshot{}, ""); got != "" {
		t.Fatalf("expected empty for empty inputs, got %q", got)
	}
}

func TestBuildSessionContextReminder_OnlyDate(t *testing.T) {
	got := buildSessionContextReminder(SessionEnvSnapshot{Date: "Apr 17 2026"}, "")
	if got == "" {
		t.Fatal("expected non-empty reminder")
	}
	if !strings.Contains(got, "<env>") {
		t.Errorf("missing env block when Date set: %q", got)
	}
	if !strings.Contains(got, "Today's date: Apr 17 2026") {
		t.Errorf("missing env date line: %q", got)
	}
	if strings.Contains(got, "# currentDate") {
		t.Errorf("should not duplicate currentDate when env date is present: %q", got)
	}
	if strings.Contains(got, "Today's date is 2026-04-17") {
		t.Errorf("should not duplicate ISO date when env date is present: %q", got)
	}
	if strings.Contains(got, "AGENTS.md") {
		t.Errorf("should not mention AGENTS.md when AGENTS.md empty: %q", got)
	}
}

func TestBuildSessionContextReminder_IncludesSessionWorkingDirectory(t *testing.T) {
	got := buildSessionContextReminder(SessionEnvSnapshot{WorkDir: "/repo/session", Platform: "test/os", Date: "Apr 17 2026"}, "")
	if got == "" {
		t.Fatal("expected non-empty reminder")
	}
	for _, want := range []string{
		"<env>",
		"Working directory: /repo/session",
		"Platform: test/os",
		"Today's date: Apr 17 2026",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("reminder missing %q: %q", want, got)
		}
	}
}

func TestCallLLMInjectsWorkingDirectoryReminderIntoFirstProviderRequest(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.cachedWorkDir = "/repo/session"
	a.cachedAgentsMD = ""
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

	_, err := a.callLLM(t.Context(), []message.Message{{Role: "user", Content: "where am I"}})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}
	seen := provider.seenMessages[0]
	if len(seen) < 2 {
		t.Fatalf("provider messages = %#v, want reminder plus user message", seen)
	}
	if seen[0].Role != "user" || !strings.Contains(seen[0].Content, "Working directory: /repo/session") {
		t.Fatalf("first provider message missing working directory reminder: %#v", seen[0])
	}
	if !strings.Contains(seen[1].Content, "where am I") {
		t.Fatalf("actual user message = %q", seen[1].Content)
	}
}

func TestBuildSessionContextReminder_WithAgentsMD(t *testing.T) {
	got := buildSessionContextReminder(SessionEnvSnapshot{Date: "Apr 17 2026"}, "project rules body")
	if got == "" {
		t.Fatal("expected non-empty reminder")
	}
	if !strings.HasPrefix(got, "# AGENTS.md instructions\n") {
		t.Errorf("AGENTS.md block must declare its identity on the first line: %q", got)
	}
	if !strings.Contains(got, "<INSTRUCTIONS>") || !strings.Contains(got, "</INSTRUCTIONS>") {
		t.Errorf("missing <INSTRUCTIONS> bounding markers: %q", got)
	}
	if !strings.Contains(got, "Each applicable AGENTS.md is already loaded here before the first visible user message") {
		t.Errorf("missing AGENTS.md loaded-state line: %q", got)
	}
	if !strings.Contains(got, "root-to-current order") || !strings.Contains(got, "with its path labeled") {
		t.Errorf("missing AGENTS.md scoped loading details: %q", got)
	}
	if !strings.Contains(got, "Use those loaded sections as scoped workspace instructions") {
		t.Errorf("missing AGENTS.md workspace-instruction guidance: %q", got)
	}
	if !strings.Contains(got, "inspect only task-relevant project files needed to understand, modify, or verify the requested work") {
		t.Errorf("missing task-relevant file inspection guidance: %q", got)
	}
	if !strings.Contains(got, "<INSTRUCTIONS>\n") || !strings.Contains(got, "\nproject rules body\n") || !strings.Contains(got, "project rules body\n</INSTRUCTIONS>") {
		t.Errorf("AGENTS.md body must be bounded inside <INSTRUCTIONS>: %q", got)
	}
	if strings.Contains(got, "# currentDate") || strings.Contains(got, "Today's date is") {
		t.Errorf("should not render duplicate currentDate section: %q", got)
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
	if !strings.Contains(seen[0].Content, "Each applicable AGENTS.md is already loaded here") || !strings.Contains(seen[0].Content, "inspect only task-relevant project files") {
		t.Fatalf("first provider message should explain loaded AGENTS.md state: %#v", seen[0])
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
