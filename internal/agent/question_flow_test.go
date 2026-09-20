package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func waitQuestionRequestEvent(t *testing.T, a *MainAgent) QuestionRequestEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-a.Events():
			if q, ok := e.(QuestionRequestEvent); ok {
				return q
			}
		case <-deadline:
			t.Fatal("question request event not emitted")
			return QuestionRequestEvent{}
		}
	}
}

func waitQuestionResolvedEvent(t *testing.T, a *MainAgent, requestID string) QuestionResolvedEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-a.Events():
			if r, ok := e.(QuestionResolvedEvent); ok && r.RequestID == requestID {
				return r
			}
		case <-deadline:
			t.Fatalf("question resolved event not emitted for %s", requestID)
			return QuestionResolvedEvent{}
		}
	}
}

func questionItems(headers ...string) []tools.QuestionItem {
	items := make([]tools.QuestionItem, 0, len(headers))
	for _, h := range headers {
		items = append(items, tools.QuestionItem{
			Header:   h,
			Question: "q-" + h,
			Options:  []tools.QuestionOption{{Label: "yes"}, {Label: "no"}},
		})
	}
	return items
}

// TestAskQuestionsFillsNotAskedAfterNonAnswered pins the batch contract: an
// answered question is preserved, the first non-answered question keeps its
// outcome, and every later question becomes not_asked with no request event.
func TestAskQuestionsFillsNotAskedAfterNonAnswered(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	ctx := a.turn.Ctx

	type outcome struct {
		answers []tools.QuestionAnswer
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		answers, err := a.AskQuestions(ctx, questionItems("h1", "h2", "h3"), 0)
		done <- outcome{answers: answers, err: err}
	}()

	q1 := waitQuestionRequestEvent(t, a)
	if _, accepted := a.ResolveQuestion([]string{"yes"}, tools.QuestionOutcomeAnswered, q1.RequestID); !accepted {
		t.Fatal("first answer was not accepted")
	}
	if got := waitQuestionResolvedEvent(t, a, q1.RequestID); got.Reason != tools.QuestionOutcomeAnswered {
		t.Fatalf("first resolved reason = %q, want answered", got.Reason)
	}

	q2 := waitQuestionRequestEvent(t, a)
	if q2.Header != "h2" {
		t.Fatalf("second request header = %q, want h2", q2.Header)
	}
	if _, accepted := a.ResolveQuestion(nil, tools.QuestionOutcomeDeclined, q2.RequestID); !accepted {
		t.Fatal("decline was not accepted")
	}
	if got := waitQuestionResolvedEvent(t, a, q2.RequestID); got.Reason != tools.QuestionOutcomeDeclined {
		t.Fatalf("second resolved reason = %q, want declined", got.Reason)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("AskQuestions err = %v", got.err)
	}
	if len(got.answers) != 3 {
		t.Fatalf("answers = %#v, want 3 entries", got.answers)
	}
	if got.answers[0].Outcome != tools.QuestionOutcomeAnswered || len(got.answers[0].Selected) != 1 || got.answers[0].Selected[0] != "yes" {
		t.Fatalf("answers[0] = %#v, want answered yes", got.answers[0])
	}
	if got.answers[1].Outcome != tools.QuestionOutcomeDeclined || len(got.answers[1].Selected) != 0 {
		t.Fatalf("answers[1] = %#v, want declined with no selection", got.answers[1])
	}
	if got.answers[2].Outcome != tools.QuestionOutcomeNotAsked || len(got.answers[2].Selected) != 0 {
		t.Fatalf("answers[2] = %#v, want not_asked", got.answers[2])
	}
}

// TestAskQuestionsDeadlineYieldsNoResponse verifies the request carries an
// absolute deadline and an unanswered request closes as no_response with a
// matching resolved event, not an error.
func TestAskQuestionsDeadlineYieldsNoResponse(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	ctx := a.turn.Ctx

	type outcome struct {
		answers []tools.QuestionAnswer
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		answers, err := a.AskQuestions(ctx, questionItems("h1"), 50*time.Millisecond)
		done <- outcome{answers: answers, err: err}
	}()

	q1 := waitQuestionRequestEvent(t, a)
	if q1.Deadline.IsZero() {
		t.Fatal("a positive timeout must produce an absolute deadline")
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("AskQuestions err = %v", got.err)
	}
	if len(got.answers) != 1 || got.answers[0].Outcome != tools.QuestionOutcomeNoResponse {
		t.Fatalf("answers = %#v, want one no_response", got.answers)
	}
	if resolved := waitQuestionResolvedEvent(t, a, q1.RequestID); resolved.Reason != tools.QuestionOutcomeNoResponse {
		t.Fatalf("resolved reason = %q, want no_response", resolved.Reason)
	}
}

