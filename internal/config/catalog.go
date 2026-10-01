package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/modelcatalog"
)

func modelcatalogVersion() string { return modelcatalog.Version() }

// CatalogResponsesCompat returns the verified optional Responses fields for a
// model binding. It is a lowest-priority internal default: callers must merge
// provider and model compat over it before building a request.
func CatalogResponsesCompat(preset, wireModel string, model ModelConfig) *ResponsesCompatConfig {
	preset = strings.ToLower(strings.TrimSpace(preset))
	if model.Catalog != nil && model.Catalog.Disabled {
		return nil
	}
	var binding modelcatalog.Binding
	var ok bool
	if model.Catalog != nil && model.Catalog.ID != "" {
		binding, ok = modelcatalog.LookupBindingByModelID(preset, model.Catalog.ID)
	} else {
		binding, ok = modelcatalog.LookupBinding(preset, wireModel)
	}
	if !ok || binding.Responses == nil {
		return nil
	}
	r := binding.Responses
	return &ResponsesCompatConfig{
		SendStore:             cloneBool(r.SendStore),
		SendReasoningInclude:  cloneBool(r.SendReasoningInclude),
		SendToolChoice:        cloneBool(r.SendToolChoice),
		SendPromptCacheKey:    cloneBool(r.SendPromptCacheKey),
		SendMaxOutputTokens:   cloneBool(r.SendMaxOutputTokens),
		SendParallelToolCalls: cloneBool(r.SendParallelToolCalls),
	}
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	return new(*value)
}

// responsesSendFields lists the Responses field-emission gates the catalog can
// claim on. The catalog never claims mcp_additional_tools, so it is absent.
var responsesSendFields = []struct {
	name  string
	value func(*ResponsesCompatConfig) *bool
}{
	{"send_store", func(c *ResponsesCompatConfig) *bool { return c.SendStore }},
	{"send_reasoning_include", func(c *ResponsesCompatConfig) *bool { return c.SendReasoningInclude }},
	{"send_tool_choice", func(c *ResponsesCompatConfig) *bool { return c.SendToolChoice }},
	{"send_parallel_tool_calls", func(c *ResponsesCompatConfig) *bool { return c.SendParallelToolCalls }},
	{"send_prompt_cache_key", func(c *ResponsesCompatConfig) *bool { return c.SendPromptCacheKey }},
	{"send_max_output_tokens", func(c *ResponsesCompatConfig) *bool { return c.SendMaxOutputTokens }},
}

// catalogSendOverrideDiagnostics reports user send-field overrides a verified
// binding rejects: the binding recorded that the endpoint never accepts the
// field (even as false), the explicit user value still wins per the source
// matrix and is emitted, and requests on the model are known to fail. The
// error-level diagnostic keeps the failure expected instead of mysterious and
// is counted by doctor; it does not block loading or other models.
func catalogSendOverrideDiagnostics(cfg *Config, claims func(preset, wireModel string, model ModelConfig) *ResponsesCompatConfig) []Diagnostic {
	if cfg == nil || len(cfg.Providers) == 0 {
		return nil
	}
	providerNames := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		providerNames = append(providerNames, name)
	}
	slices.Sort(providerNames)
	var diags []Diagnostic
	for _, providerName := range providerNames {
		provider := cfg.Providers[providerName]
		preset := strings.ToLower(strings.TrimSpace(provider.Preset))
		if !modelcatalog.IsManagedPreset(preset) {
			continue
		}
		modelNames := make([]string, 0, len(provider.Models))
		for name := range provider.Models {
			modelNames = append(modelNames, name)
		}
		slices.Sort(modelNames)
		for _, modelName := range modelNames {
			model := provider.Models[modelName]
			bindingClaims := claims(preset, modelName, model)
			if bindingClaims == nil {
				continue
			}
			var providerResp, modelResp *ResponsesCompatConfig
			if provider.Compat != nil {
				providerResp = provider.Compat.Responses
			}
			if model.Compat != nil {
				modelResp = model.Compat.Responses
			}
			for _, field := range responsesSendFields {
				claim := field.value(bindingClaims)
				if claim == nil || *claim {
					continue // no claim, or the binding accepts the field
				}
				layer, value := "", (*bool)(nil)
				if modelResp != nil {
					if value = field.value(modelResp); value != nil {
						layer = "model"
					}
				}
				if value == nil && providerResp != nil {
					if value = field.value(providerResp); value != nil {
						layer = "provider"
					}
				}
				if value == nil || !*value {
					continue // nothing explicitly sends the field
				}
				path := "providers." + providerName + ".compat.responses." + field.name
				if layer == "model" {
					path = "providers." + providerName + ".models." + modelName + ".compat.responses." + field.name
				}
				diags = append(diags, Diagnostic{
					Severity: DiagnosticSeverityError,
					Path:     path,
					Message: fmt.Sprintf(
						"the %q preset binding for model %q is verified never to accept the %s field; the explicit %s-layer override still sends it, so requests on this model will fail — remove the override, or the preset if the endpoint really accepts the field",
						preset, modelName, strings.TrimPrefix(field.name, "send_"), layer,
					),
					Continues: true,
					Scope:     "model " + providerName + "/" + modelName,
				})
			}
		}
	}
	return diags
}

