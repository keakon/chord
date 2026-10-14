package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ContextConfig controls automatic context compression behavior.
type ContextConfig struct {
	Compaction CompactionConfig       `json:"compaction" yaml:"compaction,omitempty"`
	Reduction  ContextReductionConfig `json:"reduction" yaml:"reduction,omitempty"`
}

// ContextReductionConfig controls request-level context pruning. In YAML it
// accepts either a mapping of per-rule fields or the boolean shorthand
// `reduction: false`, which disables request-level reduction entirely.
type ContextReductionConfig struct {
	ConfirmAgeTurns         int `json:"confirm_age_turns,omitempty" yaml:"confirm_age_turns,omitempty"`
	ErrorAgeTurns           int `json:"error_age_turns,omitempty" yaml:"error_age_turns,omitempty"`
	HighRiskProtectAgeTurns int `json:"high_risk_protect_age_turns,omitempty" yaml:"high_risk_protect_age_turns,omitempty"`
	DiffProtectAgeTurns     int `json:"diff_protect_age_turns,omitempty" yaml:"diff_protect_age_turns,omitempty"`
	ShellSuccessAgeTurns    int `json:"shell_success_age_turns,omitempty" yaml:"shell_success_age_turns,omitempty"`
	ShellSuccessBytes       int `json:"shell_success_bytes,omitempty" yaml:"shell_success_bytes,omitempty"`
	ShellReadOnlyAgeTurns   int `json:"shell_read_only_age_turns,omitempty" yaml:"shell_read_only_age_turns,omitempty"`
	ReadLikeAgeTurns        int `json:"read_like_age_turns,omitempty" yaml:"read_like_age_turns,omitempty"`
	ReadLikeOutputBytes     int `json:"read_like_output_bytes,omitempty" yaml:"read_like_output_bytes,omitempty"`
	StaleAgeTurns           int `json:"stale_age_turns,omitempty" yaml:"stale_age_turns,omitempty"`
	StaleOutputBytes        int `json:"stale_output_bytes,omitempty" yaml:"stale_output_bytes,omitempty"`
	WrapUpGraceRequests     int `json:"wrap_up_grace_requests,omitempty" yaml:"wrap_up_grace_requests,omitempty"`
	MinToolResultsPrune     int `json:"min_tool_results_prune,omitempty" yaml:"min_tool_results_prune,omitempty"`
	MinIncrementalTokens    int `json:"min_incremental_saved_tokens,omitempty" yaml:"min_incremental_saved_tokens,omitempty"`

	mode contextReductionMode
}

type contextReductionMode uint8

const (
	contextReductionInherit contextReductionMode = iota
	contextReductionEnabled
	contextReductionDisabled
)

// DisabledValue reports whether request-level reduction was disabled via the
// boolean shorthand `reduction: false`.
func (c ContextReductionConfig) DisabledValue() bool { return c.mode == contextReductionDisabled }

// ExplicitEnabledValue reports whether this config layer explicitly enables
// or disables reduction. An omitted/null value inherits the less-specific
// layer; true and mappings enable, while false disables.
func (c ContextReductionConfig) ExplicitEnabledValue() (enabled, explicit bool) {
	switch c.mode {
	case contextReductionEnabled:
		return true, true
	case contextReductionDisabled:
		return false, true
	default:
		return false, false
	}
}

// MarshalYAML preserves the boolean-shorthand disabled state across the
// map-level project-config merge, which round-trips configs through YAML.
func (c ContextReductionConfig) MarshalYAML() (any, error) {
	if c.mode == contextReductionDisabled {
		return false, nil
	}
	type plain ContextReductionConfig
	return plain(c), nil
}

// contextReductionKnownKeys mirrors the yaml tags of ContextReductionConfig.
// The custom UnmarshalYAML decodes through yaml.Node, which does not inherit
// the loader's KnownFields strictness, so unknown keys are rejected here.
var contextReductionKnownKeys = map[string]bool{
	"confirm_age_turns":            true,
	"error_age_turns":              true,
	"high_risk_protect_age_turns":  true,
	"diff_protect_age_turns":       true,
	"shell_success_age_turns":      true,
	"shell_success_bytes":          true,
	"shell_read_only_age_turns":    true,
	"read_like_age_turns":          true,
	"read_like_output_bytes":       true,
	"stale_age_turns":              true,
	"stale_output_bytes":           true,
	"wrap_up_grace_requests":       true,
	"min_tool_results_prune":       true,
	"min_incremental_saved_tokens": true,
}

