package agent

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

type RuntimeModelPoolPolicy struct {
	mu sync.RWMutex

	currentModelPool string
	overrides        map[string]string

	lastPicked map[string]map[string]string
}

type modelPoolSelectionSnapshot struct {
	currentPool string
	overrides   map[string]string
}

func NewRuntimeModelPoolPolicy() *RuntimeModelPoolPolicy {
	return &RuntimeModelPoolPolicy{
		overrides:  make(map[string]string),
		lastPicked: make(map[string]map[string]string),
	}
}

func (p *RuntimeModelPoolPolicy) ReplaceSelections(currentModelPool string, overrides map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentModelPool = currentModelPool
	p.overrides = make(map[string]string, len(overrides))
	for agentName, pool := range overrides {
		agentName = strings.TrimSpace(agentName)
		if agentName == "" {
			continue
		}
		p.overrides[agentName] = strings.TrimSpace(pool)
	}
}

func (p *RuntimeModelPoolPolicy) ReplaceSelectionsForSessionRestore(currentModelPool string, overrides map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentModelPool = currentModelPool
	p.overrides = make(map[string]string, len(overrides))
	for agentName, pool := range overrides {
		agentName = strings.TrimSpace(agentName)
		if agentName == "" {
			continue
		}
		p.overrides[agentName] = strings.TrimSpace(pool)
	}
	p.lastPicked = make(map[string]map[string]string)
}

func (p *RuntimeModelPoolPolicy) SetCurrentModelPool(pool string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentModelPool = pool
}

func (p *RuntimeModelPoolPolicy) CurrentModelPool() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.currentModelPool
}

func (p *RuntimeModelPoolPolicy) SetAgentOverride(agentName, pool string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.overrides == nil {
		p.overrides = make(map[string]string)
	}
	p.overrides[agentName] = pool
}

func (p *RuntimeModelPoolPolicy) ClearAgentOverride(agentName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.overrides, agentName)
}

func (p *RuntimeModelPoolPolicy) AgentOverride(agentName string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool, ok := p.overrides[agentName]
	return pool, ok
}

func (p *RuntimeModelPoolPolicy) EffectivePool(agentName string, cfg *config.AgentConfig) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.effectivePoolLocked(agentName, cfg)
}

func (p *RuntimeModelPoolPolicy) effectivePoolLocked(agentName string, cfg *config.AgentConfig) string {
	if override, ok := p.overrides[agentName]; ok {
		if cfg != nil && cfg.HasPool(override) {
			return override
		}
	}

	// Current model pool is only applied to non-subagent roles.
	if cfg != nil && !cfg.IsSubAgent() && cfg.HasPool(p.currentModelPool) {
		return p.currentModelPool
	}

	if cfg != nil && len(cfg.Models) > 0 {
		firstPool := cfg.PoolNames()[0]
		if len(firstPool) > 0 {
			return firstPool
		}
	}

	return ""
}

func (p *RuntimeModelPoolPolicy) EffectiveModels(agentName string, cfg *config.AgentConfig) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.effectiveModelsLocked(agentName, cfg)
}

func (p *RuntimeModelPoolPolicy) effectiveModelsLocked(agentName string, cfg *config.AgentConfig) []string {
	if cfg == nil || len(cfg.Models) == 0 {
		return nil
	}

	pool := p.effectivePoolLocked(agentName, cfg)
	if pool != "" {
		return cfg.PoolModels(pool)
	}

	return nil
}

func (p *RuntimeModelPoolPolicy) SetLastPicked(roleName, poolName, modelRef string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastPicked == nil {
		p.lastPicked = make(map[string]map[string]string)
	}
	if p.lastPicked[roleName] == nil {
		p.lastPicked[roleName] = make(map[string]string)
	}
	p.lastPicked[roleName][poolName] = modelRef
}