// TestAskQuestionsSystemCancelReturnsError verifies a cancelled turn returns an
// error rather than a fabricated successful batch, while still closing the
// published request as cancelled.
func TestAskQuestionsSystemCancelReturnsError(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	ctx := a.turn.Ctx

	done := make(chan error, 1)
	go func() {
		_, err := a.AskQuestions(ctx, questionItems("h1"), 0)
		done <- err
	}()

	q1 := waitQuestionRequestEvent(t, a)
	a.CancelCurrentTurn()

	if err := <-done; err == nil {
		t.Fatal("a cancelled turn must return an error, not a fabricated batch")
	}
	if resolved := waitQuestionResolvedEvent(t, a, q1.RequestID); resolved.Reason != QuestionResolvedReasonCancelled {
		t.Fatalf("resolved reason = %q, want cancelled", resolved.Reason)
	}
}

// TestAskQuestionsSupersedeClosesRequest verifies a superseded request returns
// the superseded outcome and emits the matching resolved event.
func TestAskQuestionsSupersedeClosesRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	ctx := a.turn.Ctx

	done := make(chan []tools.QuestionAnswer, 1)
	go func() {
		answers, _ := a.AskQuestions(ctx, questionItems("h1"), 0)
		done <- answers
	}()

	q1 := waitQuestionRequestEvent(t, a)
	if !a.SupersedeQuestion(q1.RequestID) {
		t.Fatal("supersede was not accepted")
	}
	if resolved := waitQuestionResolvedEvent(t, a, q1.RequestID); resolved.Reason != tools.QuestionOutcomeSuperseded {
		t.Fatalf("resolved reason = %q, want superseded", resolved.Reason)
	}

	answers := <-done
	if len(answers) != 1 || answers[0].Outcome != tools.QuestionOutcomeSuperseded {
		t.Fatalf("answers = %#v, want one superseded", answers)
	}
}

// TestAskQuestionsSupersedeAfterUserMessageEnqueued pins the headless
// supersede order at the agent level: the new user message is accepted (queued
// on the event bus, sequenced) before the blocked Question is woken, so
// whatever the resumed tool does next is ordered after it. The headless
// handler test only pins the backend call order on a mock; this test pins the
// underlying scheduling invariant on a real MainAgent.
func TestAskQuestionsSupersedeAfterUserMessageEnqueued(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	ctx := a.turn.Ctx

	done := make(chan []tools.QuestionAnswer, 1)
	go func() {
		answers, _ := a.AskQuestions(ctx, questionItems("h1"), 0)
		done <- answers
	}()

	q1 := waitQuestionRequestEvent(t, a)

	// Mirror the headless handler: accept the new message first, then wake.
	a.SendUserMessage("skip the question")
	select {
	case evt := <-a.eventCh:
		if evt.Type != EventUserMessage {
			t.Fatalf("queued event type = %q, want %q", evt.Type, EventUserMessage)
		}
		if evt.Seq == 0 {
			t.Fatal("the user message must be sequenced when queued, so later work orders after it")
		}
		// Hand it back: the loop (not started in tests) still owns it.
		a.eventCh <- evt
	default:
		t.Fatal("the user message must be queued before the blocked question is woken")
	}

	if !a.SupersedeQuestion(q1.RequestID) {
		t.Fatal("supersede was not accepted")
	}
	if resolved := waitQuestionResolvedEvent(t, a, q1.RequestID); resolved.Reason != tools.QuestionOutcomeSuperseded {
		t.Fatalf("resolved reason = %q, want superseded", resolved.Reason)
	}
	if answers := <-done; len(answers) != 1 || answers[0].Outcome != tools.QuestionOutcomeSuperseded {
		t.Fatalf("answers = %#v, want one superseded", answers)
	}
	// The queued message survives the supersede: it is still pending for the
	// loop to consume as the next input.
	select {
	case evt := <-a.eventCh:
		if evt.Type != EventUserMessage {
			t.Fatalf("pending event type = %q, want %q", evt.Type, EventUserMessage)
		}
	default:
		t.Fatal("supersede must not drop the queued user message")
	}
}

