package agent

import (
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestRuntimeModelPoolPolicyEffectivePool(t *testing.T) {
	cfg := &config.AgentConfig{
		Name: "builder",
		Mode: config.AgentModeMain,
		Models: map[string][]string{
			"base":   {"provider/model-a"},
			"fast":   {"provider/model-b"},
			"strong": {"provider/model-c"},
		},
	}

	p := NewRuntimeModelPoolPolicy()

	if pool := p.EffectivePool("builder", cfg); pool != "base" {
		t.Fatalf("no current model pool set, should use first pool alphabetically: got %q, want %q", pool, "base")
	}

	p.SetCurrentModelPool("fast")
	if pool := p.EffectivePool("builder", cfg); pool != "fast" {
		t.Fatalf("current role fast pool: got %q, want %q", pool, "fast")
	}

	p.SetAgentOverride("builder", "strong")
	if pool := p.EffectivePool("builder", cfg); pool != "strong" {
		t.Fatalf("override strong pool: got %q, want %q", pool, "strong")
	}

	p.ClearAgentOverride("builder")
	if pool := p.EffectivePool("builder", cfg); pool != "fast" {
		t.Fatalf("after clear override: got %q, want %q", pool, "fast")
	}
}

func TestRuntimeModelPoolPolicyFallbackToFirstPool(t *testing.T) {
	cfg := &config.AgentConfig{
		Name: "builder",
		Mode: config.AgentModeMain,
		Models: map[string][]string{
			"base": {"provider/model-a"},
		},
	}

	p := NewRuntimeModelPoolPolicy()
	p.SetCurrentModelPool("fast")

	if pool := p.EffectivePool("builder", cfg); pool != "base" {
		t.Fatalf("current model pool not supported, should fallback to first pool: got %q", pool)
	}
}

func TestRuntimeModelPoolPolicyEmptyConfig(t *testing.T) {
	cfg := &config.AgentConfig{
		Name:   "builder",
		Mode:   config.AgentModeMain,
		Models: map[string][]string{},
	}

	p := NewRuntimeModelPoolPolicy()
	if pool := p.EffectivePool("builder", cfg); pool != "" {
		t.Fatalf("empty models should give empty pool: got %q", pool)
	}
	if models := p.EffectiveModels("builder", cfg); models != nil {
		t.Fatalf("empty models should give nil effective models: got %v", models)
	}
}

func TestRuntimeModelPoolPolicyGlobalDoesNotAffectSubAgent(t *testing.T) {
	reviewer := &config.AgentConfig{
		Name: "reviewer",
		Mode: "subagent",
		Models: map[string][]string{
			"base": {"provider/model-a"},
			"fast": {"provider/model-b"},
		},
	}

	p := NewRuntimeModelPoolPolicy()
	p.SetCurrentModelPool("fast")

	// Subagents ignore the current model pool unless explicitly overridden.
	if pool := p.EffectivePool("reviewer", reviewer); pool != "base" {
		t.Fatalf("subagent should ignore current model pool: got %q, want %q", pool, "base")
	}

	p.SetAgentOverride("reviewer", "fast")
	if pool := p.EffectivePool("reviewer", reviewer); pool != "fast" {
		t.Fatalf("subagent override should win: got %q, want %q", pool, "fast")
	}
}

func TestRuntimeModelPoolPolicyEffectiveModels(t *testing.T) {
	cfg := &config.AgentConfig{
		Name: "builder",
		Mode: config.AgentModeMain,
		Models: map[string][]string{
			"base": {"provider/model-a", "provider/model-b"},
			"fast": {"provider/model-c"},
		},
	}

	p := NewRuntimeModelPoolPolicy()
	models := p.EffectiveModels("builder", cfg)
	if len(models) != 2 || models[0] != "provider/model-a" || models[1] != "provider/model-b" {
		t.Fatalf("first pool effective models: got %v", models)
	}

	p.SetCurrentModelPool("fast")
	models = p.EffectiveModels("builder", cfg)
	if len(models) != 1 || models[0] != "provider/model-c" {
		t.Fatalf("fast effective models: got %v", models)
	}

	models[0] = "mutated"
	models2 := p.EffectiveModels("builder", cfg)
	if models2[0] != "provider/model-c" {
		t.Fatalf("EffectiveModels must return a copy: got %v", models2)
	}
}

func TestRuntimeModelPoolPolicyLastPicked(t *testing.T) {
	cfg := &config.AgentConfig{
		Name: "builder",
		Mode: config.AgentModeMain,
		Models: map[string][]string{
			"base": {"provider/model-a", "provider/model-b"},
		},
	}

	p := NewRuntimeModelPoolPolicy()

	ref := p.ResolveInitialModelRef("builder", cfg)
	if ref != "provider/model-a" {
		t.Fatalf("initial model without lastPicked: got %q, want %q", ref, "provider/model-a")
	}

	p.SetLastPicked("builder", "base", "provider/model-b")
	ref = p.ResolveInitialModelRef("builder", cfg)
	if ref != "provider/model-b" {
		t.Fatalf("initial model with lastPicked: got %q, want %q", ref, "provider/model-b")
	}
}

func TestRuntimeModelPoolPolicyOverridePrecedence(t *testing.T) {
	builderCfg := &config.AgentConfig{
		Name: "builder",
		Mode: config.AgentModeMain,
		Models: map[string][]string{
			"base": {"provider/model-a"},
			"fast": {"provider/model-b"},
		},
	}
	reviewerCfg := &config.AgentConfig{
		Name: "reviewer",
		Mode: "subagent",
		Models: map[string][]string{
			"base":   {"provider/model-c"},
			"strong": {"provider/model-d"},
		},
	}

	p := NewRuntimeModelPoolPolicy()
	p.SetCurrentModelPool("fast")

	if pool := p.EffectivePool("builder", builderCfg); pool != "fast" {
		t.Fatalf("builder should use current role fast: got %q", pool)
	}
	if pool := p.EffectivePool("reviewer", reviewerCfg); pool != "base" {
		t.Fatalf("reviewer doesn't have fast, should fallback to first pool: got %q", pool)
	}

	p.SetAgentOverride("reviewer", "strong")
	if pool := p.EffectivePool("reviewer", reviewerCfg); pool != "strong" {
		t.Fatalf("reviewer override should take precedence: got %q", pool)
	}
}

func TestRuntimeModelPoolPolicyFallbackRespectsModelPoolsOrder(t *testing.T) {
	agents := map[string]*config.AgentConfig{
		"builder": {
			Name:       "builder",
			Mode:       config.AgentModeMain,
			ModelPools: []string{"thinking", "non-thinking"},
		},
	}
	globalPools := map[string][]string{
		"thinking":     {"provider/thinking"},
		"non-thinking": {"provider/non"},
	}
	if err := config.ResolveAgentModelPools(agents, globalPools); err != nil {
		t.Fatalf("ResolveAgentModelPools: %v", err)
	}
	cfg := agents["builder"]
	p := NewRuntimeModelPoolPolicy()
	if pool := p.EffectivePool("builder", cfg); pool != "thinking" {
		t.Fatalf("fallback should respect model_pools list order: got %q, want %q", pool, "thinking")
	}
}

func TestApplySessionModelPoolStateRewritesMissingPoolsByOwningAgent(t *testing.T) {
	agents := map[string]*config.AgentConfig{
		"builder": {
			Name:       "builder",
			Mode:       config.AgentModeMain,
			ModelPools: []string{"gpt-5.6-sol-high", "gpt-5.6-sol-max"},
		},
		"planner": {
			Name:       "planner",
			Mode:       config.AgentModeMain,
			ModelPools: []string{"free", "gpt-5.6-sol-high"},
		},
		"reviewer": {
			Name:       "reviewer",
			Mode:       config.AgentModeSubAgent,
			ModelPools: []string{"glm", "free"},
		},
		"explorer": {
			Name:       "explorer",
			Mode:       config.AgentModeSubAgent,
			ModelPools: []string{"free", "glm"},
		},
	}
	globalPools := map[string][]string{
		"gpt-5.6-sol-high": {"provider/high"},
		"gpt-5.6-sol-max":  {"provider/max"},
		"glm":              {"provider/glm"},
		"free":             {"provider/free"},
	}
	if err := config.ResolveAgentModelPools(agents, globalPools); err != nil {
		t.Fatalf("ResolveAgentModelPools: %v", err)
	}

	statePath := filepath.Join(t.TempDir(), "model_pool_state.yaml")
	a := &MainAgent{
		agentConfigs:       agents,
		activeConfig:       agents["planner"],
		modelPoolPolicy:    NewRuntimeModelPoolPolicy(),
		modelPoolStatePath: statePath,
	}
	a.applySessionModelPoolState(&loadedSessionState{
		ActiveRole:                "builder",
		ModelPoolCurrentModelPool: "gpt-5.6-sol",
		ModelPoolAgentOverrides: map[string]string{
			"reviewer": "removed-review-pool",
			"explorer": "glm",
		},
	})

	if got := a.modelPoolPolicy.CurrentModelPool(); got != "gpt-5.6-sol-high" {
		t.Fatalf("CurrentModelPool() = %q, want builder's first pool", got)
	}
	if got, ok := a.modelPoolPolicy.AgentOverride("reviewer"); !ok || got != "glm" {
		t.Fatalf("reviewer override = %q, %v; want first reviewer pool", got, ok)
	}
	if got, ok := a.modelPoolPolicy.AgentOverride("explorer"); !ok || got != "glm" {
		t.Fatalf("explorer override = %q, %v; want valid restored pool preserved", got, ok)
	}

	persisted, err := config.LoadModelPoolState(statePath)
	if err != nil {
		t.Fatalf("LoadModelPoolState: %v", err)
	}
	if persisted.CurrentModelPool != "gpt-5.6-sol-high" || persisted.AgentOverrides["reviewer"] != "glm" {
		t.Fatalf("persisted state = %#v, want rewritten selections", persisted)
	}

	a.modelPoolPolicy = NewRuntimeModelPoolPolicy()
	a.applySessionModelPoolState(&loadedSessionState{
		ModelPoolCurrentModelPool: "removed-pool",
	})
	if got := a.modelPoolPolicy.CurrentModelPool(); got != "free" {
		t.Fatalf("CurrentModelPool() without restored active role = %q, want current role's first pool", got)
	}
}

func TestRuntimeModelPoolPolicyOverrides(t *testing.T) {
	p := NewRuntimeModelPoolPolicy()
	p.SetAgentOverride("reviewer", "strong")
	p.SetAgentOverride("planner", "fast")

	overrides := p.Overrides()
	if len(overrides) != 2 {
		t.Fatalf("expected 2 overrides, got %d", len(overrides))
	}
	if overrides["reviewer"] != "strong" {
		t.Fatalf("reviewer override: got %q, want %q", overrides["reviewer"], "strong")
	}
	if overrides["planner"] != "fast" {
		t.Fatalf("planner override: got %q, want %q", overrides["planner"], "fast")
	}

	overrides["mutated"] = "nope"
	overrides2 := p.Overrides()
	if _, ok := overrides2["mutated"]; ok {
		t.Fatal("Overrides must return a copy")
	}
}

func TestRuntimeModelPoolPolicyClearAgentOverride(t *testing.T) {
	p := NewRuntimeModelPoolPolicy()
	p.SetAgentOverride("reviewer", "strong")
	p.ClearAgentOverride("reviewer")

	if _, ok := p.AgentOverride("reviewer"); ok {
		t.Fatal("override should be cleared")
	}
}

func TestRuntimeModelPoolPolicyDefaultPoolNameAllowed(t *testing.T) {
	cfg := &config.AgentConfig{
		Name: "builder",
		Mode: config.AgentModeMain,
		Models: map[string][]string{
			"default": {"provider/model-a"},
			"fast":    {"provider/model-b"},
		},
	}

	p := NewRuntimeModelPoolPolicy()

	if pool := p.EffectivePool("builder", cfg); pool != "default" {
		t.Fatalf("first pool alphabetically is 'default': got %q, want %q", pool, "default")
	}

	p.SetCurrentModelPool("fast")
	if pool := p.EffectivePool("builder", cfg); pool != "fast" {
		t.Fatalf("current role fast pool: got %q, want %q", pool, "fast")
	}
}
