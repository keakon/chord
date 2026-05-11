package agent

import (
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
)

const (
	defaultStreamTextFlushInterval     = 20 * time.Millisecond
	defaultStreamThinkingFlushInterval = 150 * time.Millisecond
)

type streamContentCommitMode int

const (
	streamContentCommitEmpty streamContentCommitMode = iota
	streamContentCommitFullText
)

type streamContentReducer struct {
	agentID string
	emit    func(AgentEvent)

	appendPartialText func(string)

	scrubThinkingDelta bool
	scrubThinkingFinal bool

	emitThinkingStarted     bool
	ignoreThinkingAfterText bool
	closeThinkingOnText     bool
	closeThinkingOnFinish   bool
	thinkingCommitMode      streamContentCommitMode

	textFlushInterval     time.Duration
	thinkingFlushInterval time.Duration

	textAccum    strings.Builder
	textLastEmit time.Time

	thinkingAccum    strings.Builder
	thinkingActive   bool
	thinkingLastEmit time.Time

	responseTextStarted bool
}

func (r *streamContentReducer) Handle(delta message.StreamDelta) bool {
	if r == nil {
		return false
	}
	switch delta.Type {
	case "text":
		r.handleText(delta.Text)
		return true
	case "thinking":
		r.handleThinking(delta.Text)
		return true
	case "thinking_end":
		r.closeThinkingBlock()
		return true
	default:
		return false
	}
}

func (r *streamContentReducer) Finish() {
	if r == nil {
		return
	}
	if r.closeThinkingOnFinish {
		r.closeThinkingBlock()
	}
	r.flushTextDelta()
}

func (r *streamContentReducer) handleText(text string) {
	if r.closeThinkingOnText {
		r.closeThinkingBlock()
	}
	r.responseTextStarted = true
	r.textAccum.WriteString(text)
	if r.appendPartialText != nil {
		r.appendPartialText(text)
	}
	if r.textLastEmit.IsZero() {
		// Emit the first delta immediately for perceived responsiveness.
		r.flushTextDelta()
		return
	}
	interval := r.textFlushInterval
	if interval <= 0 {
		interval = defaultStreamTextFlushInterval
	}
	if time.Since(r.textLastEmit) >= interval {
		r.flushTextDelta()
	}
}

func (r *streamContentReducer) handleThinking(text string) {
	if r.ignoreThinkingAfterText && r.responseTextStarted {
		return
	}
	r.flushTextDelta()
	if !r.thinkingActive {
		r.thinkingActive = true
		if r.emitThinkingStarted && r.emit != nil {
			r.emit(ThinkingStartedEvent{})
		}
		r.thinkingLastEmit = time.Now()
	}
	r.thinkingAccum.WriteString(text)
	if r.thinkingFlushInterval <= 0 {
		// SubAgent historically forwards thinking deltas immediately while still
		// retaining the full accumulated block for thinking_end.
		r.emitThinkingDelta(text, r.scrubThinkingDelta)
		return
	}
	if time.Since(r.thinkingLastEmit) >= r.thinkingFlushInterval {
		r.flushThinkingDelta()
	}
}

func (r *streamContentReducer) flushTextDelta() {
	if r == nil {
		return
	}
	if r.textAccum.Len() > 0 {
		text := r.textAccum.String()
		r.textAccum.Reset()
		if r.emit != nil {
			r.emit(StreamTextEvent{Text: text, AgentID: r.agentID})
		}
	}
	r.textLastEmit = time.Now()
}

func (r *streamContentReducer) flushThinkingDelta() {
	if r == nil {
		return
	}
	if r.thinkingAccum.Len() > 0 {
		delta := r.thinkingAccum.String()
		r.thinkingAccum.Reset()
		r.emitThinkingDelta(delta, r.scrubThinkingDelta)
	}
	r.thinkingLastEmit = time.Now()
}

func (r *streamContentReducer) emitThinkingDelta(text string, scrub bool) {
	if r == nil || r.emit == nil {
		return
	}
	if scrub {
		text = scrubThinkingToolcallMarkers(text)
	}
	if strings.TrimSpace(text) != "" {
		r.emit(StreamThinkingDeltaEvent{Text: text, AgentID: r.agentID})
	}
}

func (r *streamContentReducer) closeThinkingBlock() {
	if r == nil {
		return
	}
	if !r.thinkingActive && r.thinkingAccum.Len() == 0 {
		return
	}
	var finalText string
	switch r.thinkingCommitMode {
	case streamContentCommitFullText:
		finalText = r.thinkingAccum.String()
		if r.scrubThinkingFinal {
			finalText = scrubThinkingToolcallMarkers(finalText)
		}
		if strings.TrimSpace(finalText) == "" {
			r.thinkingAccum.Reset()
			r.thinkingActive = false
			return
		}
	default:
		r.flushThinkingDelta()
	}
	r.thinkingAccum.Reset()
	r.thinkingActive = false
	if r.emit != nil {
		r.emit(StreamThinkingEvent{Text: finalText, AgentID: r.agentID})
	}
}

type llmStreamReducer struct {
	tool    streamToolDeltaReducer
	content streamContentReducer

	emitActivity             func(ActivityType, string)
	promoteStreamingActivity func(string)

	onProgress       func(*message.StreamProgressDelta)
	beforeStatus     func(*message.StatusDelta)
	onRateLimits     func(message.StreamDelta)
	onKeySwitched    func()
	onKeyDeactivated func(email, accountID string)
	onKeyExpired     func(email, accountID string)
	onKeyConfirmed   func(*message.StatusDelta)
	onError          func(text string)
}

func (r *llmStreamReducer) Handle(delta message.StreamDelta) {
	if r == nil {
		return
	}
	if delta.Progress != nil && r.onProgress != nil {
		r.onProgress(delta.Progress)
	}
	if r.tool.Handle(delta) {
		return
	}
	if r.content.Handle(delta) {
		return
	}
	switch delta.Type {
	case "status":
		if delta.Status == nil {
			return
		}
		if r.beforeStatus != nil {
			r.beforeStatus(delta.Status)
		}
		if delta.Status.Type == string(ActivityStreaming) {
			if r.promoteStreamingActivity != nil {
				r.promoteStreamingActivity("llm_status")
			}
			return
		}
		if r.emitActivity != nil {
			r.emitActivity(ActivityType(delta.Status.Type), delta.Status.Detail)
		}
	case "rate_limits":
		if r.onRateLimits != nil {
			r.onRateLimits(delta)
		}
	case "key_switched":
		if r.onKeySwitched != nil {
			r.onKeySwitched()
		}
	case "key_deactivated":
		if r.onKeyDeactivated != nil {
			r.onKeyDeactivated(delta.Email, delta.AccountID)
		}
	case "key_expired":
		if r.onKeyExpired != nil {
			r.onKeyExpired(delta.Email, delta.AccountID)
		}
	case "key_confirmed":
		if r.onKeyConfirmed != nil {
			r.onKeyConfirmed(delta.Status)
		}
	case "error":
		if r.onError != nil {
			r.onError(delta.Text)
		}
	}
}

func (r *llmStreamReducer) Finish() {
	if r == nil {
		return
	}
	r.content.Finish()
}

func streamKeyIdentity(email, accountID string) string {
	switch {
	case email != "" && accountID != "":
		return email + " (" + accountID + ")"
	case email != "":
		return email
	case accountID != "":
		return accountID
	default:
		return "unknown"
	}
}