// TestAskQuestionsSendTimeoutLeavesNoTrace verifies congestion handling: a
// request that cannot reach the output channel before its deadline fails as a
// send timeout, leaves no pending state, and emits neither event — the client
// never saw the question, so there is nothing to resolve.
func TestAskQuestionsSendTimeoutLeavesNoTrace(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()

	// Tests never start Run, so nothing drains outputCh. Filling it through the
	// best-effort emit path keeps the fill itself non-blocking while the next
	// interactive send still has to wait for space that never comes.
	for len(a.outputCh) < cap(a.outputCh) {
		a.emitToTUI(NotificationEvent{Message: "filler"})
	}

	answers, err := a.AskQuestions(a.turn.Ctx, questionItems("h1"), 30*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "question request send timed out") {
		t.Fatalf("AskQuestions err = %v, want a send timeout", err)
	}
	if answers != nil {
		t.Fatalf("answers = %#v, want nil when the request never reached the client", answers)
	}
	if a.interaction.hasPendingUserInteraction() {
		t.Fatal("a request whose send failed must not stay pending")
	}
	for drained := false; !drained; {
		select {
		case e := <-a.Events():
			if resolved, ok := e.(QuestionResolvedEvent); ok {
				t.Fatalf("resolved event %+v emitted for a request that was never sent", resolved)
			}
		default:
			drained = true
		}
	}
}

// TestAskQuestionsResolvesBeforeNextRequest verifies the event order clients
// depend on: the answered request is closed before the next one is published,
// so at most one question dialog is open at a time.
func TestAskQuestionsResolvesBeforeNextRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()

	done := make(chan []tools.QuestionAnswer, 1)
	go func() {
		answers, _ := a.AskQuestions(a.turn.Ctx, questionItems("h1", "h2"), 0)
		done <- answers
	}()

	first := waitQuestionRequestEvent(t, a)
	if _, accepted := a.ResolveQuestion([]string{"yes"}, tools.QuestionOutcomeAnswered, first.RequestID); !accepted {
		t.Fatal("first answer was not accepted")
	}

	order := make([]string, 0, 2)
	var nextRequestID string
	deadline := time.After(3 * time.Second)
	for len(order) < 2 {
		select {
		case e := <-a.Events():
			switch ev := e.(type) {
			case QuestionResolvedEvent:
				if ev.RequestID == first.RequestID {
					order = append(order, "resolved")
				}
			case QuestionRequestEvent:
				if ev.Header != "h2" {
					t.Fatalf("next request header = %q, want h2", ev.Header)
				}
				nextRequestID = ev.RequestID
				order = append(order, "request")
			}
		case <-deadline:
			t.Fatalf("question events = %v, want the resolution and the next request", order)
		}
	}
	if order[0] != "resolved" || order[1] != "request" {
		t.Fatalf("event order = %v, want the answered request resolved first", order)
	}

	if _, accepted := a.ResolveQuestion([]string{"yes"}, tools.QuestionOutcomeAnswered, nextRequestID); !accepted {
		t.Fatal("second answer was not accepted")
	}
	if answers := <-done; len(answers) != 2 || answers[1].Outcome != tools.QuestionOutcomeAnswered || answers[1].Selected[0] != "yes" {
		t.Fatalf("answers = %#v, want both questions answered", answers)
	}
}

// TestAskQuestionsSecondBatchCancelsWhileFirstWaits verifies batch admission is
// cancellable: a cancelled second batch returns without waiting for an
// unbounded first one, and the first keeps ownership of the slot.
func TestAskQuestionsSecondBatchCancelsWhileFirstWaits(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	first := make(chan []tools.QuestionAnswer, 1)
	go func() {
		answers, _ := a.AskQuestions(a.turn.Ctx, questionItems("h1"), 0)
		first <- answers
	}()
	q1 := waitQuestionRequestEvent(t, a)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.AskQuestions(cancelled, questionItems("h2"), 0); err == nil {
		t.Fatal("a cancelled second batch must not block behind the first")
	}

	a.SupersedeQuestion(q1.RequestID)
	if answers := <-first; len(answers) != 1 || answers[0].Outcome != tools.QuestionOutcomeSuperseded {
		t.Fatalf("first batch answers = %#v, want one superseded", answers)
	}
}
