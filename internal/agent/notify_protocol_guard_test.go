package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func notifyCallArgs(t *testing.T, target, messageType, correlationID, kind, message string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"target_task_id": target,
		"message_type":   messageType,
		"correlation_id": correlationID,
		"kind":           kind,
		"message":        message,
	})
	if err != nil {
		t.Fatalf("marshal notify args: %v", err)
	}
	return string(raw)
}

// TestNotifyProtocolStreakCountsAcrossChangingArgs pins the reviewer-8 C
// counting rule: empty id → forged id → empty id again, with changing
// message/kind in between, all keep counting toward the same target because
// they belong to the same "no valid response correlation" error family.
func TestNotifyProtocolStreakCountsAcrossChangingArgs(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	const target = "task-notify-1"
	missingCorrelation := errors.New("correlation_id is required for response: message_type=response answers only a real pending request")
	unknownRequest := errors.New(`unknown pending request "corr-forged" for task task-notify-1`)

	// 1st failure: empty correlation id.
	note, pause := a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, target, "response", "", "reply", "please continue"), missingCorrelation)
	if note != "" || pause {
		t.Fatalf("first failure => note=%q pause=%v, want silent count", note, pause)
	}
	// 2nd failure: a different (forged) id and different message/kind.
	note, pause = a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, target, "response", "corr-forged", "correction", "try again with option B"), unknownRequest)
	if note != notifyResponseProtocolGuidance || pause {
		t.Fatalf("second failure => note=%q pause=%v, want reinforced guidance", note, pause)
	}
	// 3rd failure: empty correlation id again.
	note, pause = a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, target, "response", "", "reply", "please continue once more"), missingCorrelation)
	if note != notifyResponseProtocolPauseNote || !pause {
		t.Fatalf("third failure => note=%q pause=%v, want pause", note, pause)
	}
	if a.turn == nil || a.turn.notifyProtocolStreak[target] != notifyProtocolPauseThreshold {
		t.Fatalf("streak after three failures = %#v, want %d", a.turn.notifyProtocolStreak, notifyProtocolPauseThreshold)
	}
}

// TestNotifyProtocolStreakIgnoresNonProtocolErrors pins the "only certain
// protocol errors count" rule: IO/network/session and other tool failures, and
// plain (non-response) notify failures, must never feed the counter.
func TestNotifyProtocolStreakIgnoresNonProtocolErrors(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	const target = "task-notify-io"
	for _, tc := range []struct {
		name string
		args string
		err  error
	}{
		{"plain notify io failure", notifyCallArgs(t, target, "progress", "", "progress", "still working"), errors.New("targeted notify is not available")},
		{"response session change", notifyCallArgs(t, target, "response", "corr-real", "reply", "answer"), errors.New("agent response delivery invalidated by session change")},
		{"response persistence failure", notifyCallArgs(t, target, "response", "corr-real", "reply", "answer"), errors.New("failed to persist agent request: open: permission denied")},
		{"unrelated tool error", `{"path":"notes.txt"}`, errors.New("no such file")},
	} {
		note, pause := a.observeNotifyProtocolFailure(tools.NameNotify, tc.args, tc.err)
		if note != "" || pause {
			t.Fatalf("%s => note=%q pause=%v, want no count", tc.name, note, pause)
		}
	}
	if len(a.turn.notifyProtocolStreak) != 0 {
		t.Fatalf("non-protocol errors fed the streak: %#v", a.turn.notifyProtocolStreak)
	}
	// A non-notify tool result never enters the guard either.
	note, pause := a.observeNotifyProtocolFailure(tools.NameRead, `{"path":"x.txt"}`, errors.New("boom"))
	if note != "" || pause {
		t.Fatalf("non-notify tool => note=%q pause=%v, want no-op", note, pause)
	}
}

// TestNotifyProtocolStreakResetsOnSuccessAndRecoversWithPlainNotify pins the
// "real valid reply and plain notifications must allow recovery" rule: a
// successful notify call on the target clears its streak, so a later isolated
// mistake starts over instead of inheriting stale failures.
func TestNotifyProtocolStreakResetsOnSuccessAndRecoversWithPlainNotify(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	const target = "task-notify-recover"
	missing := errors.New("correlation_id is required for response: message_type=response answers only a real pending request")
	if _, pause := a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, target, "response", "", "reply", "one"), missing); pause {
		t.Fatal("unexpected pause on first failure")
	}
	if _, pause := a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, target, "response", "corr-x", "reply", "two"), errors.New(`unknown pending request "corr-x" for task task-notify-recover`)); pause {
		t.Fatal("unexpected pause on second failure")
	}
	// The model switches to a genuine valid reply: success clears the streak.
	note, pause := a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, target, "response", "corr-real", "reply", "option A"), nil)
	if note != "" || pause {
		t.Fatalf("valid response => note=%q pause=%v, want silent reset", note, pause)
	}
	// A plain (non-response) notify success is equally a recovery.
	note, pause = a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, target, "progress", "", "progress", "still working"), nil)
	if note != "" || pause {
		t.Fatalf("plain notify success => note=%q pause=%v, want silent reset", note, pause)
	}
	// One isolated failure after recovery must not pause.
	if _, pause := a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, target, "response", "", "reply", "again"), missing); pause {
		t.Fatal("isolated failure after a recovery paused the turn")
	}
	if a.turn.notifyProtocolStreak[target] != 1 {
		t.Fatalf("streak after isolated failure = %d, want 1 (reset happened on success)", a.turn.notifyProtocolStreak[target])
	}
}

