//go:build unix

package externalclaude

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSupervisedFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// startSupervisedSession starts a fixture process under an independent
// supervisor. The supervisor is this test binary: Chord re-executes the
// running executable, and the TestMain intercept turns it into a supervisor.
func startSupervisedSession(t *testing.T, body string) *Session {
	t.Helper()
	session, err := Start(ProcessConfig{
		Command:    writeSupervisedFixture(t, body),
		KillGrace:  time.Second,
		Supervisor: &SupervisorConfig{},
	})
	if err != nil {
		t.Fatalf("start supervised session: %v", err)
	}
	return session
}

// A supervised run has to be indistinguishable from a direct one on the
// business pipes: Chord still initializes, sends a turn and reads the result.
func TestSupervisedSessionStreamsResult(t *testing.T) {
	session := startSupervisedSession(t, `
IFS= read -r init
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"chord-initialize","response":{}}}'
IFS= read -r prompt
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"supervised done"}'
`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := session.Initialize(ctx, ""); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := session.WriteUserMessage(ctx, "go"); err != nil {
		t.Fatalf("send prompt: %v", err)
	}
	event := <-session.Events()
	if event.Type != TypeResult || event.Result != "supervised done" {
		t.Fatalf("event = %+v", event)
	}
	session.Finish(2 * time.Second)
	for range session.Events() {
	}
	if err := session.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !session.ProcessEnded() {
		t.Fatal("supervised process group survived the run")
	}
}

// A process that breaks the protocol still has to be cleaned up: the
// supervisor owns the group, and Chord verifies the group is gone before it
// reports the worker as ended.
func TestSupervisedSessionTerminatesOnProtocolError(t *testing.T) {
	session := startSupervisedSession(t, `
printf 'not json\n'
sleep 60
`)
	for range session.Events() {
	}
	if err := session.Wait(); err == nil {
		t.Fatal("a protocol violation ended without an error")
	}
	if !session.ProcessEnded() {
		t.Fatal("supervised process group survived a protocol failure")
	}
}

// The case the supervisor exists for: a managed process that exited but left a
// child holding its pipes open must still leave nothing behind.
func TestSupervisedSessionCleansUpLeakedChild(t *testing.T) {
	session := startSupervisedSession(t, `
IFS= read -r init
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"chord-initialize","response":{}}}'
IFS= read -r prompt
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"leaky done"}'
sleep 60 &
exit 0
`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := session.Initialize(ctx, ""); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := session.WriteUserMessage(ctx, "go"); err != nil {
		t.Fatalf("send prompt: %v", err)
	}
	if event := <-session.Events(); event.Type != TypeResult {
		t.Fatalf("event = %+v", event)
	}
	session.Finish(time.Second)
	for range session.Events() {
	}
	_ = session.Wait()
	if !session.ProcessEnded() {
		t.Fatal("a child left behind by the managed process survived the run")
	}
}

// startEscapedSession starts a fixture that leaves one descendant outside the
// managed process group while that descendant keeps the business pipes open.
// The group kill cannot reach it, so only Chord closing the read ends can end
// the run.
func startEscapedSession(t *testing.T, supervised bool) *Session {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	ready := filepath.Join(t.TempDir(), "escaped-ready")
	cfg := ProcessConfig{
		Command: writeSupervisedFixture(t, `
IFS= read -r init
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"chord-initialize","response":{}}}'
IFS= read -r prompt
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"escaped done"}'
"$1" escaped-descendant-test-helper "$2" &
while [ ! -f "$2" ]; do sleep 0.01; done
exit 0
`),
		Args:      []string{exe, ready},
		KillGrace: time.Second,
	}
	if supervised {
		cfg.Supervisor = &SupervisorConfig{}
	}
	session, err := Start(cfg)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	return session
}

// A result that was already delivered stays a result even when an escaped
// descendant keeps the pipes open: the run ends once Chord stops reading
// instead of waiting for that process or turning the teardown into a failure.
func assertEscapedDescendantRunEnds(t *testing.T, session *Session) {
	t.Helper()
	t.Cleanup(func() {
		session.Stop(time.Second)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := session.Initialize(ctx, ""); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := session.WriteUserMessage(ctx, "go"); err != nil {
		t.Fatalf("send prompt: %v", err)
	}
	select {
	case event, ok := <-session.Events():
		if !ok || event.Type != TypeResult {
			t.Fatalf("event = %+v, stream open = %t", event, ok)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the external result")
	}
	session.Finish(5 * time.Second)
	waited := make(chan error, 1)
	go func() { waited <- session.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("an escaped descendant turned a delivered result into a failure: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session never ended while an escaped descendant held the streams")
	}
	if !session.TerminationProven() {
		t.Fatal("the managed process group survived the run")
	}
}

func TestSupervisedSessionEndsWhenEscapedDescendantHoldsStreams(t *testing.T) {
	assertEscapedDescendantRunEnds(t, startEscapedSession(t, true))
}

func TestDirectSessionEndsWhenEscapedDescendantHoldsStreams(t *testing.T) {
	assertEscapedDescendantRunEnds(t, startEscapedSession(t, false))
}
