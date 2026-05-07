package agent

import (
	"strings"
	"sync"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

type RuntimeModelPoolPolicy struct {
	mu sync.RWMutex

	currentRolePool string
	overrides       map[string]string

	lastPicked map[string]map[string]string
}

func NewRuntimeModelPoolPolicy() *RuntimeModelPoolPolicy {
	return &RuntimeModelPoolPolicy{
		overrides:  make(map[string]string),
		lastPicked: make(map[string]map[string]string),
	}
}

func (p *RuntimeModelPoolPolicy) ReplaceSelections(currentRolePool string, overrides map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentRolePool = currentRolePool
	p.overrides = make(map[string]string, len(overrides))
	for agentName, pool := range overrides {
		agentName = strings.TrimSpace(agentName)
		if agentName == "" {
			continue
		}
		p.overrides[agentName] = strings.TrimSpace(pool)
	}
}

func (p *RuntimeModelPoolPolicy) ReplaceSelectionsForSessionRestore(currentRolePool string, overrides map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentRolePool = currentRolePool
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

func (p *RuntimeModelPoolPolicy) SetCurrentRole(pool string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentRolePool = pool
}

func (p *RuntimeModelPoolPolicy) CurrentRole() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.currentRolePool
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

	// Current role pool is only applied to non-subagent roles.
	if cfg != nil && !cfg.IsSubAgent() && cfg.HasPool(p.currentRolePool) {
		return p.currentRolePool
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
				for _, m := range models {
					if m == ref {
						return ref
					}
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
	for k, v := range p.overrides {
		out[k] = v
	}
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
	if sub := a.validFocusedSubAgent(); sub != nil {
		a.stateMu.RLock()
		cfg := a.agentConfigs[sub.agentDefName]
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

func (a *MainAgent) MainRoleCurrentPoolName() string {
	if a.modelPoolPolicy == nil {
		return ""
	}
	cfg := a.currentActiveConfig()
	if cfg == nil {
		return ""
	}
	return a.modelPoolPolicy.EffectivePool(cfg.Name, cfg)
}

func (a *MainAgent) MainRolePoolNames() []string {
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

func (a *MainAgent) SetCurrentRolePool(pool string) error {
	return a.setCurrentRolePool(pool, true)
}

func (a *MainAgent) SetAgentModelPool(agentName, pool string) error {
	return a.setAgentModelPool(agentName, pool, true)
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
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (a *MainAgent) snapshotModelPoolState() (string, map[string]string) {
	if a.modelPoolPolicy == nil {
		return "", nil
	}
	return strings.TrimSpace(a.modelPoolPolicy.CurrentRole()), a.modelPoolPolicy.Overrides()
}

func (a *MainAgent) applySessionModelPoolState(loaded *loadedSessionState) {
	if a.modelPoolPolicy == nil || loaded == nil {
		return
	}
	if strings.TrimSpace(loaded.ModelPoolCurrentRole) == "" && len(loaded.ModelPoolAgentOverrides) == 0 {
		return
	}
	a.modelPoolPolicy.ReplaceSelectionsForSessionRestore(strings.TrimSpace(loaded.ModelPoolCurrentRole), loaded.ModelPoolAgentOverrides)
}

func (a *MainAgent) saveModelPoolState() {
	if a.modelPoolPolicy == nil || a.modelPoolStatePath == "" {
		return
	}
	state := &config.ModelPoolState{
		CurrentRole:    a.modelPoolPolicy.CurrentRole(),
		AgentOverrides: a.modelPoolPolicy.Overrides(),
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
