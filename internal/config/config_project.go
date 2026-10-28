package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/golog/log"
	"gopkg.in/yaml.v3"
)

// ProjectConfigPath returns the project-local config path under .chord/.
func ProjectConfigPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".chord", "config.yaml")
}

// MergeProjectConfig overlays a project-level .chord/config.yaml onto an
// already-loaded global config. Missing configs are ignored. Malformed YAML is
// a fatal error: it prevents startup rather than silently dropping the file.
// Invalid field-level values are stripped before the merge so they fall back to
// the inherited global/default value instead of clobbering it; only keys
// documented as project-scoped participate. Global-only keys such as paths.*
// and maintenance.* are intentionally ignored here.
func MergeProjectConfig(base *Config, path string) (projectCfg *Config, merged *Config, err error) {
	if strings.TrimSpace(path) == "" {
		return nil, base, nil
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, base, nil
		}
		return nil, nil, fmt.Errorf("read config %s: %w", path, readErr)
	}
	layer, err := inspectKeymapLayer(path, data, OriginLayerProject)
	if err != nil {
		return nil, nil, err
	}
	cleanedData, drops, err := stripTypeInvalidOverride(path, data)
	if err != nil {
		return nil, nil, err
	}
	for _, drop := range drops {
		log.Warnf("config %s: ignoring invalid value: %s", path, strings.Join(drop.Segments, "."))
	}
	projectCfg, err = loadConfigData(path, cleanedData, false, nil)
	if err != nil {
		return nil, nil, err
	}
	merged, err = mergeConfigOverrideData(base, cleanedData, path, nil)
	if err != nil {
		return nil, nil, err
	}
	for _, diag := range ApplyEmulation(merged) {
		log.Warnf("config %s: ignoring invalid value: %s", path, diag.Message)
	}
	rc := &ResolvedConfig{}
	rc.addKeymapLayer(layer)
	var inherited KeyMapConfig
	if base != nil {
		inherited = base.KeyMap
	}
	logKeymapDiagnostics(keymapDiagnostics(merged, rc.Index, []keymapLayer{layer}, inherited))
	return projectCfg, merged, nil
}

var projectScopedTopLevelKeys = map[string]bool{
	"image_generation":                true,
	"providers":                       true,
	"model_pools":                     true,
	"orchestration":                   true,
	"context":                         true,
	"thinking_translation":            true,
	"skills":                          true,
	"confirm_timeout":                 true,
	"question_timeout":                true,
	"question_auto_select_timeout":    true,
	"diff":                            true,
	"desktop_notification":            true,
	"desktop_notification_foreground": true,
	"prevent_sleep":                   true,
	"keymap":                          true,
	"commands":                        true,
	"ime_switch_target":               true,
	"log_level":                       true,
	"lsp":                             true,
	"mcp":                             true,
	"hooks":                           true,
	"max_output_tokens":               true,
	"stream_retry_rounds":             true,
	"proxy":                           true,
	"web_fetch":                       true,
	"hosted_tools":                    true,
	"worktree":                        true,
	"memory":                          true,
	"emulation":                       true,
}

// projectIgnoredTopLevelKeys are valid global config keys that are
// deliberately ignored in project config (documented in docs/configuration.md);
// anything else outside projectScopedTopLevelKeys is logged and dropped.
var projectIgnoredTopLevelKeys = map[string]bool{
	"input_history":   true,
	"paths":           true,
	"maintenance":     true,
	"model_templates": true,
	"diagnostics":     true,
}

