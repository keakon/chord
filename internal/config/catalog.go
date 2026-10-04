package config

import (
	"encoding/json"
	"fmt"
	"reflect"
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

// catalogModelDefaults resolves preset bindings and explicitly selected
// catalog IDs. Custom endpoints inherit recipes when the wire protocol matches.
func catalogModelDefaults(provider ProviderConfig, wireModel string, model ModelConfig) (modelcatalog.ModelFacts, map[string]modelcatalog.Variant, *modelcatalog.ConfigProfile, bool, error) {
	if model.Catalog != nil && model.Catalog.Disabled {
		return modelcatalog.ModelFacts{}, nil, nil, false, nil
	}
	preset := strings.ToLower(strings.TrimSpace(provider.Preset))
	var binding modelcatalog.Binding
	var bound bool
	if model.Catalog != nil && model.Catalog.ID != "" {
		facts, exists := modelcatalog.Model(model.Catalog.ID)
		if !exists {
			return modelcatalog.ModelFacts{}, nil, nil, false, fmt.Errorf("catalog model %q not found", model.Catalog.ID)
		}
		if preset == "" {
			variants, profile, err := customCatalogProfile(provider, facts)
			return facts, variants, profile, true, err
		}
		binding, bound = modelcatalog.LookupBindingByModelID(preset, model.Catalog.ID)
		if !bound {
			return modelcatalog.ModelFacts{}, nil, nil, false, fmt.Errorf("catalog model %q is not bound to preset %q", model.Catalog.ID, preset)
		}
	} else {
		binding, bound = modelcatalog.LookupBinding(preset, wireModel)
	}
	if !bound {
		return modelcatalog.ModelFacts{}, nil, nil, false, nil
	}
	facts, exists := modelcatalog.BindingFacts(binding)
	profile := modelcatalog.MergeConfigProfiles(facts.Profile, binding.Profile)
	return facts, binding.Variants, profile, exists, nil
}

// customCatalogProfile borrows the official recipe for the same wire protocol.
// A different wire still inherits protocol-independent compaction guidance.
// Endpoint addresses, OAuth settings and request compression never transfer.
func customCatalogProfile(provider ProviderConfig, facts modelcatalog.ModelFacts) (map[string]modelcatalog.Variant, *modelcatalog.ConfigProfile, error) {
	protocol := strings.ToLower(strings.TrimSpace(provider.Type))
	if protocol == "" {
		protocol = InferProviderTypeFromAPIURL(provider.APIURL)
	}
	vendor, _, _ := strings.Cut(facts.ID, "/")
	var selected *modelcatalog.Binding
	for _, endpoint := range modelcatalog.EndpointContracts() {
		if endpoint.Protocol != protocol {
			continue
		}
		if binding, ok := modelcatalog.LookupBindingByModelID(endpoint.PresetID, facts.ID); ok {
			if selected == nil || endpoint.PresetID == vendor {
				selected = &binding
			}
			if endpoint.PresetID == vendor {
				break
			}
		}
	}
	if selected != nil {
		profile := modelcatalog.MergeConfigProfiles(facts.Profile, selected.Profile)
		if selected.Responses != nil {
			data, err := json.Marshal(selected.Responses)
			if err != nil {
				return nil, nil, err
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				return nil, nil, err
			}
			profile = modelcatalog.MergeConfigProfiles(&modelcatalog.ConfigProfile{Compat: map[string]any{"responses": fields}}, profile)
		}
		return selected.Variants, profile, nil
	}
	if facts.Connection != nil && InferProviderTypeFromAPIURL(facts.Connection.RequestURL) == protocol {
		return nil, modelcatalog.MergeConfigProfiles(facts.Profile, nil), nil
	}
	if facts.Profile != nil && facts.Profile.Compaction != nil {
		return nil, &modelcatalog.ConfigProfile{Compaction: facts.Profile.Compaction, Sources: facts.Profile.Sources}, nil
	}
	return nil, nil, nil
}

// Catalog compat is below explicit provider settings, even though it is
// materialized into the model. Leave overlapping leaves to the provider layer.
func prepareCatalogCompatDefaults(defaults *ModelConfig, provider *ProviderCompatConfig, model *ModelCompatConfig) error {
	if defaults.Compat == nil {
		return nil
	}
	encode := func(value any) (map[string]any, error) {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var fields map[string]any
		err = json.Unmarshal(data, &fields)
		return fields, err
	}
	base, err := encode(defaults.Compat)
	if err != nil {
		return err
	}
	var override map[string]any
	if provider != nil {
		override, err = encode(provider)
		if err != nil {
			return err
		}
	}
	var omit func(map[string]any, map[string]any)
	omit = func(dst, src map[string]any) {
		for key, value := range src {
			if child, ok := value.(map[string]any); ok {
				if existing, ok := dst[key].(map[string]any); ok {
					omit(existing, child)
					if len(existing) == 0 {
						delete(dst, key)
					}
				}
			} else {
				delete(dst, key)
			}
		}
	}
	omit(base, override)
	if model != nil {
		fields, err := encode(model)
		if err != nil {
			return err
		}
		base = modelcatalog.MergeConfigProfiles(&modelcatalog.ConfigProfile{Compat: base}, &modelcatalog.ConfigProfile{Compat: fields}).Compat
	}
	if len(base) == 0 {
		defaults.Compat = nil
		return nil
	}
	data, err := json.Marshal(base)
	if err != nil {
		return err
	}
	var compat ModelCompatConfig
	if err := json.Unmarshal(data, &compat); err != nil {
		return err
	}
	defaults.Compat = &compat
	return nil
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
			facts, variants, profile, found, err := catalogModelDefaults(provider, modelName, mc)
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: DiagnosticSeverityError,
					Path:     "providers." + providerName + ".models." + modelName + ".catalog",
					Message:  err.Error(), Continues: true, Scope: "model " + providerName + "/" + modelName,
				})
				continue
			}
			var defaults ModelConfig
			if profile != nil {
				defaults, err = DecodeCatalogProfile(profile)
				if err == nil {
					err = prepareCatalogCompatDefaults(&defaults, provider.Compat, mc.Compat)
				}
				if provider.Store != nil {
					defaults.Store = nil
				}
				if provider.ParallelToolCalls != nil {
					defaults.ParallelToolCalls = nil
				}
				if err != nil {
					diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticSeverityError, Path: "providers." + providerName + ".models." + modelName + ".catalog", Message: err.Error(), Continues: true})
					continue
				}
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
			if mc.fillFromCatalog(facts, variants, profile, defaults, rc.Index, providerName, modelName, userDefined) {
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
func (m *ModelConfig) fillFromCatalog(facts modelcatalog.ModelFacts, variants map[string]modelcatalog.Variant, profile *modelcatalog.ConfigProfile, defaults ModelConfig, idx *SourceIndex, provider, model string, userDefined bool) bool {
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
	if profile != nil {
		fill := func(block string, unset bool, assign func()) {
			if !unset || blockCleared(block) {
				return
			}
			assign()
			if idx != nil {
				idx.prependCatalogBlockOrigin(provider, model, block)
			}
			changed = true
		}
		fill("thinking", m.Thinking == nil && defaults.Thinking != nil, func() { m.Thinking = defaults.Thinking })
		fill("reasoning", m.Reasoning == nil && defaults.Reasoning != nil, func() { m.Reasoning = defaults.Reasoning })
		fill("text", m.Text == nil && defaults.Text != nil, func() { m.Text = defaults.Text })
		fill("prompt_cache", m.PromptCache == nil && defaults.PromptCache != nil, func() { m.PromptCache = defaults.PromptCache })
		fill("parallel_tool_calls", m.ParallelToolCalls == nil && defaults.ParallelToolCalls != nil, func() { m.ParallelToolCalls = defaults.ParallelToolCalls })
		fill("store", m.Store == nil && defaults.Store != nil, func() { m.Store = defaults.Store })
		fill("compat", defaults.Compat != nil && !reflect.DeepEqual(m.Compat, defaults.Compat), func() { m.Compat = defaults.Compat })
		if c := profile.Compaction; c != nil && !blockCleared("compaction") {
			mc := m.Compaction
			if mc == nil {
				mc = &ModelCompactionConfig{}
			}
			added := false
			modelThresholdExplicit := mc.Threshold != nil
			// Explicit global settings win over recommendations. Explicit model
			// settings continue to override the global settings at runtime.
			if mc.Threshold == nil && c.Threshold != nil && (idx == nil || len(idx.Compaction["threshold"]) == 0) {
				mc.Threshold = new(*c.Threshold)
				added = true
			}
			if !modelThresholdExplicit && mc.Reminder == nil && c.Reminder != nil && (idx == nil || len(idx.Compaction["reminder"]) == 0 && len(idx.Compaction["threshold"]) == 0) {
				mc.Reminder = new(*c.Reminder)
				added = true
			}
			if added {
				m.Compaction = mc
				if idx != nil {
					idx.prependCatalogBlockOrigin(provider, model, "compaction")
				}
				changed = true
			}
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
