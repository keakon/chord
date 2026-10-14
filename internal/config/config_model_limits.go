package config

// ModelLimit defines token limits for a model. Context is the total request
// window when known, Input is the input-side hard/usable budget when providers
// expose split limits, and Output is the requested-output hard cap.
type ModelLimit struct {
	Context int `json:"context" yaml:"context"`
	Input   int `json:"input,omitempty" yaml:"input,omitempty"`
	Output  int `json:"output" yaml:"output"`
	// ContextDerived marks a Context that normalizeModelLimits computed from
	// Input+Output rather than explicit configuration. Derived totals must not
	// survive a map-level project merge as if the user wrote them: a project
	// override of input/output re-derives instead of keeping the stale sum.
	ContextDerived bool `json:"-" yaml:"-"`
}

// EffectiveInputBudget returns the input-side budget used for request sizing
// and request safety checks. An explicit limit.input is authoritative and used
// as-is, even when it is not additive with limit.output inside the context
// window. Without limit.input, reserve this client's planned output budget,
// bounded by the model's output capacity. A maximum output capacity is not a
// published input allocation: models can allow output up to their entire
// context window while accepting normal inputs at smaller request output caps.
// This is a local planning budget; a transport that omits the output-cap field
// does not enforce it at the server. Independent input limits remain authoritative.
func (l ModelLimit) EffectiveInputBudget(outputCapSetting, defaultOutputCap int) int {
	if l.Input > 0 {
		return l.Input
	}
	if l.Context <= 0 {
		return 0
	}
	reserve := max(l.EffectiveOutputBudget(outputCapSetting, defaultOutputCap), 0)
	budget := l.Context - reserve
	if budget < 1 {
		return 1
	}
	return budget
}

// CompactionBudget is a fixed baseline for compaction ratios, independent of
// the output cap of an individual request. An explicit input allowance wins.
// Otherwise reserve the documented maximum output when it leaves a positive
// input allocation. Models whose output capacity spans the whole window have
// no fixed input/output partition, so their baseline is the total window.
// This recommendation baseline is not a provider-enforced input limit.
func (l ModelLimit) CompactionBudget() int {
	if l.Input > 0 {
		return l.Input
	}
	if l.Context <= 0 {
		return 0
	}
	if l.Output > 0 && l.Output < l.Context {
		return l.Context - l.Output
	}
	return l.Context
}

// normalizeModelLimits derives a total context window for models that publish
// separate positive input and output limits but omit context. An explicit
// context always wins because some providers publish a total window that is
// not the simple sum of their input and output limits.
func normalizeModelLimits(cfg *Config) {
	if cfg == nil {
		return
	}
	for providerName, provider := range cfg.Providers {
		for modelName, model := range provider.Models {
			if model.Limit.Context <= 0 && model.Limit.Input > 0 && model.Limit.Output > 0 {
				model.Limit.Context = model.Limit.Input + model.Limit.Output
				model.Limit.ContextDerived = true
				provider.Models[modelName] = model
			}
		}
		cfg.Providers[providerName] = provider
	}
}

// EffectiveOutputBudget returns the requested-output budget after applying the
// global output cap default and the model's own limit.output cap.
func (l ModelLimit) EffectiveOutputBudget(outputCapSetting, defaultOutputCap int) int {
	budget := outputCapSetting
	if budget <= 0 {
		budget = defaultOutputCap
	}
	if l.Output > 0 && (budget <= 0 || l.Output < budget) {
		budget = l.Output
	}
	if budget < 0 {
		return 0
	}
	return budget
}
