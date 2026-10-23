package config

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/externalclaude"
)

// External backend selection for subagent definitions.
const (
	BackendKindClaudeCode = "claude-code"

	DefaultClaudeCodeCommand = "claude"
)

// BackendConfig makes a subagent definition execute on an external coding-agent
// process instead of Chord's own LLM loop. The external process owns its model
// selection and its own tool execution; Chord keeps the task record, mailbox,
// permission decisions and completion envelope.
type BackendConfig struct {
	Kind       string            `json:"kind,omitempty" yaml:"kind,omitempty"`
	Command    string            `json:"command,omitempty" yaml:"command,omitempty"`
	LaunchArgs []string          `json:"launch_args,omitempty" yaml:"launch_args,omitempty"`
	Env        map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
}

// errBackendMustBeMapping is shared by the agent-level presence scan and the
// backend decoder so both report one wording for a non-mapping backend node.
func errBackendMustBeMapping() error {
	return fmt.Errorf("backend must be a mapping")
}

func (b *BackendConfig) UnmarshalYAML(node *yaml.Node) error {
	allowed := map[string]bool{"kind": true, "command": true, "launch_args": true, "env": true}
	if node.Kind != yaml.MappingNode {
		return errBackendMustBeMapping()
	}
	entries, err := YAMLMappingEntries(node)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !allowed[entry.Key] {
			return fmt.Errorf("unsupported backend field %s", entry.Key)
		}
	}
	type plain BackendConfig
	return node.Decode((*plain)(b))
}

// backendAgentConflict is an agent field an external backend must not declare.
// One declaration covers two checks because the two entry points see different
// shapes: the decoder sees the field name before any value exists, so an
// explicit zero (temperature: 0, mcp: {}) still counts as a declaration, while
// validation only sees decoded values and judges them with Declared. Both
// report Message, so the rule and its wording stay in one place.
type backendAgentConflict struct {
	fields   []string
	message  string
	declared func(*AgentConfig) bool
}

var backendAgentConflicts = []backendAgentConflict{
	{[]string{"model_pools"},
		"backend agents must not declare model_pools; configure the external command and its launch_args instead",
		func(c *AgentConfig) bool { return len(c.ModelPools) > 0 }},
	{[]string{"mcp"},
		"backend agents must not declare mcp; external MCP execution is not supported by the managed transport",
		func(c *AgentConfig) bool { return len(c.MCP) > 0 }},
	{[]string{"prompt_preset"},
		"backend agents must not declare prompt_preset; the external agent owns its own prompt",
		func(c *AgentConfig) bool { return strings.TrimSpace(c.PromptPreset) != "" }},
	{[]string{"variant", "temperature", "max_tokens"},
		"backend agents must not declare variant, temperature or max_tokens; the external agent owns its model settings",
		func(c *AgentConfig) bool { return c.Variant != "" || c.Temperature != 0 || c.MaxTokens != 0 }},
}

// backendAgentConflictFor reports the conflict a declared agent field has with
// an external backend, or false when the field may be declared.
func backendAgentConflictFor(field string) (backendAgentConflict, bool) {
	for _, conflict := range backendAgentConflicts {
		if slices.Contains(conflict.fields, field) {
			return conflict, true
		}
	}
	return backendAgentConflict{}, false
}

// IsExternalBackend reports whether this agent runs on an external backend.
func (c *AgentConfig) IsExternalBackend() bool {
	return c != nil && c.Backend != nil && strings.TrimSpace(c.Backend.Kind) != ""
}

func (b *BackendConfig) EffectiveCommand() string {
	if b == nil {
		return DefaultClaudeCodeCommand
	}
	if command := strings.TrimSpace(b.Command); command != "" {
		return command
	}
	return DefaultClaudeCodeCommand
}

// validateAgentBackend rejects backend declarations that would silently do
// nothing, and flags that would take a reserved protocol decision away from
// Chord.
func validateAgentBackend(path string, cfg *AgentConfig) error {
	if cfg == nil || cfg.Backend == nil {
		return nil
	}
	backend := cfg.Backend
	kind := strings.TrimSpace(backend.Kind)
	if kind == "" {
		return fmt.Errorf("agent config %s: backend.kind is required", path)
	}
	if kind != BackendKindClaudeCode {
		return fmt.Errorf("agent config %s: unknown backend.kind %q; expected %s", path, backend.Kind, BackendKindClaudeCode)
	}
	if !cfg.IsSubAgent() {
		return fmt.Errorf("agent config %s: backend is only supported for subagent definitions; set mode: %s", path, AgentModeSubAgent)
	}

	for _, conflict := range backendAgentConflicts {
		if conflict.declared(cfg) {
			return fmt.Errorf("agent config %s: %s", path, conflict.message)
		}
	}

	if command := strings.TrimSpace(backend.Command); strings.ContainsRune(command, 0) {
		return fmt.Errorf("agent config %s: backend.command must not contain NUL", path)
	}

	for key := range backend.Env {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("agent config %s: backend.env key must not be empty", path)
		}
		if strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("agent config %s: backend.env key %q must not contain '=' or NUL", path, key)
		}
	}

	if err := externalclaude.ValidateLaunchArgs(backend.LaunchArgs); err != nil {
		return fmt.Errorf("agent config %s: backend.launch_args: %w", path, err)
	}
	if err := externalclaude.ValidateEnvironment(backend.Env); err != nil {
		return fmt.Errorf("agent config %s: backend.env: %w", path, err)
	}
	return nil
}