// unsupportedProjectTopLevelKeys returns the top-level override keys that are
// neither project-scoped nor deliberately ignored at the project layer. The
// loader logs and drops them; `chord doctor config` reports them.
func unsupportedProjectTopLevelKeys(overrideMap map[string]any) []string {
	var keys []string
	for key := range overrideMap {
		if !projectScopedTopLevelKeys[key] && !projectIgnoredTopLevelKeys[key] {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func mergeConfigOverrideData(base *Config, overrideData []byte, overridePath string, diagnostics *[]Diagnostic) (*Config, error) {
	if _, err := inspectKeymapLayer(overridePath, overrideData, OriginLayerProject); err != nil {
		return nil, err
	}
	baseMap, err := configToYAMLMap(base)
	if err != nil {
		return nil, err
	}
	overrideMap, err := decodeConfigOverrideMap(overrideData)
	if err != nil {
		log.Warnf("config %s: ignoring invalid project config: %v", overridePath, err)
		return base, nil
	}
	// Keys outside both sets are not recognized at the project layer; log and
	// drop them so the rest of the override still applies.
	for _, key := range unsupportedProjectTopLevelKeys(overrideMap) {
		log.Warnf("config %s: ignoring unsupported project field %q", overridePath, key)
		appendLoadDiagnostic(diagnostics, overridePath, key, fmt.Sprintf("field %q is not supported in project config", key), "field ignored")
		delete(overrideMap, key)
	}
	normalizeContextReductionOverride(baseMap, overrideMap)
	// Semantically invalid leaves must not clobber the global value they
	// overlay. Evaluate the merged candidate, strip exactly those leaves from
	// the override, and re-merge until nothing invalid remains.
	mergedData, err := marshalSanitizedMerge(baseMap, overrideMap, overridePath, diagnostics)
	if err != nil {
		return nil, err
	}
	return loadConfigData(overridePath, mergedData, false, diagnostics)
}

// maxOverrideSanitizeRounds bounds how often invalid leaves can be stripped
// before giving up; each round removes at least one leaf, so real configs
// converge in one or two.
const maxOverrideSanitizeRounds = 4

func marshalSanitizedMerge(baseMap, overrideMap map[string]any, path string, diagnostics *[]Diagnostic) ([]byte, error) {
	for range maxOverrideSanitizeRounds {
		trial := cloneYAMLValue(baseMap).(map[string]any)
		mergeProjectConfigMap(trial, overrideMap, nil)
		trialData, err := yaml.Marshal(trial)
		if err != nil {
			return nil, fmt.Errorf("marshal merged config %s: %w", path, err)
		}
		badPaths, ferr := semanticInvalidOverridePaths(trialData)
		if ferr != nil {
			return nil, fmt.Errorf("evaluate merged config %s: %w", path, ferr)
		}
		var removable [][]string
		for _, p := range badPaths {
			if mapHasPath(overrideMap, p) {
				removable = append(removable, p)
			}
		}
		if len(removable) == 0 {
			return trialData, nil
		}
		for _, p := range removable {
			removeMapPath(overrideMap, p)
			log.Warnf("config %s: ignoring semantically invalid override value: %s", path, strings.Join(p, "."))
			appendLoadPathDiagnostic(diagnostics, path, p, "ignoring semantically invalid override value", "inherited global/default value")
		}
	}
	return nil, fmt.Errorf("config %s: too many invalid project values to sanitize", path)
}

// semanticInvalidOverridePaths evaluates a merged candidate config and returns
// the paths of leaves that violate semantic validation (invalid retry/key
// settings, out-of-range diagnostics values, orchestration scalar leaves that
// a project override wrote as zero or negative, or out-of-range compaction
// fractions). Values that only fail because of
// cross-layer inheritance (for example key_order=smart without a codex preset
// in the same override) are intentionally left in place: the final decode
// resets them against the fully merged preset instead of guessing here.
func semanticInvalidOverridePaths(data []byte) ([][]string, error) {
	cfg := &Config{}
	if _, err := decodeStrict(data, cfg); err != nil {
		return nil, err
	}
	var paths [][]string
	for name, p := range cfg.Providers {
		base := []string{"providers", name}
		v := strings.TrimSpace(p.RetryBackoff)
		if v != "" && !validRetryBackoff(v) {
			paths = append(paths, append(slices.Clip(base), "retry_backoff"))
		}
		if p.RetryDelayMS != nil && !validRetryDelayMS(*p.RetryDelayMS) {
			paths = append(paths, append(slices.Clip(base), "retry_delay_ms"))
		}
		if p.RetryAfterMaxS != nil && !validRetryAfterMaxS(*p.RetryAfterMaxS) {
			paths = append(paths, append(slices.Clip(base), "retry_after_max_s"))
		}
		v = strings.TrimSpace(p.KeyRotation)
		if v != "" && !validKeyRotation(v) {
			paths = append(paths, append(slices.Clip(base), "key_rotation"))
		}
		v = strings.TrimSpace(p.KeyOrder)
		if v != "" && v != KeyOrderSequential && v != KeyOrderRandom && v != KeyOrderSmart {
			paths = append(paths, append(slices.Clip(base), "key_order"))
		}
	}
	for _, issue := range collectDiagnosticIssues(cfg) {
		paths = append(paths, strings.Split(issue.Path, "."))
	}
	// Compaction threshold/reminder leaves are validated the same way so an
	// out-of-range project value (including NaN/±Inf) falls back to the global
	// line it overlays instead of clobbering it. The dead-reminder combination
	// (threshold 0 + reminder) is deliberately not stripped: both values are
	// individually valid, and removing either leaf would silently override the
	// user's explicit disable intent.
	comp := cfg.Context.Compaction
	if !validCompactionFraction(comp.Threshold) {
		paths = append(paths, []string{"context", "compaction", "threshold"})
	}
	if !validCompactionReminder(comp.Reminder) {
		paths = append(paths, []string{"context", "compaction", "reminder"})
	}
	for name, p := range cfg.Providers {
		for modelName, mc := range p.Models {
			if mc.Compaction == nil {
				continue
			}
			base := []string{"providers", name, "models", modelName, "compaction"}
			if mc.Compaction.Threshold != nil && !validCompactionFraction(*mc.Compaction.Threshold) {
				paths = append(paths, append(slices.Clip(base), "threshold"))
			}
			if mc.Compaction.Reminder != nil && !validCompactionReminder(*mc.Compaction.Reminder) {
				paths = append(paths, append(slices.Clip(base), "reminder"))
			}
		}
	}
	// Orchestration scalar leaves: only positive project scalar values override
	// the corresponding global values, so a project leaf that merged to zero or
	// below must be stripped for the global line it overlays to survive. The
	// waiting_main clock pair is deliberately not listed here:
	// an inversion is a contradiction between two individually valid leaves
	// that cannot be fixed by dropping either one, so it fails the final merge
	// decode instead of guessing a side.
	for _, leaf := range orchestrationScalarLeafValues(cfg.Orchestration) {
		if leaf.value <= 0 {
			paths = append(paths, leaf.path)
		}
	}
	return paths, nil
}

func mapHasPath(m map[string]any, path []string) bool {
	cur := m
	for i, seg := range path {
		child, ok := cur[seg]
		if !ok {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		next, ok := child.(map[string]any)
		if !ok {
			return false
		}
		cur = next
	}
	return false
}

func removeMapPath(m map[string]any, path []string) {
	cur := m
	for i, seg := range path {
		child, ok := cur[seg]
		if !ok {
			return
		}
		if i == len(path)-1 {
			delete(cur, seg)
			return
		}
		next, ok := child.(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
}

// normalizeContextReductionOverride resolves the context.reduction boolean
// shorthand in a project override before the map-level merge: `true` means
// "keep the base tuning" and is dropped so the base mapping survives, while
// `false` is left in place and disables reduction after the merged re-parse.
func normalizeContextReductionOverride(base, override map[string]any) {
	contextRaw, ok := override["context"].(map[string]any)
	if !ok {
		return
	}
	if enabled, ok := contextRaw["reduction"].(bool); ok && enabled {
		baseContext, _ := base["context"].(map[string]any)
		if _, baseIsMapping := baseContext["reduction"].(map[string]any); baseIsMapping {
			delete(contextRaw, "reduction")
		}
	}
}

func configToYAMLMap(cfg *Config) (map[string]any, error) {
	if cfg == nil {
		return map[string]any{}, nil
	}
	// Derived context totals are recomputed on every load; marshaling them as
	// explicit values would freeze a stale sum when a project override changes
	// input or output. Strip them so the merged re-parse re-derives.
	stripped := *cfg
	// Model templates are a pure anchor namespace: every decoded value has
	// already been materialized, project configs cannot reference anchors
	// across documents (anchors are document-scoped), and nothing reads the
	// namespace directly. Re-emitting it orders the template mapping by key,
	// which can place an alias before the anchor it references and fail the
	// merged re-parse with an unknown-anchor error; drop it instead.
	stripped.ModelTemplates = nil
	if len(cfg.Providers) > 0 {
		stripped.Providers = make(map[string]ProviderConfig, len(cfg.Providers))
		for providerName, provider := range cfg.Providers {
			if len(provider.Models) > 0 {
				models := make(map[string]ModelConfig, len(provider.Models))
				for modelName, model := range provider.Models {
					if model.Limit.ContextDerived {
						model.Limit.Context = 0
						model.Limit.ContextDerived = false
					}
					models[modelName] = model
				}
				provider.Models = models
			}
			stripped.Providers[providerName] = provider
		}
	}
	data, err := yaml.Marshal(&stripped)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

func mergeProjectConfigMap(dst, src map[string]any, path []string) {
	if dst == nil || src == nil {
		return
	}
	for key, raw := range src {
		if len(path) == 0 && !projectScopedTopLevelKeys[key] {
			continue
		}
		if len(path) == 1 && path[0] == "skills" && key == "paths" {
			dst[key] = appendYAMLSequences(dst[key], raw)
			continue
		}
		if len(path) == 1 && path[0] == "hooks" {
			dst[key] = appendYAMLSequences(dst[key], raw)
			continue
		}
		// An MCP server definition is a single connection/permission unit.
		// Mixing fields from global and project layers can combine incompatible
		// transports or unintentionally inherit credentials and tool access.
		if len(path) == 1 && path[0] == "mcp" {
			dst[key] = cloneYAMLValue(raw)
			continue
		}
		if childDst, ok := dst[key].(map[string]any); ok {
			if childSrc, ok := raw.(map[string]any); ok {
				mergeProjectConfigMap(childDst, childSrc, append(path, key))
				dst[key] = childDst
				continue
			}
		}
		dst[key] = cloneYAMLValue(raw)
	}
}

func appendYAMLSequences(base, extra any) any {
	baseSeq, _ := base.([]any)
	extraSeq, ok := extra.([]any)
	if !ok {
		return cloneYAMLValue(extra)
	}
	out := make([]any, 0, len(baseSeq)+len(extraSeq))
	for _, item := range baseSeq {
		out = append(out, cloneYAMLValue(item))
	}
	for _, item := range extraSeq {
		out = append(out, cloneYAMLValue(item))
	}
	return out
}

func cloneYAMLValue(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(vv))
		for k, child := range vv {
			cloned[k] = cloneYAMLValue(child)
		}
		return cloned
	case []any:
		cloned := make([]any, len(vv))
		for i, child := range vv {
			cloned[i] = cloneYAMLValue(child)
		}
		return cloned
	default:
		return vv
	}
}
