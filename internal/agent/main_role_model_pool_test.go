package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

// rolePoolCall records the model-pool snapshot a role switch handed to the
// model-switch factory. Before the fix the factory derived the pool from the
// still-active (old) role, so a switch installed the previous role's pool — or
// no pool at all when the target head was not in it.
type rolePoolCall struct {
	providerModel string
	poolRefs      []string
	poolVariant   string
}

// splitRolePoolTestRef splits "provider/model" with an optional inline
// "@variant" into its parts, applying defaultVariant when no inline variant is
// present — the same rule buildModelPool applies to role pool entries.
func splitRolePoolTestRef(ref, defaultVariant string) (provider, model, variant string) {
	base, variant := config.ParseModelRef(ref)
	if variant == "" {
		variant = defaultVariant
	}
	provider, model, _ = strings.Cut(base, "/")
	return provider, model, variant
}

// newRolePoolTestClient assembles a client the way the production factory does
// for a caller-supplied pool snapshot: poolRefs become the fallback chain
// (each entry defaults to poolVariant), and the pool is attached when the
// selected model is part of that chain. A nil poolRefs yields a plain single
// model client with no pool.
func newRolePoolTestClient(t *testing.T, providerModel string, poolRefs []string, poolVariant string) *llm.Client {
	t.Helper()
	if len(poolRefs) == 0 {
		prov, model, _ := splitRolePoolTestRef(providerModel, "")
		return newRoleSwitchClient(t, prov, model, 8192)
	}
	selProv, selModel, selVariant := splitRolePoolTestRef(providerModel, poolVariant)
	pool := make([]llm.FallbackModel, 0, len(poolRefs))
	selectedIdx := -1
	for _, ref := range poolRefs {
		prov, model, variant := splitRolePoolTestRef(ref, poolVariant)
		entry := newRoleSwitchClient(t, prov, model, 8192).PrimaryModelEntry()
		entry.Variant = variant
		if selectedIdx < 0 && prov == selProv && model == selModel && variant == selVariant {
			selectedIdx = len(pool)
		}
		pool = append(pool, entry)
	}
	if selectedIdx < 0 {
		// The production factory only attaches a pool when the selected model
		// is a member of the given chain; anything else stays a single model.
		prov, model, _ := splitRolePoolTestRef(providerModel, poolVariant)
		return newRoleSwitchClient(t, prov, model, 8192)
	}
	selected := pool[selectedIdx]
	client := llm.NewClient(selected.ProviderConfig, selected.ProviderImpl, selected.ModelID, selected.MaxTokens, "")
	client.SetModelPool(pool, selectedIdx)
	if _, inlineVariant := config.ParseModelRef(providerModel); inlineVariant != "" {
		client.SetVariant(inlineVariant)
	}
	return client
}

// installRolePoolClient makes the given client the active main LLM client, as
// an earlier role switch or startup would have.
func installRolePoolClient(t *testing.T, a *MainAgent, ref string, refs []string) *llm.Client {
	t.Helper()
	client := newRolePoolTestClient(t, ref, refs, "")
	a.swapLLMClientWithRef(client, "model", 8192, ref)
	return client
}

func poolModelIDs(t *testing.T, client *llm.Client) (ids []string, variants []string, selectedIdx int) {
	t.Helper()
	if client == nil {
		t.Fatal("installed client is nil")
	}
	pool, selectedIdx := client.ModelPoolSnapshot()
	ids = make([]string, 0, len(pool))
	variants = make([]string, 0, len(pool))
	for _, entry := range pool {
		ids = append(ids, entry.ProviderConfig.Name()+"/"+entry.ModelID)
		variants = append(variants, entry.Variant)
	}
	return ids, variants, selectedIdx
}