// UnmarshalYAML accepts a boolean shorthand (`reduction: false` disables
// reduction, `reduction: true` keeps the current tuning) or the usual field
// mapping. Mapping fields overlay the receiver's current values, matching the
// load path that unmarshals into a defaults-initialised config. A non-boolean
// scalar or an unknown field is reported as a TypeError so the loader logs it
// and treats that value as not configured; the recognized fields of the same
// mapping are still applied.
func (c *ContextReductionConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		if value.Tag == "!!null" {
			return nil
		}
		var enabled bool
		if err := value.Decode(&enabled); err != nil {
			return typeErrorAt(value, "context.reduction (mapping or boolean)")
		}
		if enabled {
			c.mode = contextReductionEnabled
		} else {
			c.mode = contextReductionDisabled
		}
		return nil
	}
	if value.Kind == yaml.MappingNode {
		c.mode = contextReductionEnabled
		var unknown []string
		known := make([]*yaml.Node, 0, len(value.Content))
		for i := 0; i+1 < len(value.Content); i += 2 {
			keyNode := value.Content[i]
			if !contextReductionKnownKeys[keyNode.Value] {
				unknown = append(unknown, contextReductionFieldError(keyNode))
				continue
			}
			known = append(known, keyNode, value.Content[i+1])
		}
		if len(unknown) > 0 {
			// Decode the recognized siblings before reporting: one unknown key
			// must be treated as not configured, not take the whole mapping
			// (and every valid tuning value in it) down with it.
			filtered := *value
			filtered.Content = known
			if err := c.decodeFields(&filtered); err != nil {
				return err
			}
			return &yaml.TypeError{Errors: unknown}
		}
	}
	return c.decodeFields(value)
}

// decodeFields decodes node into the receiver without re-entering
// UnmarshalYAML.
func (c *ContextReductionConfig) decodeFields(node *yaml.Node) error {
	type plain ContextReductionConfig
	p := plain(*c)
	if err := node.Decode(&p); err != nil {
		return err
	}
	*c = ContextReductionConfig(p)
	return nil
}

// removedContextReductionKeys names tuning keys earlier versions accepted, with
// what replaced them. An upgrading config still carries these, and reporting one
// as a bare unknown field tells the user nothing about what to do instead —
// which is the only reason tolerating them is better than rejecting them.
var removedContextReductionKeys = map[string]string{
	"high_pressure_usage": "request-batch age thresholds now control reduction without usage-pressure tuning",
	"force_prune_usage":   "request-batch age thresholds now control reduction without usage-pressure tuning",
}

// contextReductionFieldError describes one unrecognized context.reduction key,
// naming the migration when the key is a removed setting rather than a typo.
// Both spellings keep the "line N: field ..." shape so the override sanitizer
// still pins the failure to the key node (see parseTypeErrorFailures).
func contextReductionFieldError(keyNode *yaml.Node) string {
	if why, removed := removedContextReductionKeys[keyNode.Value]; removed {
		return fmt.Sprintf("line %d: field %s was removed from context.reduction; %s", keyNode.Line, keyNode.Value, why)
	}
	return unknownFieldErrorAt(keyNode, "context.reduction")
}