// TestNotifyProtocolStreakSeparatesTargets pins per-target accounting: a
// healthy target never inherits another target's failures, and clearing one
// target does not clear the other.
func TestNotifyProtocolStreakSeparatesTargets(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	missing := errors.New("correlation_id is required for response: message_type=response answers only a real pending request")
	unknown := errors.New(`unknown pending request "corr-fake" for task task-a`)
	a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, "task-a", "response", "corr-fake", "reply", "x"), unknown)
	a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, "task-a", "response", "", "reply", "y"), missing)
	a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, "task-b", "response", "", "reply", "z"), missing)
	if a.turn.notifyProtocolStreak["task-a"] != 2 || a.turn.notifyProtocolStreak["task-b"] != 1 {
		t.Fatalf("per-target streaks = %#v, want task-a=2 task-b=1", a.turn.notifyProtocolStreak)
	}
	a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, "task-b", "response", "corr-b", "reply", "w"), errors.New(`unknown pending request "corr-b" for task task-b`))
	a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, "task-b", "response", "", "reply", "again"), missing)
	if !a.turn.notifyProtocolPause {
		t.Fatal("third failure on task-b did not flag the pause; task-a's streak must not delay it")
	}
	if a.turn.notifyProtocolPauseTask != "task-b" {
		t.Fatalf("pause task = %q, want task-b", a.turn.notifyProtocolPauseTask)
	}
}

// TestNotifyProtocolPauseIsTurnScopedAndClearedOnConsume pins the
// turn/session reset and batch-terminal behavior: a new turn drops the streak,
// and consumeNotifyProtocolPause ends the turn and asks the user for an
// explicit correction instead of auto-retrying.
func TestNotifyProtocolPauseIsTurnScopedAndClearedOnConsume(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	firstTurn := a.turn.ID
	const target = "task-notify-turn"
	missing := errors.New("correlation_id is required for response: message_type=response answers only a real pending request")
	for range notifyProtocolPauseThreshold {
		a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, target, "response", "", "reply", "again"), missing)
	}
	if !a.consumeNotifyProtocolPause() {
		t.Fatal("consumeNotifyProtocolPause did not consume the flagged pause")
	}
	if a.turn != nil {
		t.Fatalf("turn still active after pause, turn=%#v", a.turn)
	}
	if a.consumeNotifyProtocolPause() {
		t.Fatal("consumeNotifyProtocolPause fired without a pause flag")
	}
	// A fresh turn (a new user message / session) starts with a clean streak.
	a.newTurn()
	if a.turn.ID == firstTurn {
		t.Fatalf("expected a fresh turn id, got %v", a.turn.ID)
	}
	if a.turn.notifyProtocolStreak != nil || a.turn.notifyProtocolPause {
		t.Fatalf("fresh turn inherited the pause state: %#v", a.turn)
	}
	note, pause := a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, target, "response", "", "reply", "one more"), missing)
	if note != "" || pause {
		t.Fatalf("fresh turn counted a stale failure: note=%q pause=%v", note, pause)
	}
}

// TestNotifyProtocolPauseEmitsUserInputRequest verifies the pause surfaces a
// user-visible notification asking for explicit correction (the owner/user
// decision point the third failure must reach).
func TestNotifyProtocolPauseEmitsUserInputRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.turn.notifyProtocolPause = true
	a.turn.notifyProtocolPauseTask = "task-notify-user"
	if !a.consumeNotifyProtocolPause() {
		t.Fatal("consumeNotifyProtocolPause did not consume the pause")
	}
	var notified bool
	for len(a.outputCh) > 0 {
		switch evt := (<-a.outputCh).(type) {
		case InfoEvent:
			if strings.Contains(evt.Message, "Notify paused") {
				notified = true
			}
		case NotificationEvent:
			if evt.Reason == NotificationReasonUserInputRequired {
				notified = true
			}
		}
	}
	if !notified {
		t.Fatal("expected a user-input notification for the paused notify retry")
	}
}

// TestNotifyProtocolGuardIgnoresLoopSwitch pins that the loop enablement does
// not change this protection (unlike the repeated-tool-call interception).
func TestNotifyProtocolGuardIgnoresLoopSwitch(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.loopState.Enabled = true
	missing := errors.New("correlation_id is required for response: message_type=response answers only a real pending request")
	a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, "task-a", "response", "", "reply", "x"), missing)
	a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, "task-a", "response", "", "reply", "y"), missing)
	if a.turn.notifyProtocolStreak["task-a"] != 2 {
		t.Fatalf("loop-on streak = %d, want 2", a.turn.notifyProtocolStreak["task-a"])
	}
	a.loopState.Enabled = false
	a.observeNotifyProtocolFailure(tools.NameNotify, notifyCallArgs(t, "task-a", "response", "", "reply", "z"), missing)
	if a.turn.notifyProtocolStreak["task-a"] != 3 || !a.turn.notifyProtocolPause {
		t.Fatalf("loop-off streak = %#v pause=%v, want count 3 + pause (loop switch is irrelevant)", a.turn.notifyProtocolStreak, a.turn.notifyProtocolPause)
	}
}
