//go:build unix

package externalclaude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startFixtureSession(t *testing.T, body string, maxLine int) *Session {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Start(ProcessConfig{Command: path, MaxLineBytes: maxLine, KillGrace: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Terminate()
		for range s.Events() {
		}
		_ = s.Wait()
	})
	return s
}

func TestSessionInitializationBackpressureCancels(t *testing.T) {
	s := startFixtureSession(t, "exec sleep 60\n", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := s.Initialize(ctx, strings.Repeat("x", 2<<20)); err == nil {
		t.Fatal("blocked initialization succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("initialization write ignored cancellation")
	}
}

func TestSessionProtocolFailuresTerminate(t *testing.T) {
	for _, test := range []struct {
		name, body string
		max        int
	}{
		{"invalid", "printf 'invalid\\n'\nexec sleep 60\n", 0},
		{"long stdout", "printf '%200s\\n' x\nexec sleep 60\n", 128},
		{"long stderr", "printf '%1100000s\\n' x >&2\nexec sleep 60\n", 0},
		{"child inherited pipes", "sleep 60 &\nexit 0\n", 0},
		{"malformed actionable frame", "printf '%s\\n' '{\"type\":\"assistant\",\"message\":\"not an object\"}'\nexec sleep 60\n", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := startFixtureSession(t, test.body, test.max)
			for range s.Events() {
			}
			if err := s.Wait(); err == nil {
				t.Fatal("protocol or child-process failure was ignored")
			}
			if !s.ProcessEnded() {
				t.Fatal("external process group remained alive")
			}
		})
	}
}

func TestSessionToleratesProtocolDriftBeforeInitialize(t *testing.T) {
	// UI system frames arrive before the initialize acknowledgment, and a later
	// release may add fields with shapes this decoder does not know. Neither may
	// stop the session or swallow the frames that follow.
	s := startFixtureSession(t, `
IFS= read -r line
printf '%s\n' '{"type":"system","subtype":"ui_invalidate","event":"ui.render","session_id":"efe26821-06f5-49b3-968e-34e763b3de3f"}'
printf '%s\n' '{"type":"system","subtype":"ui_toast","message":"dismiss"}'
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"chord-initialize"}}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"par"}}}'
printf '%s\n' '{"type":"future_event","tools":"Read"}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"tial"}}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'
exit 0
`, 0)
	deferred, err := s.Initialize(context.Background(), "task")
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if len(deferred) != 2 || deferred[0].Subtype != "ui_invalidate" || deferred[1].Subtype != "ui_toast" {
		t.Fatalf("deferred initialization events = %+v", deferred)
	}
	var got []Event
	for event := range s.Events() {
		got = append(got, event)
	}
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	want := []string{TypeStreamEvent, "future_event", TypeStreamEvent, TypeResult}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %d", got, len(want))
	}
	for i, eventType := range want {
		if got[i].Type != eventType {
			t.Fatalf("event %d type = %q, want %q", i, got[i].Type, eventType)
		}
	}
	for i, text := range []string{"par", "tial"} {
		event := got[i*2]
		if event.StreamEvent == nil || event.StreamEvent.Delta == nil || event.StreamEvent.Delta.Text != text {
			t.Fatalf("stream event %d = %+v", i, event)
		}
	}
}

func TestSessionFinishDrainsResultWithoutInterrupt(t *testing.T) {
	s := startFixtureSession(t, `
IFS= read -r line
printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'
while IFS= read -r line; do exit 9; done
`, 0)
	if err := s.WriteUserMessage(context.Background(), "Input."); err != nil {
		t.Fatal(err)
	}
	if event := <-s.Events(); event.Type != TypeResult {
		t.Fatalf("event = %+v", event)
	}
	s.Finish(time.Second)
	for range s.Events() {
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	if !s.ProcessEnded() {
		t.Fatal("process still exists")
	}
}

func TestManagedEnvironmentAndDiagnostic(t *testing.T) {
	env := processEnvironment([]string{
		"HOME=/sample", "PATH=/bin", "NODE_OPTIONS=--require=sample",
		"CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1", "ANTHROPIC_API_KEY=old",
	}, map[string]string{"ANTHROPIC_API_KEY": "new"})
	got := strings.Join(env, "\n")
	if strings.Contains(got, "NODE_OPTIONS") || strings.Contains(got, "RESUME_INTERRUPTED") ||
		strings.Contains(got, "=old") || !strings.Contains(got, "HOME=/sample") || !strings.Contains(got, "=new") {
		t.Fatalf("environment = %s", got)
	}
	t.Setenv("SAMPLE_SECRET", "fixture-secret")
	if got := RedactDiagnostic("fixture-secret fixture-token", map[string]string{"ANTHROPIC_AUTH_TOKEN": "fixture-token"}); strings.Contains(got, "fixture-") {
		t.Fatalf("secret leaked: %s", got)
	}
}