// catalogCandidateModels returns the model names a preset provider materializes
// from the catalog: the models the user defined explicitly plus every model a
// pool references from this provider. The whole catalog is never expanded.
func catalogCandidateModels(cfg *Config, providerName string, extraRefs ...string) []string {
	p, ok := cfg.Providers[providerName]
	if !ok {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(modelName string) {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" || seen[modelName] {
			return
		}
		seen[modelName] = true
		out = append(out, modelName)
	}
	for modelName := range p.Models {
		add(modelName)
	}
	for _, refs := range cfg.ModelPools {
		for _, ref := range refs {
			base, _ := ParseModelRef(strings.TrimSpace(ref))
			refProvider, refModel := SplitProviderModelRef(base)
			if refProvider == providerName && refModel != "" {
				add(refModel)
			}
		}
	}
	for _, ref := range extraRefs {
		base, _ := ParseModelRef(strings.TrimSpace(ref))
		refProvider, refModel := SplitProviderModelRef(base)
		if refProvider == providerName && refModel != "" {
			add(refModel)
		}
	}
	slices.Sort(out)
	return out
}

// catalogModelDefaults resolves explicit IDs even for custom endpoints. Only a
// verified preset binding contributes endpoint-specific variants.
func catalogModelDefaults(preset, wireModel string, model ModelConfig) (modelcatalog.ModelFacts, map[string]modelcatalog.Variant, bool, error) {
	if model.Catalog != nil && model.Catalog.Disabled {
		return modelcatalog.ModelFacts{}, nil, false, nil
	}
	preset = strings.ToLower(strings.TrimSpace(preset))
	var binding modelcatalog.Binding
	var bound bool
	if model.Catalog != nil && model.Catalog.ID != "" {
		facts, exists := modelcatalog.Model(model.Catalog.ID)
		if !exists {
			return modelcatalog.ModelFacts{}, nil, false, fmt.Errorf("catalog model %q not found", model.Catalog.ID)
		}
		if preset == "" {
			return facts, nil, true, nil
		}
		binding, bound = modelcatalog.LookupBindingByModelID(preset, model.Catalog.ID)
		if !bound {
			return modelcatalog.ModelFacts{}, nil, false, fmt.Errorf("catalog model %q is not bound to preset %q", model.Catalog.ID, preset)
		}
	} else {
		binding, bound = modelcatalog.LookupBinding(preset, wireModel)
	}
	if !bound {
		return modelcatalog.ModelFacts{}, nil, false, nil
	}
	facts, exists := modelcatalog.Model(binding.ModelID)
	return facts, binding.Variants, exists, nil
}

// materializeCatalogModels fills unset facts for explicitly bound custom models
// and verified preset models. Only configured and pool-referenced models expand.
func materializeCatalogModels(rc *ResolvedConfig, extraRefs ...string) []Diagnostic {
	if rc == nil || rc.Config == nil {
		return nil
	}
	var diagnostics []Diagnostic
	for providerName, provider := range rc.Config.Providers {
		changed := false
		for _, modelName := range catalogCandidateModels(rc.Config, providerName, extraRefs...) {
			mc, userDefined := provider.Models[modelName]
			facts, variants, found, err := catalogModelDefaults(provider.Preset, modelName, mc)
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: DiagnosticSeverityError,
					Path:     "providers." + providerName + ".models." + modelName + ".catalog",
					Message:  err.Error(), Continues: true, Scope: "model " + providerName + "/" + modelName,
				})
				continue
			}
			if !found {
				continue
			}
			if mc.Name == "" {
				mc.Name = modelName
			}
			if rc.Index != nil {
				rc.Index.ensureModelOrigin(providerName, modelName)
			}
			if mc.fillFromCatalog(facts, variants, rc.Index, providerName, modelName, userDefined) {
				if provider.Models == nil {
					provider.Models = make(map[string]ModelConfig)
				}
				provider.Models[modelName] = mc
				changed = true
			}
		}
		if changed {
			rc.Config.Providers[providerName] = provider
		}
	}
	return diagnostics
}