func (p *RuntimeModelPoolPolicy) LastPicked(roleName, poolName string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.lastPicked == nil {
		return "", false
	}
	roleMap, ok := p.lastPicked[roleName]
	if !ok {
		return "", false
	}
	ref, ok := roleMap[poolName]
	return ref, ok
}

func (p *RuntimeModelPoolPolicy) ResolveInitialModelRef(agentName string, cfg *config.AgentConfig) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	models := p.effectiveModelsLocked(agentName, cfg)
	if len(models) == 0 {
		return ""
	}

	pool := p.effectivePoolLocked(agentName, cfg)
	if pool != "" {
		if picked, ok := p.lastPicked[agentName]; ok {
			if ref, ok := picked[pool]; ok {
				if slices.Contains(models, ref) {
					return ref
				}
			}
		}
	}

	ref := models[0]
	_, variant := config.ParseModelRef(ref)
	if variant == "" && cfg != nil && cfg.Variant != "" {
		ref = ref + "@" + cfg.Variant
	}
	return ref
}

func (p *RuntimeModelPoolPolicy) Overrides() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]string, len(p.overrides))
	maps.Copy(out, p.overrides)
	return out
}

func (a *MainAgent) effectiveSubAgentModels(agentDef *config.AgentConfig) []string {
	if a.modelPoolPolicy != nil && len(agentDef.Models) > 0 {
		return a.modelPoolPolicy.EffectiveModels(agentDef.Name, agentDef)
	}
	if len(agentDef.Models) > 0 {
		firstPool := agentDef.PoolNames()[0]
		if refs := agentDef.PoolModels(firstPool); len(refs) > 0 {
			return refs
		}
	}
	return nil
}

func (a *MainAgent) focusedAgentConfig() *config.AgentConfig {
	target := a.focusedAgentSnapshot()
	agentDefName := ""
	if target.sub != nil {
		agentDefName = target.sub.agentDefName
	} else if target.parked && target.task != nil {
		agentDefName = target.task.AgentDefName
	}
	if agentDefName != "" {
		a.stateMu.RLock()
		cfg := a.agentConfigs[agentDefName]
		a.stateMu.RUnlock()
		return cfg
	}
	return a.currentActiveConfig()
}

// CurrentPoolName returns the effective pool name for the agent currently shown
// in the TUI (focused SubAgent if any, else current main role), or "" if no pool
// policy is configured.
func (a *MainAgent) CurrentPoolName() string {
	if a.modelPoolPolicy == nil {
		return ""
	}
	cfg := a.focusedAgentConfig()
	if cfg == nil {
		return ""
	}
	return a.modelPoolPolicy.EffectivePool(cfg.Name, cfg)
}

func (a *MainAgent) MainModelPoolName() string {
	if a.modelPoolPolicy == nil {
		return ""
	}
	cfg := a.currentActiveConfig()
	if cfg == nil {
		return ""
	}
	return a.modelPoolPolicy.EffectivePool(cfg.Name, cfg)
}

func (a *MainAgent) MainModelPoolNames() []string {
	cfg := a.currentActiveConfig()
	if cfg == nil {
		return nil
	}
	return cfg.PoolNames()
}

func (a *MainAgent) PoolNames() []string {
	if a.modelPoolPolicy == nil {
		return nil
	}
	cfg := a.focusedAgentConfig()
	if cfg == nil {
		return nil
	}
	return cfg.PoolNames()
}

func (a *MainAgent) SetCurrentModelPool(pool string) error {
	target := a.focusedAgentSnapshot()
	if target.parked && target.task != nil {
		return a.SetAgentModelPool(target.task.AgentDefName, pool)
	}
	a.sendEvent(Event{Type: EventModelPoolSwitch, Payload: modelPoolSwitchRequest{Pool: pool}})
	return nil
}

func (a *MainAgent) SetAgentModelPool(agentName, pool string) error {
	a.sendEvent(Event{Type: EventModelPoolSwitch, Payload: modelPoolSwitchRequest{AgentName: agentName, Pool: pool}})
	return nil
}