// CompactionConfig controls durable compaction backend, output profile, and
// input-budget reservation used by auto-compaction / oversize recovery.
type CompactionConfig struct {
	Threshold float64 `json:"threshold,omitempty" yaml:"threshold,omitempty"`
	Profile   string  `json:"profile,omitempty" yaml:"profile,omitempty"`
	Reserved  int     `json:"reserved,omitempty" yaml:"reserved,omitempty"`
	ModelPool string  `json:"model_pool,omitempty" yaml:"model_pool,omitempty"`
	// Reminder is the context-pressure reminder line as a usage fraction.
	// Zero means "not configured": the runtime derives it as
	// min(0.60, threshold*0.90). Unlike threshold, zero is not a disable
	// signal; CompactionReminderDisabled (-1) disables the reminder while
	// keeping automatic compaction.
	Reminder float64 `json:"reminder,omitempty" yaml:"reminder,omitempty"`
	// ModelDriven exposes the compact_context tool so the model can request a
	// durable context checkpoint once its working state is externalized.
	// Defaults to false; only the merged effective value decides whether the
	// tool is registered, so a project setting this false overrides a global
	// true like any other scalar override.
	ModelDriven bool `json:"model_driven,omitempty" yaml:"model_driven,omitempty"`
	// RetainRecentTokens is the estimated-token budget for the newest real
	// user messages (plus a dangling interrupted assistant reply) kept
	// verbatim inside every compaction checkpoint, so the continuation can
	// pick up the latest work boundary without re-reading the archived
	// history. Zero (or omitted) uses the built-in default; only the message
	// text counts toward the budget.
	RetainRecentTokens int `json:"retain_recent_tokens,omitempty" yaml:"retain_recent_tokens,omitempty"`
}

// CompactionReminderDisabled is the reminder value that switches the
// context-pressure reminder off without touching the compaction threshold.
const CompactionReminderDisabled = -1

// ModelCompactionConfig carries per-model overrides for the compaction
// threshold and reminder lines. Pointer fields distinguish "not configured"
// (nil, inherit global) from an explicit value; a zero threshold disables
// automatic compaction for that model (and its reminders).
type ModelCompactionConfig struct {
	Threshold *float64 `json:"threshold,omitempty" yaml:"threshold,omitempty"`
	Reminder  *float64 `json:"reminder,omitempty" yaml:"reminder,omitempty"`
}

// validCompactionFraction reports whether a configured compaction threshold or
// reminder line is usable: 0 is the documented "unset / disable" value and
// (0,1] the only meaningful usage-fraction range. NaN and ±Inf fail the
// comparisons and are rejected here so they cannot ride through to the
// runtime's threshold and reminder decisions, the TUI's context coloring, or
// the compaction logs.
func validCompactionFraction(v float64) bool {
	return v == 0 || (v > 0 && v <= 1)
}

// validCompactionReminder accepts the reminder line values: 0 (derive from
// the threshold), a usage fraction in (0,1], or -1 (explicitly disable the
// context-pressure reminder while keeping automatic compaction).
func validCompactionReminder(v float64) bool {
	return v == CompactionReminderDisabled || validCompactionFraction(v)
}

// collectCompactionConfigIssues reports compaction configurations that are
// invalid or silently ineffective, and resets the invalid values to their
// unset state so they behave as not configured. Two kinds of problems exist:
//
//   - An out-of-range threshold/reminder (negative, above 1, NaN, ±Inf) must
//     not ride through to the runtime: a negative or NaN threshold silently
//     disables automatic compaction, a value above 1 pushes the compaction
//     line past the usable budget so it never triggers, and NaN poisons every
//     comparison in the reminder and coloring paths. These are reset to the
//     built-in default (reminder 0 re-derives from the threshold).
//   - A reminder set against a disabled compaction (threshold 0) never fires —
//     threshold 0 disables both auto-compaction and reminders — so the
//     combination is reported so the user knows the reminder is dead. A
//     reminder at or above the threshold is *not* an issue: the reminder never
//     injects on its own once its resolved line sits at or above the
//     threshold, because the request that crosses the line and every
//     grace-deferred request already carry the "compaction imminent" notice
//     (or the externalization warning once the grace is spent) with the same
//     wrap-up and externalize instructions, so stacking the reminder would
//     only duplicate the prompt.
//
// Global compaction is checked directly; per-model compaction lives on the
// model definitions (ModelConfig.Compaction) and is checked in
// collectModelCompactionIssues.
func collectCompactionConfigIssues(cfg *Config) []string {
	comp := cfg.Context.Compaction
	var issues []string
	if !validCompactionFraction(comp.Threshold) {
		issues = append(issues, fmt.Sprintf("context.compaction.threshold must be 0 (disable automatic compaction) or a usage fraction in (0,1]; got %v, using the default %v", comp.Threshold, DefaultContextCompactUsage))
		comp.Threshold = DefaultContextCompactUsage
	}
	if !validCompactionReminder(comp.Reminder) {
		issues = append(issues, fmt.Sprintf("context.compaction.reminder must be 0 (derive from the threshold), -1 (disable the reminder), or a usage fraction in (0,1]; got %v, using the derived default", comp.Reminder))
		comp.Reminder = 0
	}
	cfg.Context.Compaction = comp
	if comp.Threshold <= 0 && comp.Reminder > 0 {
		issues = append(issues, "context.compaction.reminder is set but context.compaction.threshold is 0 (auto-compaction disabled); the reminder will never fire")
	}
	return issues
}

