package agent

import (
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// Durable rows, rather than a model's runtime cycle, own the two notice slots.
// A model switch can reuse a still-valid row without appending another card.
// A row the armed withdrawal selects is omitted from requests while its
// durable removal waits for an idle boundary, so it no longer counts as
// present: a re-crossing before that sweep must stage a fresh notice instead
// of leaving one request with neither the omitted row nor a fresh one.
func (a *MainAgent) hasDurablePressureNotice(level string) bool {
	if a == nil || a.ctxMgr == nil {
		return false
	}
	slot := pressureNoticeSlot(level)
	if slot < 0 {
		return false
	}
	withdrawing := a.contextNoticesStale.Load() &&
		pressureSlotWithdrawn(slot, a.contextNoticeWithdrawalScope.Load())
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Kind != message.KindContextNotice || pressureNoticeSlot(msg.NoticeLevel) != slot {
			continue
		}
		if withdrawing {
			continue
		}
		return true
	}
	return false
}

// Re-evaluate notices only across a model switch or a changed prefix before
// an existing notice. Changes after a notice cannot invalidate its cache.
func (a *MainAgent) reconcilePressureNoticesForModel(messages, prepared []message.Message, modelRef string, inputBudget int) []message.Message {
	if a == nil || a.ctxMgr == nil || modelRef == "" || inputBudget <= 0 {
		return a.omitStaleContextNoticesFromRequest(messages)
	}
	a.overlayClaims.mu.Lock()
	previous := a.overlayClaims.noticeModel
	previousPrefix := a.overlayClaims.noticePrefixSource
	prefixKnown := a.overlayClaims.noticePrefixesKnown
	a.overlayClaims.mu.Unlock()
	changedPrefix := false
	var changedSlots [pressureNoticeSlotCount]bool
	if prefixKnown {
		for _, msg := range prepared {
			slot := pressureNoticeSlot(msg.NoticeLevel)
			if msg.Kind == message.KindContextNotice && slot >= 0 && pressureNoticePrefixChanged(previousPrefix, prepared, slot) {
				changedPrefix = true
				changedSlots[slot] = true
			}
		}
	}
	if previous == modelRef && !changedPrefix {
		return a.omitStaleContextNoticesFromRequest(messages)
	}
	if previous == "" && !containsContextNotice(messages) && a.ctxMgr.LastTotalContextTokens() == 0 {
		return messages
	}
	// Reduction makes the previous usage obsolete. A model switch alone can
	// reuse that observation as an estimate against the target model's budget.
	pressure, compaction := a.pressureNoticeValidity(messages, modelRef, inputBudget, !changedPrefix)
	if previous == modelRef {
		// A reduction between the two notices may retire the upper one, but must
		// not rewrite the lower notice whose prefix is still cacheable.
		for _, msg := range messages {
			if msg.Kind != message.KindContextNotice {
				continue
			}
			switch pressureNoticeSlot(msg.NoticeLevel) {
			case 0:
				if !changedSlots[0] {
					pressure = true
				}
			case 1:
				if !changedSlots[1] {
					compaction = true
				}
			}
		}
	}
	a.clearPendingContextNotices()
	a.resetContextNotices()
	a.setPressureNoticeValidity(pressure, compaction)
	messages = a.omitStaleContextNoticesFromRequest(messages)
	if pressure || compaction {
		key := a.currentOverlayWindowKey()
		key.modelRef = modelRef
		a.notePressureStage(pressureStageReminded, key)
	}
	if pressure {
		a.stageContextNotice(contextNoticePressure, buildContextPressureReminderText())
	}
	if compaction {
		a.stageContextNotice(contextNoticeWarning, compactionThresholdNoticeText)
	}
	return messages
}

func (a *MainAgent) pressureNoticeValidity(messages []message.Message, modelRef string, inputBudget int, preferUsage bool) (pressure, compaction bool) {
	threshold := a.effectiveCompactionThreshold(modelRef)
	reminder := a.effectiveReminderPctForModelRef(modelRef, threshold)
	usable := max(inputBudget-a.effectiveCompactionReservedInput(), 0)
	if !a.compactContextVisible() || threshold <= 0 || usable <= 0 {
		return false, false
	}
	a.llmMu.RLock()
	prompt := a.installedSysPrompt
	a.llmMu.RUnlock()
	tokens := 0
	if preferUsage {
		tokens = a.ctxMgr.LastTotalContextTokens()
	}
	if tokens <= 0 {
		tokens = llm.EstimateRequestInputTokens(prompt, messages, a.mainLLMToolDefinitions())
	}
	return reminder > 0 && reminder < threshold && float64(tokens) >= reminder*float64(usable),
		float64(tokens) >= threshold*float64(usable)
}

