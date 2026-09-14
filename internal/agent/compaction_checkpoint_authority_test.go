package agent

import (
	"strings"
	"testing"
)

// A checkpoint is the one place where model-authored text sits next to runtime
// facts in the same shape. The tests here pin the contract that keeps the
// difference legible to whoever reads the checkpoint next: model-declared
// state_files references are rendered as unverified claims (never existence-
// stamped, which would turn a checkpoint into a cross-permission probe), and
// the continuation guidance states which source wins when they disagree.

func TestStateFilesSectionNeverStampsExistence(t *testing.T) {
	section := renderStateFilesSection([]string{
		"notes.md — the running task notes",
		"missing/plan.md",
		"the design decisions we agreed on",
	})
	if strings.Contains(section, "verified present") || strings.Contains(section, "NOT FOUND") {
		t.Fatalf("state_files must never be existence-stamped:\n%s", section)
	}
	if !strings.Contains(section, "notes.md — the running task notes") {
		t.Fatalf("an existing reference was dropped:\n%s", section)
	}
	if !strings.Contains(section, "missing/plan.md") {
		t.Fatalf("a missing reference was dropped:\n%s", section)
	}
	if !strings.Contains(section, "existence is not verified") {
		t.Fatalf("the section must state references are unverified:\n%s", section)
	}
}

func TestStateFilesSectionEmptyFallback(t *testing.T) {
	section := renderStateFilesSection(nil)
	if !strings.Contains(section, "none reported") {
		t.Fatalf("empty state_files must render a fallback marker:\n%s", section)
	}
	// An empty list is legitimate, so the fallback is actionable rather than a
	// rejection: it names where the continuation's state lives and points at
	// the next checkpoint as the place to register a notes/plan file.
	if !strings.Contains(section, "register it at the next checkpoint") {
		t.Fatalf("empty state_files must tell the continuation how to close the gap:\n%s", section)
	}
	// The nudge belongs to the empty case only; a registered reference must
	// not carry it.
	registered := renderStateFilesSection([]string{"notes/current.md"})
	if strings.Contains(registered, "register it at the next checkpoint") {
		t.Fatalf("a non-empty state_files section must not carry the empty-case nudge:\n%s", registered)
	}
}

// TestCheckpointContinuationStatesPrecedence covers the rule the checkpoint
// itself cannot express: which source wins when the preserved summary and a
// newer fact disagree. Both continuation paths must carry it, and the user's
// newest message must outrank the checkpoint text.
func TestCheckpointContinuationStatesPrecedence(t *testing.T) {
	for name, text := range map[string]string{
		"overlay":       appendContextPressureVerificationGuidance("System note: checkpoint applied."),
		"auto_continue": autoContinuePrompt(),
	} {
		t.Run(name, func(t *testing.T) {
			userIdx := strings.Index(text, "latest user message")
			checkpointIdx := strings.Index(text, "checkpoint's own text")
			if userIdx < 0 || checkpointIdx < 0 {
				t.Fatalf("continuation guidance lost its precedence clause: %q", text)
			}
			if userIdx > checkpointIdx {
				t.Fatalf("the checkpoint must never outrank the newest user message: %q", text)
			}
			for _, source := range []string{"runtime state", "files on disk", "tool results", "archived artifacts"} {
				if !strings.Contains(text, source) {
					t.Fatalf("precedence clause omits %q: %q", source, text)
				}
			}
		})
	}
}
