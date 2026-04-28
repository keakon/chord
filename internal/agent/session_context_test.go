package agent

import (
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(got, "<system-reminder>") {
		t.Errorf("missing <system-reminder> marker: %q", got)
	}
	if !strings.Contains(got, "Today's date is 2026-04-17") {
		t.Errorf("missing date line: %q", got)
	}
	if strings.Contains(got, "claudeMd") {
		t.Errorf("should not mention claudeMd when AGENTS.md empty: %q", got)
	}
}

func TestBuildSessionContextReminder_WithAgentsMD(t *testing.T) {
	now := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	got := buildSessionContextReminder("project rules body", now)
	if got == "" {
		t.Fatal("expected non-empty reminder")
	}
	if !strings.Contains(got, "# claudeMd\nproject rules body") {
		t.Errorf("missing agents md section: %q", got)
	}
	if !strings.Contains(got, "# currentDate") {
		t.Errorf("missing currentDate section: %q", got)
	}
	if !strings.Contains(got, "IMPORTANT:") {
		t.Errorf("missing disclaimer: %q", got)
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
