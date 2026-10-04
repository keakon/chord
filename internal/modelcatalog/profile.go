package modelcatalog

import (
	"fmt"
	"math"
)

// ConfigProfile contains the Chord configuration guidance that belongs to a
// model or route. Compat is deliberately represented as JSON-shaped data: the
// catalog must not import config, while Chord's strict config decoder remains
// the authority for the supported compat fields. Compaction is a recommendation
// and fills only unset model fields without overriding explicit global settings.
type ConfigProfile struct {
	// Model contains direct ModelConfig fields such as thinking, reasoning,
	// prompt_cache, text, store and parallel_tool_calls. It is kept as JSON
	// data to avoid making the catalog depend on config's Go types.
	Model      map[string]any     `json:"model,omitempty"`
	Compat     map[string]any     `json:"compat,omitempty"`
	Compaction *CompactionProfile `json:"compaction,omitempty"`
	Sources    []Source           `json:"sources,omitempty"`
}

// CompactionProfile contains model-level compaction hints, not the user's
// global context.compaction strategy.
type CompactionProfile struct {
	Threshold *float64 `json:"threshold,omitempty"`
	Reminder  *float64 `json:"reminder,omitempty"`
	Notes     string   `json:"notes,omitempty"`
}

func (p *ConfigProfile) validate(modelID string) error {
	if p == nil {
		return nil
	}
	for _, s := range p.Sources {
		if err := validateMetadataSource(s); err != nil {
			return fmt.Errorf("model %q config profile: %w", modelID, err)
		}
	}
	if err := validateProfileMap(modelID, "model", p.Model, profileModelFields); err != nil {
		return err
	}
	if p.Compaction == nil {
		return nil
	}
	c := p.Compaction
	if c.Threshold != nil && (math.IsNaN(*c.Threshold) || math.IsInf(*c.Threshold, 0) || *c.Threshold < 0 || *c.Threshold > 1) {
		return fmt.Errorf("model %q config profile: compaction threshold must be 0 or between 0 and 1", modelID)
	}
	if c.Reminder != nil && (*c.Reminder != -1 && (math.IsNaN(*c.Reminder) || math.IsInf(*c.Reminder, 0) || *c.Reminder < 0 || *c.Reminder > 1)) {
		return fmt.Errorf("model %q config profile: compaction reminder must be -1, 0, or between 0 and 1", modelID)
	}
	return nil
}

// Profiles may configure only model behavior, never connection or identity.
// Config's adapter checks nested fields and values using its actual schema.
var profileModelFields = map[string]bool{"thinking": true, "reasoning": true, "text": true, "parallel_tool_calls": true, "prompt_cache": true, "store": true}

func validateProfileMap(modelID, section string, values map[string]any, fields map[string]bool) error {
	for key := range values {
		if !fields[key] {
			return fmt.Errorf("model %q config profile: unknown %s field %q", modelID, section, key)
		}
	}
	return nil
}

// MergeConfigProfiles overlays route guidance over model guidance. Compat is
// merged recursively so a route can override one documented field without
// losing the model's other settings.
func MergeConfigProfiles(base, overlay *ConfigProfile) *ConfigProfile {
	if base == nil && overlay == nil {
		return nil
	}
	out := &ConfigProfile{}
	if base != nil {
		out.Model = cloneJSONMap(base.Model)
		out.Compat = cloneJSONMap(base.Compat)
		out.Compaction = cloneCompactionProfile(base.Compaction)
		out.Sources = append(out.Sources, base.Sources...)
	}
	if overlay == nil {
		return out
	}
	out.Model = mergeJSONMaps(out.Model, overlay.Model)
	out.Compat = mergeJSONMaps(out.Compat, overlay.Compat)
	if overlay.Compaction != nil {
		out.Compaction = cloneCompactionProfile(overlay.Compaction)
	}
	out.Sources = append(out.Sources, overlay.Sources...)
	return out
}

func cloneJSONMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	return cloneJSONValue(in).(map[string]any)
}
func mergeJSONMaps(dst, src map[string]any) map[string]any {
	if dst == nil && src == nil {
		return nil
	}
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			dm, _ := dst[k].(map[string]any)
			dst[k] = mergeJSONMaps(dm, sm)
		} else {
			dst[k] = cloneJSONValue(v)
		}
	}
	return dst
}