type modelPoolSwitchRequest struct {
	AgentName string
	Pool      string
}

func (a *MainAgent) handleModelPoolSwitchEvent(evt Event) {
	req, ok := evt.Payload.(modelPoolSwitchRequest)
	if !ok {
		log.Errorf("handleModelPoolSwitchEvent: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	if strings.TrimSpace(req.AgentName) != "" {
		if err := a.setAgentModelPool(req.AgentName, req.Pool, true); err != nil {
			a.emitToTUI(ErrorEvent{Err: err})
		}
		return
	}
	if err := a.setCurrentModelPool(req.Pool, true); err != nil {
		a.emitToTUI(ErrorEvent{Err: err})
	}
}

func (a *MainAgent) markMainModelPoolSwitchPending() {
	a.pendingMainModelPoolSwitch = true
}

func (a *MainAgent) markAgentModelPoolSwitchPending(agentName string) {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return
	}
	if a.pendingAgentModelPoolSwitch == nil {
		a.pendingAgentModelPoolSwitch = make(map[string]struct{})
	}
	a.pendingAgentModelPoolSwitch[agentName] = struct{}{}
}

func (a *MainAgent) agentModelPoolSwitchInFlight(agentName string) bool {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return false
	}
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	for _, sub := range a.subs.subAgents {
		if sub == nil || sub.agentDefName != agentName {
			continue
		}
		if sub.llmRequestInFlight.Load() {
			return true
		}
	}
	return false
}

func (a *MainAgent) pendingAgentModelPoolSwitchInFlight(pendingAgents map[string]struct{}) bool {
	for agentName := range pendingAgents {
		if a.agentModelPoolSwitchInFlight(agentName) {
			return true
		}
	}
	return false
}

func (a *MainAgent) handleSubAgentRequestBoundary(_ Event) {
	a.applyPendingModelPoolSwitchesAtRequestBoundary()
}

func (a *MainAgent) capturePendingModelPoolRollback() {
	if a == nil || a.modelPoolPolicy == nil || a.pendingModelPoolRollback != nil {
		return
	}
	current, overrides := a.snapshotModelPoolState()
	a.pendingModelPoolRollback = &modelPoolSelectionSnapshot{
		currentPool: current,
		overrides:   cloneStringMap(overrides),
	}
}

func (a *MainAgent) restorePendingModelPoolRollback() {
	if a == nil || a.modelPoolPolicy == nil || a.pendingModelPoolRollback == nil {
		return
	}
	snap := a.pendingModelPoolRollback
	a.modelPoolPolicy.ReplaceSelections(snap.currentPool, snap.overrides)
	a.pendingModelPoolRollback = nil
	a.saveModelPoolState()
}

func (a *MainAgent) restoreFailedModelPoolSelections(mainFailed bool, failedAgents []string) {
	if a == nil || a.modelPoolPolicy == nil || a.pendingModelPoolRollback == nil {
		return
	}
	snap := a.pendingModelPoolRollback
	currentPool := a.modelPoolPolicy.CurrentModelPool()
	if mainFailed {
		currentPool = snap.currentPool
	}
	overrides := a.modelPoolPolicy.Overrides()
	for _, agentName := range failedAgents {
		agentName = strings.TrimSpace(agentName)
		if agentName == "" {
			continue
		}
		if pool, ok := snap.overrides[agentName]; ok {
			overrides[agentName] = pool
		} else {
			delete(overrides, agentName)
		}
	}
	a.modelPoolPolicy.ReplaceSelections(currentPool, overrides)
	a.saveModelPoolState()
}

