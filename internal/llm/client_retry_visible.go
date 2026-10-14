package llm

import (
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

type visibleStreamTracker struct {
	inner            StreamCallback
	visible          bool
	toolStreamStated bool   // a tool-call delta reached the UI in the current attempt
	textStated       bool   // a non-blank text delta reached the UI; only this is resumable body text
	pendingRollback  string // non-empty: a prior attempt's partial output is still rendered; its rollback must precede this attempt's first visible delta
	rollbackFired    bool   // the armed pendingRollback was emitted to the UI
	onVisibleStart   func() // called each time a visible streaming attempt begins; may be nil
	providerModel    string
	keyLogID         string
	keyAttempt       int
	keyCount         int
}

func (t *visibleStreamTracker) Callback(delta message.StreamDelta) {
	if t == nil {
		return
	}
	t.emitPendingRollbackBefore(delta)
	switch delta.Type {
	case message.StreamDeltaText, message.StreamDeltaThinking, message.StreamDeltaToolUseStart, message.StreamDeltaToolUseDelta, message.StreamDeltaToolUseEnd:
		switch delta.Type {
		case message.StreamDeltaToolUseStart, message.StreamDeltaToolUseDelta, message.StreamDeltaToolUseEnd:
			t.toolStreamStated = true
		case message.StreamDeltaText:
			// Only body text can be resumed by a continuation prompt. Thinking
			// deltas are visible but never reach the turn's partial-text
			// accumulator, so an attempt that streamed nothing but reasoning
			// has no resumable reply.
			if strings.TrimSpace(delta.Text) != "" {
				t.textStated = true
			}
		}
		if !t.visible {
			log.Debugf("LLM first visible stream delta delta_type=%v model=%v key_id=%v key_attempt=%v key_total=%v", delta.Type, t.providerModel, t.keyLogID, t.keyAttempt, t.keyCount)
			t.visible = true
			if t.onVisibleStart != nil {
				t.onVisibleStart()
			}
		}
	case message.StreamDeltaRollback:
		if t.visible {
			reason := ""
			if delta.Rollback != nil {
				reason = delta.Rollback.Reason
			}
			log.Debugf("LLM visible stream rollback model=%v key_id=%v key_attempt=%v key_total=%v reason=%v", t.providerModel, t.keyLogID, t.keyAttempt, t.keyCount, reason)
		}
		t.visible = false
		t.toolStreamStated = false
		t.textStated = false
	}
	if t.inner != nil {
		t.inner(delta)
	}
}

// emitPendingRollbackBefore clears a prior attempt's preserved partial output
// at the moment this attempt starts producing visible content: the retry
// regenerates the response from the beginning, so the preserved text must
// leave the screen (and the turn's partial-text accumulator) exactly when its
// replacement starts arriving — otherwise the card concatenates both attempts
// and diverges from the session record. Emitting the rollback lazily (instead
// of at interruption time) keeps the preserved text on screen during the retry
// wait, so there is no intermediate blank card.
func (t *visibleStreamTracker) emitPendingRollbackBefore(delta message.StreamDelta) {
	if t.pendingRollback == "" || t.inner == nil {
		return
	}
	switch delta.Type {
	case message.StreamDeltaText, message.StreamDeltaThinking,
		message.StreamDeltaToolUseStart, message.StreamDeltaToolUseDelta, message.StreamDeltaToolUseEnd:
		t.inner(message.StreamDelta{
			Type: message.StreamDeltaRollback,
			Rollback: &message.RollbackDelta{
				Reason: t.pendingRollback,
			},
		})
		t.pendingRollback = ""
		t.rollbackFired = true
	}
}

// EmitRollback tells the UI to discard what the current attempt already
// rendered and reports whether a rollback was actually emitted.
func (t *visibleStreamTracker) EmitRollback(reason string) bool {
	if t == nil || !t.visible || t.inner == nil {
		return false
	}
	t.inner(message.StreamDelta{
		Type: message.StreamDeltaRollback,
		Rollback: &message.RollbackDelta{
			Reason: reason,
		},
	})
	t.visible = false
	t.pendingRollback = ""
	return true
}

// MarkInterruptedVisibleOutput closes the current visible attempt without
// telling the UI to discard what it already rendered. It reports false when the
// attempt cannot be preserved — a tool-call delta already reached the UI, and a
// retry would emit a second tool card for the same call, so the caller must roll
// back instead.
func (t *visibleStreamTracker) MarkInterruptedVisibleOutput() bool {
	if t == nil || !t.visible {
		return true
	}
	if t.toolStreamStated {
		return false
	}
	log.Debugf("LLM visible stream interrupted; keeping partial output model=%v key_id=%v key_attempt=%v key_total=%v", t.providerModel, t.keyLogID, t.keyAttempt, t.keyCount)
	t.visible = false
	t.toolStreamStated = false
	return true
}

// HadTextDelta reports whether the current attempt streamed non-blank body
// text, which is the only kind of output a continuation prompt can resume.
// Thinking-only attempts are visible but never reach the caller's
// partial-text accumulator, so they must keep the ordinary silent retry.
// Unlike visible and toolStreamStated, textStated survives
// MarkInterruptedVisibleOutput: callers close the attempt first and then ask
// whether what it left behind is resumable.
func (t *visibleStreamTracker) HadTextDelta() bool {
	return t != nil && t.textStated
}

func newStreamAttemptTracker(cb StreamCallback, target streamRetryTarget, apiKey, modelRef, attemptReason string, keyAttempt, keyCount int) *visibleStreamTracker {
	keyLogIDValue := keyLogID(apiKey)
	return &visibleStreamTracker{
		inner:         cb,
		providerModel: modelRef,
		keyLogID:      keyLogIDValue,
		keyAttempt:    keyAttempt + 1,
		keyCount:      keyCount,
		onVisibleStart: func() {
			target.provider.MarkKeySuccess(apiKey)
			log.Debugf("LLM emitting streaming status after visible output provider=%v model=%v key_id=%v key_attempt=%v key_total=%v", target.provider.Name(), modelRef, keyLogIDValue, keyAttempt+1, keyCount)
			if cb == nil {
				return
			}
			emitStreamStatus(cb, message.StatusDeltaStreaming, "")
			// key_confirmed must carry the effective model ref so the agent/UI can
			// confirm routing decisions (fallback/key switch) only after the model
			// actually begins emitting visible output.
			cb(message.StreamDelta{
				Type: "key_confirmed",
				Status: &message.StatusDelta{
					ModelRef: modelRef,
					Reason:   attemptReason,
				},
			})
		},
	}
}
