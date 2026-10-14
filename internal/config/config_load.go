package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/keakon/golog/log"
	"gopkg.in/yaml.v3"
)

// LoadConfig loads configuration from the user-level config.yaml.
func LoadConfig() (*Config, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadConfigFromPath(configPath)
}

// LoadConfigFromPath loads configuration from an arbitrary YAML file path.
// The config is initialised with DefaultConfig() values before unmarshalling,
// so omitted YAML fields retain their defaults (e.g. compaction.threshold: 0.8).
func LoadConfigFromPath(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	layer, err := inspectKeymapLayer(path, data, OriginLayerGlobal)
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfigData(path, data, true, nil)
	if err != nil {
		return nil, err
	}
	rc := &ResolvedConfig{}
	rc.addKeymapLayer(layer)
	logKeymapDiagnostics(keymapDiagnostics(cfg, rc.Index, []keymapLayer{layer}, nil))
	return cfg, nil
}

// decodeStrict decodes data into cfg with strict rules (unknown fields are
// rejected). It returns the accumulated field-level type errors, or a fatal
// error when the YAML itself is malformed and cannot be partially decoded. A
// fatal error must prevent startup; field-level errors are logged and the
// affected fields keep their defaults.
func decodeStrict(data []byte, cfg *Config) ([]string, error) {
	if err := validateMCPConfigYAML(data); err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	err := dec.Decode(cfg)
	if err == nil || errors.Is(err, io.EOF) {
		// An empty or fully commented-out file has no YAML document; Decode
		// reports io.EOF where Unmarshal used to be a no-op. Not an issue.
		return nil, nil
	}
	if typeErr, ok := errors.AsType[*yaml.TypeError](err); ok {
		return typeErr.Errors, nil
	}
	return nil, err
}

// collectSemanticIssues validates constraints YAML decoding cannot express and
// resets offending values to their unset state, so an invalid value behaves as
// not configured instead of riding through to the runtime.
func collectSemanticIssues(cfg *Config) []string {
	var issues []string
	for providerName, providerCfg := range cfg.Providers {
		issues = append(issues, collectProviderIssues(providerName, &providerCfg)...)
		cfg.Providers[providerName] = resetInvalidProviderFields(providerCfg)
	}
	issues = append(issues, collectDiagnosticsConfigIssues(cfg)...)
	resetInvalidDiagnosticsFields(&cfg.Diagnostics)
	issues = append(issues, collectCompactionConfigIssues(cfg)...)
	issues = append(issues, collectModelCompactionIssues(cfg)...)
	if cfg.QuestionAutoSelectTimeout < 0 {
		issues = append(issues, "question_auto_select_timeout must be non-negative; using 0")
		cfg.QuestionAutoSelectTimeout = 0
	}
	if cfg.QuestionTimeout < 0 {
		issues = append(issues, fmt.Sprintf("question_timeout must be a non-negative number of seconds (0 = wait indefinitely); got %d, using 0", cfg.QuestionTimeout))
		cfg.QuestionTimeout = 0
	}
	return issues
}

// loadConfigData loads configuration from raw YAML bytes. Malformed YAML is a
// fatal error; unknown keys, wrong types, and semantically invalid values are
// logged and treated as not configured while valid siblings still apply.
// Contradictory orchestration clocks (an explicitly configured
// waiting_main_max_wait_sec below waiting_main_min_wait_sec) are a fatal
// error too: the effective-value layer would otherwise have to silently
// rewrite one of the two explicit values.
func loadConfigData(path string, data []byte, withDefaults bool, diagnostics *[]Diagnostic) (*Config, error) {
	cfg := &Config{}
	if withDefaults {
		cfg = DefaultConfig()
	}
	terrors, err := decodeStrict(data, cfg)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	locator := newTypeIssueLocator(data, len(terrors) > 0 && diagnostics != nil)
	for _, issue := range terrors {
		log.Warnf("config %s: ignoring invalid value: %s", path, issue)
		appendLoadPathDiagnostic(diagnostics, path, locator.path(issue), issue, "inherited/default value")
	}
	if err := orchestrationConfigLoadError(cfg); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if err := ValidateMCPAuth(cfg.MCP); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	for name, server := range cfg.MCP {
		cfg.MCP[name], _ = NormalizeMCPAuth(server)
	}
	for _, issue := range collectSemanticIssues(cfg) {
		log.Warnf("config %s: ignoring invalid value(s): %s", path, issue)
		appendLoadDiagnostic(diagnostics, path, "", issue, "inherited/default value")
	}
	if withDefaults {
		if err := cfg.ImageGeneration.Validate(cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
	}

	if withDefaults {
		if _, err := cfg.InputHistory.Effective(); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
	}
	normalizeModelLimits(cfg)
	return cfg, nil
}
