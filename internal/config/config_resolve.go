package config

import (
	"fmt"
	"sort"
	"strings"
)

// ResolveConfiguredPoolRefs validates every top-level model_pools reference
// against the configured providers: the provider must exist, the model must
// exist in it, and a @variant suffix must name a defined variant. It returns
// one error-level diagnostic per broken reference in pool name order and
// reference order, so output is deterministic.
//
// Loading continues regardless: these diagnostics describe which pools are
// broken so callers can block the actions that would use them (startup on the
// pools the main agent resolves, pool switches and fallback before they reach
// a broken pool) without turning one broken secondary pool into a global
// startup failure. Model existence is checked at load level for the first time
// here; previously it was only checked lazily when a provider was built, so a
// broken reference in a never-used pool stayed invisible until used.
func ResolveConfiguredPoolRefs(cfg *Config) []Diagnostic {
	if cfg == nil || len(cfg.ModelPools) == 0 {
		return nil
	}
	pools := make([]string, 0, len(cfg.ModelPools))
	for name := range cfg.ModelPools {
		pools = append(pools, name)
	}
	sort.Strings(pools)
	var diags []Diagnostic
	for _, pool := range pools {
		if strings.TrimSpace(pool) == "" {
			diags = append(diags, Diagnostic{
				Severity:  DiagnosticSeverityError,
				Path:      "model_pools",
				Message:   "model pool name must not be empty",
				Continues: true,
				Scope:     "model pool",
			})
			continue
		}
		for _, ref := range cfg.ModelPools[pool] {
			diag := Diagnostic{
				Severity:  DiagnosticSeverityError,
				Path:      "model_pools." + pool,
				Message:   "",
				Continues: true,
				Scope:     "model pool " + pool,
			}
			if problem := poolRefProblem(cfg, ref); problem != "" {
				diag.Message = problem
				diags = append(diags, diag)
			}
		}
	}
	return diags
}

// poolRefProblem describes why a pool reference does not resolve against the
// configured providers, or is empty when it does.
func poolRefProblem(cfg *Config, ref string) string {
	if err := ValidateConfiguredModelRefs(cfg.Providers, []string{ref}, ""); err != nil {
		return err.Error()
	}
	return ""
}

// ValidateConfiguredModelRefs checks the complete selection before any provider
// is created. Secondary pools remain inspectable until an action selects them.
func ValidateConfiguredModelRefs(providers map[string]ProviderConfig, refs []string, defaultVariant string) error {
	for _, ref := range refs {
		base, variant := ParseModelRef(strings.TrimSpace(ref))
		providerName, modelName := SplitProviderModelRef(base)
		if providerName == "" || modelName == "" {
			return fmt.Errorf("model ref %q must be provider/model", ref)
		}
		if variant == "" {
			variant = defaultVariant
		}
		provider, model, err := LookupConfiguredModelVariant(providers, providerName, modelName, variant)
		if err == nil {
			_, _, err = NormalizeProviderPreset(provider)
		}
		if err == nil {
			_, _, _, err = catalogModelDefaults(provider.Preset, modelName, model)
		}
		if err != nil {
			return fmt.Errorf("model ref %q: %w", ref, err)
		}
	}
	return nil
}