// A fallback is still the same in-flight round: filter invalid signals before
// the target sees them, without changing compaction's durable source boundary.
// Missing applicable notices are appended for the target, reusing durable rows
// when available and persisting newly crossed thresholds through the same
// dispatch path as the primary request.
func (a *MainAgent) reconcileFallbackPressureNotices(messages []message.Message, modelRef string, inputBudget int, preferUsage bool) []message.Message {
	if a == nil || a.ctxMgr == nil || inputBudget <= 0 {
		return messages
	}
	pressure, compaction := a.pressureNoticeValidity(messages, modelRef, inputBudget, preferUsage)
	a.setPressureNoticeValidity(pressure, compaction)
	var out []message.Message
	var present pressureNoticeRecord
	for i, msg := range messages {
		slot := -1
		if msg.Kind == message.KindContextNotice || msg.Kind == message.KindTurnOverlay {
			slot = pressureNoticeSlot(msg.NoticeLevel)
		}
		if slot == 0 && !pressure || slot == 1 && !compaction {
			if out == nil {
				out = make([]message.Message, 0, len(messages))
				out = append(out, messages[:i]...)
			}
			continue
		}
		if slot >= 0 {
			present[slot] = true
		}
		if out != nil {
			out = append(out, msg)
		}
	}
	if out != nil {
		messages = out
	}
	valid := pressureNoticeRecord{pressure, compaction}
	var appended bool
	for slot, keep := range valid {
		if !keep || present[slot] {
			continue
		}
		var notice message.Message
		for _, row := range a.ctxMgr.Snapshot() {
			if row.Kind == message.KindContextNotice && pressureNoticeSlot(row.NoticeLevel) == slot {
				notice = row
				break
			}
		}
		if notice.Kind == "" {
			level, text := contextNoticePressure, buildContextPressureReminderText()
			if slot == 1 {
				level, text = contextNoticeWarning, compactionThresholdNoticeText
			}
			key := a.currentOverlayWindowKey()
			key.modelRef = modelRef
			a.notePressureStage(pressureStageReminded, key)
			if slot == 0 {
				a.noteContextPressureReminderAttached()
			} else {
				a.noteCompactionWarningAttached()
			}
			a.stashContextNotice(level, text)
			notice = message.Message{Role: message.RoleUser, NoticeLevel: level, Content: "<system-reminder>\n" + text + "\n</system-reminder>"}
			appended = true
		}
		// A model switch permits reselecting notices. Reusing a durable notice
		// at the request tail avoids rebuilding unrelated input.
		notice.Kind = message.KindTurnOverlay
		messages = append(messages, notice)
	}
	if appended {
		a.markOverlayClaimsDelivered()
	}
	return messages
}

// Compare only the surface preceding the selected threshold, ignoring notice
// rows themselves. When the previous request first delivered this threshold,
// its durable row was appended at the prepared history's end.
func pressureNoticePrefixChanged(previous, current []message.Message, slot int) bool {
	prefix := func(messages []message.Message) []message.Message {
		for i, msg := range messages {
			if msg.Kind == message.KindContextNotice && pressureNoticeSlot(msg.NoticeLevel) == slot {
				return messages[:i]
			}
		}
		return messages
	}
	previous, current = prefix(previous), prefix(current)
	i, j := 0, 0
	for {
		for i < len(previous) && previous[i].Kind == message.KindContextNotice {
			i++
		}
		for j < len(current) && current[j].Kind == message.KindContextNotice {
			j++
		}
		if i == len(previous) || j == len(current) {
			return i != len(previous) || j != len(current)
		}
		if !stableReductionMessageEquivalent(&previous[i], &current[j]) {
			return true
		}
		i++
		j++
	}
}

// Only dispatched requests establish a comparison baseline; a cancelled
// preparation must not pretend its reduced prefix reached the provider.
// Reuse the request-shape snapshot helper so an unchanged prefix is neither
// rehashed nor deep-copied on each request.
func (a *MainAgent) rememberPressureNoticeRequest(messages []message.Message, modelRef string) {
	a.overlayClaims.mu.Lock()
	previous := a.overlayClaims.noticePrefixSource
	a.overlayClaims.mu.Unlock()
	source := cowRequestShapeSlice(previous, messages)
	a.overlayClaims.mu.Lock()
	a.overlayClaims.noticeModel = modelRef
	a.overlayClaims.noticePrefixSource = source
	a.overlayClaims.noticePrefixesKnown = true
	a.overlayClaims.mu.Unlock()
}

// Immediate filtering and deferred durable cleanup share the same selection.
// No transcript indices move while an active compaction still references them.
func (a *MainAgent) setPressureNoticeValidity(pressure, compaction bool) {
	if pressure && compaction {
		a.disarmContextNoticeCleanup()
		return
	}
	scope := contextNoticeWithdrawAll
	if pressure {
		scope = contextNoticeWithdrawCompaction
	} else if compaction {
		scope = contextNoticeWithdrawPressure
	}
	a.contextNoticeWithdrawalScope.Store(scope)
	a.contextNoticesStale.Store(a.contextNoticesPersisted.Load())
}
