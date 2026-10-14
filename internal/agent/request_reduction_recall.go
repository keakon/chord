package agent

import (
	"maps"
	"strings"
)

// reductionRecallProtectMaxKeys bounds the per-session recall-protection set so
// a pathological session cannot grow it without limit. Beyond the cap new
// recall evidence is dropped; existing protections persist.
const reductionRecallProtectMaxKeys = 512

// recordDiscardedInputEvidence bounds the session-scoped evidence copied into
// every reduction pass. Once the cap is reached, existing evidence remains
// useful while new entries are conservatively omitted rather than allowing a
// long session's unique reads/searches to make each request grow without
// bound.
func recordDiscardedInputEvidence(evidence map[string]string, key, callID string) {
	if evidence == nil || strings.TrimSpace(key) == "" {
		return
	}
	if _, exists := evidence[key]; !exists && len(evidence) >= reductionRecallProtectMaxKeys {
		return
	}
	evidence[key] = callID
}

// noteRecalledReductionInput records that the output of this tool input was
// reduced on an earlier request and the model re-issued the identical call —
// direct evidence that the reduction discarded content the model still needed.
// The newest output of a recalled input is exempt from reduction for the rest
// of the session.
func (a *MainAgent) noteRecalledReductionInput(key string) {
	if a == nil || key == "" {
		return
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if a.recalledReductionInputs == nil {
		a.recalledReductionInputs = make(map[string]struct{})
	}
	if len(a.recalledReductionInputs) >= reductionRecallProtectMaxKeys {
		if _, ok := a.recalledReductionInputs[key]; !ok {
			return
		}
	}
	a.recalledReductionInputs[key] = struct{}{}
}

// lastPreparedDiscardedInputsSnapshot clones the session's discarded-input
// evidence set for a reduction pass. The set is monotonic session state —
// input keys whose newest output was genuinely summarized away (never
// repeated-collapse, which always leaves a fresher full copy in context) —
// and is dropped with the visible reduction caches on restore or model switch.
func (a *MainAgent) lastPreparedDiscardedInputsSnapshot() map[string]string {
	out := make(map[string]string)
	if a == nil {
		return out
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	maps.Copy(out, a.lastPreparedLLMDiscardedInputs)
	return out
}

func (a *MainAgent) recalledReductionInputsSnapshot() map[string]struct{} {
	if a == nil {
		return nil
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if len(a.recalledReductionInputs) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(a.recalledReductionInputs))
	for key := range a.recalledReductionInputs {
		out[key] = struct{}{}
	}
	return out
}