func (a *MainAgent) applyPendingModelPoolSwitchesAtRequestBoundary() {
	if a == nil || a.modelPoolPolicy == nil || a.mainLLMRequestInFlight.Load() {
		return
	}
	pendingMain := a.pendingMainModelPoolSwitch
	pendingAgents := a.pendingAgentModelPoolSwitch
	if !pendingMain && len(pendingAgents) == 0 {
		return
	}
	if a.pendingAgentModelPoolSwitchInFlight(pendingAgents) {
		return
	}
	a.pendingMainModelPoolSwitch = false
	a.pendingAgentModelPoolSwitch = nil

	var applyErr error
	appliedAny := false
	mainChanged := false
	mainFailed := false
	var failedAgents []string
	if pendingMain {
		cfg := a.currentActiveConfig()
		if cfg != nil {
			effectivePool := a.modelPoolPolicy.EffectivePool(cfg.Name, cfg)
			if err := a.switchMainModelForPoolIfNeeded(cfg, effectivePool, ""); err != nil {
				mainFailed = true
				applyErr = errors.Join(applyErr, err)
			} else {
				appliedAny = true
				mainChanged = true
			}
		}
	}
	for agentName := range pendingAgents {
		cfg, ok := a.agentConfigs[agentName]
		if !ok {
			continue
		}
		pool := a.modelPoolPolicy.EffectivePool(agentName, cfg)
		if err := a.switchActiveSubAgentsForPoolIfNeeded(agentName, cfg, pool); err != nil {
			failedAgents = append(failedAgents, agentName)
			applyErr = errors.Join(applyErr, fmt.Errorf("agent %q: %w", agentName, err))
			continue
		}
		appliedAny = true
		a.notifySubAgentRoutingChanged(agentName, "agent_model_pool_changed")
	}
	if mainChanged {
		a.notifyMainRoutingChanged("model_pool_changed")
	}
	if applyErr != nil {
		if !appliedAny {
			a.restorePendingModelPoolRollback()
		} else {
			a.restoreFailedModelPoolSelections(mainFailed, failedAgents)
			a.pendingModelPoolRollback = nil
		}
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/models: switch model: %w", applyErr)})
		return
	}
	a.pendingModelPoolRollback = nil
}

func (a *MainAgent) AgentOverridePoolName(agentName string) (string, bool) {
	if a.modelPoolPolicy == nil {
		return "", false
	}
	return a.modelPoolPolicy.AgentOverride(agentName)
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	maps.Copy(out, src)
	return out
}

func (a *MainAgent) snapshotModelPoolState() (string, map[string]string) {
	if a.modelPoolPolicy == nil {
		return "", nil
	}
	return strings.TrimSpace(a.modelPoolPolicy.CurrentModelPool()), a.modelPoolPolicy.Overrides()
}

func (a *MainAgent) applySessionModelPoolState(loaded *loadedSessionState) {
	if a.modelPoolPolicy == nil || loaded == nil {
		return
	}
	if strings.TrimSpace(loaded.ModelPoolCurrentModelPool) == "" && len(loaded.ModelPoolAgentOverrides) == 0 {
		return
	}
	a.modelPoolPolicy.ReplaceSelectionsForSessionRestore(strings.TrimSpace(loaded.ModelPoolCurrentModelPool), loaded.ModelPoolAgentOverrides)
}

func (a *MainAgent) saveModelPoolState() {
	if a.modelPoolPolicy == nil || a.modelPoolStatePath == "" {
		return
	}
	state := &config.ModelPoolState{
		CurrentModelPool: a.modelPoolPolicy.CurrentModelPool(),
		AgentOverrides:   a.modelPoolPolicy.Overrides(),
	}
	knownAgents := make(map[string]struct{})
	a.stateMu.RLock()
	if a.agentConfigs != nil {
		for name := range a.agentConfigs {
			knownAgents[name] = struct{}{}
		}
	}
	a.stateMu.RUnlock()
	for agentName := range state.AgentOverrides {
		if _, ok := knownAgents[agentName]; !ok {
			delete(state.AgentOverrides, agentName)
		}
	}
	if err := config.SaveModelPoolState(a.modelPoolStatePath, state); err != nil {
		log.Warnf("failed to save model pool state: %v", err)
		a.emitToTUI(ToastEvent{
			Message: "Failed to save model pool state",
			Level:   "warn",
		})
	}
}
