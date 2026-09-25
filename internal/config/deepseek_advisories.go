package config

import (
	"fmt"
	"maps"
	"slices"

	"github.com/keakon/chord/internal/modelcompat"
)

// deepSeekContractAdvisories reports explicit continuity settings overridden by
// the effective DeepSeek contract. An unset setting needs no user action.
func deepSeekContractAdvisories(providerName string, cfg ProviderConfig) []string {
	wire := EffectiveProviderType(cfg)
	var advisories []string
	for _, modelID := range slices.Sorted(maps.Keys(cfg.Models)) {
		model := cfg.Models[modelID]
		contract := routeReasoningContract(cfg, model)
		if wire != ProviderTypeChatCompletions && wire != ProviderTypeMessages {
			if contract == ReasoningContractDeepSeek && wire != "" {
				advisories = append(advisories, fmt.Sprintf("provider %q model %q sets reasoning_continuity contract %q on %s, where it has no effect; this contract applies only to chat-completions and messages routes", providerName, modelID, contract, wire))
			}
			continue
		}
		if contract != ReasoningContractDeepSeek &&
			(contract != "" || modelcompat.ModelNativeFamily(modelID) != modelcompat.NativeFamilyDeepSeek) {
			continue
		}
		mode := modelReasoningContinuity(model).EffectiveMode()
		if mode == "" {
			mode = providerReasoningContinuity(cfg).EffectiveMode()
		}
		wantMode := ReasoningContinuityModeOpenAIVisible
		if wire == ProviderTypeMessages {
			wantMode = ReasoningContinuityModeAnthropicUnsigned
		}
		if mode != "" && mode != wantMode {
			advisories = append(advisories, fmt.Sprintf("provider %q model %q sets reasoning_continuity mode %q, but the DeepSeek contract requires %s; remove the mode setting, or use contract: none only if the backend does not require the DeepSeek contract", providerName, modelID, mode, wantMode))
		}
		replay := modelReasoningContinuity(model).ReasoningReplayValue()
		if replay == "" {
			replay = providerReasoningContinuity(cfg).ReasoningReplayValue()
		}
		if replay != ReasoningReplayCurrentTurn && replay != ReasoningReplayNone {
			continue
		}
		advisories = append(advisories, fmt.Sprintf("provider %q model %q sets reasoning_replay %q, but the DeepSeek contract always replays the full retained reasoning history; remove the setting or set reasoning_replay: %s",
			providerName, modelID, replay, ReasoningReplayAll))
	}
	return advisories
}