// fillFromCatalog fills the model's unset fields from verified catalog facts
// and reports whether anything changed. Nullable blocks cleared by a user
// layer are skipped; the catalog origin of every filled path is recorded as
// the lowest-priority layer in the index.
func (m *ModelConfig) fillFromCatalog(facts modelcatalog.ModelFacts, variants map[string]modelcatalog.Variant, idx *SourceIndex, provider, model string, userDefined bool) bool {
	changed := false
	leafDeclared := func(leaf string) bool {
		if !userDefined || idx == nil {
			return false
		}
		mo, ok := idx.Model(provider, model)
		if !ok {
			return false
		}
		switch leaf {
		case "context":
			return len(mo.Limit.Context) > 0
		case "input":
			return len(mo.Limit.Input) > 0
		case "output":
			return len(mo.Limit.Output) > 0
		}
		return false
	}
	blockCleared := func(block string) bool {
		if !userDefined || idx == nil {
			return false
		}
		decls, ok := idx.ModelBlock(provider, model, block)
		if !ok {
			return false
		}
		state := BlockClearing(decls)
		return state.Cleared || state.LowerCleared
	}
	recordLimit := func(leaf string) {
		if idx != nil {
			idx.prependCatalogLimitOrigin(provider, model, leaf)
		}
	}

	if m.Limit.Context == 0 && !leafDeclared("context") {
		m.Limit.Context = facts.Context
		recordLimit("context")
		changed = true
	}
	if m.Limit.Input == 0 && facts.Input > 0 && !leafDeclared("input") {
		m.Limit.Input = facts.Input
		recordLimit("input")
		changed = true
	}
	if m.Limit.Output == 0 && !leafDeclared("output") {
		m.Limit.Output = facts.Output
		recordLimit("output")
		changed = true
	}

	if m.Modalities == nil && len(facts.InputModalities) > 0 && !blockCleared("modalities") {
		m.Modalities = &ModelModalities{Input: slices.Clone(facts.InputModalities)}
		if idx != nil {
			idx.prependCatalogBlockOrigin(provider, model, "modalities")
		}
		changed = true
	}

	if len(variants) > 0 && !blockCleared("variants") {
		if m.Variants == nil {
			m.Variants = make(map[string]ModelVariant, len(variants))
		}
		variantAdded := false
		for name, cv := range variants {
			existing, exists := m.Variants[name]
			if !exists {
				m.Variants[name] = catalogModelVariant(cv)
				variantAdded = true
				continue
			}
			// A user-defined variant keeps its explicit fields and inherits
			// only the ones it leaves unset.
			if existing.Reasoning == nil && cv.ReasoningEffort != "" {
				existing.Reasoning = &ReasoningConfig{Effort: cv.ReasoningEffort}
				variantAdded = true
			}
			if existing.Thinking == nil && (cv.ThinkingType != "" || cv.ThinkingEffort != "") {
				existing.Thinking = &ThinkingConfig{Type: cv.ThinkingType, Effort: cv.ThinkingEffort}
				variantAdded = true
			}
			m.Variants[name] = existing
		}
		if variantAdded {
			if idx != nil {
				idx.prependCatalogBlockOrigin(provider, model, "variants")
			}
			changed = true
		}
	}
	return changed
}

// catalogModelVariant converts a catalog variant onto the config's own variant
// type. Only knobs the catalog actually records are set.
func catalogModelVariant(cv modelcatalog.Variant) ModelVariant {
	mv := ModelVariant{}
	if cv.ReasoningEffort != "" {
		mv.Reasoning = &ReasoningConfig{Effort: cv.ReasoningEffort}
	}
	if cv.ThinkingType != "" || cv.ThinkingEffort != "" {
		mv.Thinking = &ThinkingConfig{Type: cv.ThinkingType, Effort: cv.ThinkingEffort}
	}
	return mv
}
