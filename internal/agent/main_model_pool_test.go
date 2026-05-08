package agent

import (
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
		t.Fatalf("no current role pool set, should use first pool alphabetically: got %q, want %q", pool, "base")
	}

	p.SetCurrentRole("fast")
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
	p.SetCurrentRole("fast")

	if pool := p.EffectivePool("builder", cfg); pool != "base" {
		t.Fatalf("current role pool not supported, should fallback to first pool: got %q", pool)
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
	p.SetCurrentRole("fast")

	// Subagents ignore the current role pool unless explicitly overridden.
	if pool := p.EffectivePool("reviewer", reviewer); pool != "base" {
		t.Fatalf("subagent should ignore current role pool: got %q, want %q", pool, "base")
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

	p.SetCurrentRole("fast")
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
	p.SetCurrentRole("fast")

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

	p.SetCurrentRole("fast")
	if pool := p.EffectivePool("builder", cfg); pool != "fast" {
		t.Fatalf("current role fast pool: got %q, want %q", pool, "fast")
	}
}