// TestSwitchRoleBuildsTargetPoolWhenHeadAbsentFromOldPool locks the regression
// where preparing the new role's model before switching activeConfig made the
// factory resolve the pool from the old role: when the target head was not in
// the old pool, no pool was installed and the switch degraded to a single model
// with no fallback chain.
func TestSwitchRoleBuildsTargetPoolWhenHeadAbsentFromOldPool(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	builderRefs := []string{"alpha/model-one", "alpha/model-two"}
	execRefs := []string{"beta/model-one", "beta/model-two"}
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder":  {Name: "builder", Mode: config.AgentModeMain, Models: map[string][]string{"default": builderRefs}},
		"executor": {Name: "executor", Mode: config.AgentModeMain, Models: map[string][]string{"default": execRefs}},
	})
	oldClient := installRolePoolClient(t, a, "alpha/model-one", builderRefs)

	var calls []rolePoolCall
	a.SetModelSwitchFactory(func(providerModel string, poolRefs []string, poolVariant string) (*llm.Client, string, int, error) {
		calls = append(calls, rolePoolCall{providerModel, append([]string(nil), poolRefs...), poolVariant})
		return newRolePoolTestClient(t, providerModel, poolRefs, poolVariant), "model-one", 8192, nil
	})

	if err := a.switchRole("executor", false); err != nil {
		t.Fatalf("switchRole: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("factory calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.providerModel != "beta/model-one" {
		t.Fatalf("factory providerModel = %q, want beta/model-one", call.providerModel)
	}
	if len(call.poolRefs) != 2 || call.poolRefs[0] != "beta/model-one" || call.poolRefs[1] != "beta/model-two" {
		t.Fatalf("factory poolRefs = %#v, want the executor chain %#v (not the old role's %#v)", call.poolRefs, execRefs, builderRefs)
	}

	client, _, providerRef, _ := a.llmSnapshot()
	if client == oldClient {
		t.Fatal("main client was not swapped to the prepared executor client")
	}
	if providerRef != "beta/model-one" {
		t.Fatalf("ProviderModelRef = %q, want beta/model-one", providerRef)
	}
	ids, _, selectedIdx := poolModelIDs(t, client)
	if len(ids) != 2 || ids[0] != "beta/model-one" || ids[1] != "beta/model-two" {
		t.Fatalf("installed pool = %#v, want full executor chain beta/model-one, beta/model-two", ids)
	}
	if selectedIdx != 0 {
		t.Fatalf("installed pool selectedIdx = %d, want 0 (head selected)", selectedIdx)
	}
}

// TestSwitchRoleSwapsToTargetPoolWhenHeadInOldPool locks the regression where a
// target head that was also present in the old role's pool kept the old role's
// fallback chain and variant: the switch looked successful but kept serving the
// previous role's model policy.
func TestSwitchRoleSwapsToTargetPoolWhenHeadInOldPool(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	builderRefs := []string{"alpha/shared-model", "alpha/build-only"}
	execRefs := []string{"alpha/shared-model", "alpha/exec-only"}
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder":  {Name: "builder", Mode: config.AgentModeMain, Models: map[string][]string{"default": builderRefs}},
		"executor": {Name: "executor", Mode: config.AgentModeMain, Variant: "balanced", Models: map[string][]string{"default": execRefs}},
	})
	installRolePoolClient(t, a, "alpha/shared-model", builderRefs)

	var calls []rolePoolCall
	a.SetModelSwitchFactory(func(providerModel string, poolRefs []string, poolVariant string) (*llm.Client, string, int, error) {
		calls = append(calls, rolePoolCall{providerModel, append([]string(nil), poolRefs...), poolVariant})
		return newRolePoolTestClient(t, providerModel, poolRefs, poolVariant), "shared-model", 8192, nil
	})

	if err := a.switchRole("executor", false); err != nil {
		t.Fatalf("switchRole: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("factory calls = %d, want 1", len(calls))
	}
	call := calls[0]
	// The executor role carries a default variant, so its resolved head ref
	// carries the @balanced suffix; the fallback pool snapshot must still be
	// the executor's own chain and default variant.
	if call.providerModel != "alpha/shared-model@balanced" {
		t.Fatalf("factory providerModel = %q, want alpha/shared-model@balanced", call.providerModel)
	}
	if call.poolVariant != "balanced" {
		t.Fatalf("factory poolVariant = %q, want executor role variant balanced", call.poolVariant)
	}
	if len(call.poolRefs) != 2 || call.poolRefs[1] != "alpha/exec-only" {
		t.Fatalf("factory poolRefs = %#v, want the executor chain ending in alpha/exec-only", call.poolRefs)
	}

	client, _, providerRef, _ := a.llmSnapshot()
	if providerRef != "alpha/shared-model" {
		t.Fatalf("ProviderModelRef = %q, want alpha/shared-model", providerRef)
	}
	ids, _, selectedIdx := poolModelIDs(t, client)
	if len(ids) != 2 || ids[0] != "alpha/shared-model" || ids[1] != "alpha/exec-only" {
		t.Fatalf("installed pool = %#v, want executor chain alpha/shared-model, alpha/exec-only (not the old role's build-only fallback)", ids)
	}
	if selectedIdx != 0 {
		t.Fatalf("installed pool selectedIdx = %d, want 0", selectedIdx)
	}
}