// collectModelCompactionIssues reports per-model compaction (ModelConfig.
// Compaction) problems and resets the invalid values. An out-of-range
// threshold or reminder (negative, above 1, NaN, ±Inf) is dropped so the model
// inherits the global line again instead of running its runtime on a broken
// value; a reminder set against a model threshold of 0 never fires (threshold
// 0 disables auto-compaction and reminders for that model), which is reported
// so the user fixes it on the model side. A model that overrides only its
// threshold inherits the global reminder; when the inherited line sits at or
// above the model's threshold the reminder never injects for that model, so
// the combination is reported as a silent footgun.
func collectModelCompactionIssues(cfg *Config) []string {
	var issues []string
	global := compThresholdConfig(cfg)
	for providerName, prov := range cfg.Providers {
		modelsChanged := false
		for modelID, mc := range prov.Models {
			if mc.Compaction == nil {
				continue
			}
			comp := mc.Compaction
			changed := false
			if comp.Threshold != nil && !validCompactionFraction(*comp.Threshold) {
				issues = append(issues, fmt.Sprintf("model %s/%s: compaction.threshold must be 0 (disable automatic compaction) or a usage fraction in (0,1]; got %v, inheriting the global value", providerName, modelID, *comp.Threshold))
				comp.Threshold = nil
				changed = true
			}
			if comp.Reminder != nil && !validCompactionReminder(*comp.Reminder) {
				issues = append(issues, fmt.Sprintf("model %s/%s: compaction.reminder must be 0 (derive from the threshold), -1 (disable the reminder), or a usage fraction in (0,1]; got %v, inheriting the global reminder", providerName, modelID, *comp.Reminder))
				comp.Reminder = nil
				changed = true
			}
			if changed {
				mc.Compaction = comp
				prov.Models[modelID] = mc
				modelsChanged = true
			}
			threshold := compThresholdForModel(global, mc)
			if threshold <= 0 && comp.Reminder != nil && *comp.Reminder > 0 {
				issues = append(issues, fmt.Sprintf("model %s/%s: compaction.reminder is set but its threshold is 0 (auto-compaction disabled); the reminder will never fire", providerName, modelID))
			}
			// Inheritance footgun: a model that overrides only its
			// threshold inherits the global reminder; when that line
			// sits at or above the model's own threshold the reminder
			// never injects for the model (the crossing and grace
			// requests carry the imminent notice instead), with no
			// other hint. A derived global reminder (Reminder 0)
			// cannot trap: its derived value is always below the
			// threshold. A model that disabled compaction (threshold
			// 0) is excluded — there is nothing to remind toward.
			if comp.Threshold != nil && comp.Reminder == nil && threshold > 0 && global.Reminder > 0 && global.Reminder >= threshold {
				issues = append(issues, fmt.Sprintf("model %s/%s: inherits context.compaction.reminder %v, which is at or above this model's compaction.threshold %v; the reminder never injects for this model — set a per-model reminder below the threshold or disable it with -1", providerName, modelID, global.Reminder, threshold))
			}
		}
		if modelsChanged {
			cfg.Providers[providerName] = prov
		}
	}
	return issues
}

// compThresholdConfig returns the effective global compaction threshold config.
func compThresholdConfig(cfg *Config) CompactionConfig {
	if cfg == nil {
		return CompactionConfig{}
	}
	return cfg.Context.Compaction
}

// compThresholdForModel resolves the effective auto-compaction threshold for a
// model definition: the model's own compaction.threshold when present, else
// the global one.
func compThresholdForModel(global CompactionConfig, mc ModelConfig) float64 {
	if mc.Compaction != nil && mc.Compaction.Threshold != nil {
		return *mc.Compaction.Threshold
	}
	return global.Threshold
}
