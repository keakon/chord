package agent

import (
	"errors"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestSessionPauseRejectsExistingQuestionWaitWithoutChangingIt(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{identity.MainAgentID, "sample-worker"} {
		t.Run(owner, func(t *testing.T) {
			a := newQuestionTestAgent(t)
			created := runQuestionCommand(t, a, &questionCommand{
				operation: QuestionOperation{Operation: questionOpCreate, OperationID: "create"},
				call:      "question-call", owner: owner, task: owner,
				args: tools.QuestionArgs{Questions: questionItems("Choice"), Wait: new(true)},
			})
			if !created.Accepted {
				t.Fatalf("question creation failed: %+v", created)
			}
			wait := &questionCommand{
				operation: QuestionOperation{Operation: questionOpWait, OperationID: "wait"},
				call:      "question-call", owner: owner, task: owner, scope: a.questions.scope,
				args: tools.QuestionArgs{WaitFor: created.Result.QuestionIDs}, reply: make(chan QuestionReceipt, 1),
			}
			a.installQuestionWait(wait)
			cmd := &sessionPauseCommand{req: SessionPauseRequest{Identity: testPauseIdentity(a)}, result: make(chan error, 1)}
			a.prepareSessionPause(cmd)
			if err := <-cmd.result; err == nil {
				t.Fatal("takeover accepted while a synchronous question was waiting")
			} else if pauseErr, ok := errors.AsType[*recovery.SessionPauseError](err); !ok || pauseErr.Code != "interaction_blocked" {
				t.Fatalf("takeover error=%v", err)
			}
			if cmd.admitted || a.sessionExecutionPaused() || a.pause.inputClosed || len(a.questions.waits) != 1 {
				t.Fatal("rejected takeover changed execution, admission, or question wait")
			}
			select {
			case <-wait.reply:
				t.Fatal("rejected takeover resolved the existing question wait")
			default:
			}
		})
	}
}

func TestSessionPauseAllowsAsyncQuestionWithoutWaiter(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	createTestQuestions(t, a, questionItems("Choice"), false)
	if a.hasPauseBlockingInteraction() {
		t.Fatal("restorable asynchronous question blocked takeover")
	}
}
