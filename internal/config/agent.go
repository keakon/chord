package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentConfig holds agent-specific configuration loaded from .chord/agents/*.
//
// Supported formats:
//   - .md: YAML frontmatter + Markdown body, where the body becomes SystemPrompt.
//   - .yaml/.yml: plain YAML document; optional prompt/system_prompt fields become SystemPrompt.
type AgentConfig struct {
	Name             string           `json:"name" yaml:"name"`
	Description      string           `json:"description" yaml:"description"`
	Mode             string           `json:"mode" yaml:"mode"`
	Models           []string         `json:"models" yaml:"models"`
	Temperature      float64          `json:"temperature" yaml:"temperature"`
	MaxTokens        int              `json:"max_tokens" yaml:"max_tokens"`
	Variant          string           `json:"variant,omitempty" yaml:"variant,omitempty"`
	Color            string           `json:"color,omitempty" yaml:"color,omitempty"`
	Capabilities     []string         `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	PreferredTasks   []string         `json:"preferred_tasks,omitempty" yaml:"preferred_tasks,omitempty"`
	WriteMode        string           `json:"write_mode,omitempty" yaml:"write_mode,omitempty"`
	DelegationPolicy string           `json:"delegation_policy,omitempty" yaml:"delegation_policy,omitempty"`
	Permission       yaml.Node        `json:"-" yaml:"permission"`
	MCP              MCPConfig        `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	Delegation       DelegationConfig `json:"delegation,omitempty" yaml:"delegation,omitempty"`
	Prompt           string           `json:"-" yaml:"prompt,omitempty"`
	PromptAlt        string           `json:"-" yaml:"system_prompt,omitempty"`
	SystemPrompt     string           `json:"-" yaml:"-"`
}

type DelegationConfig struct {
	MaxChildren int   `json:"max_children,omitempty" yaml:"max_children,omitempty"`
	MaxDepth    int   `json:"max_depth,omitempty" yaml:"max_depth,omitempty"`
	ChildJoin   *bool `json:"child_join,omitempty" yaml:"child_join,omitempty"`
}

const (
	DefaultDelegationMaxChildren = 10
	DefaultDelegationMaxDepth    = 1
)

func (c DelegationConfig) EffectiveMaxChildren() int {
	if c.MaxChildren > 0 {
		return c.MaxChildren
	}
	return DefaultDelegationMaxChildren
}

func (c DelegationConfig) EffectiveMaxDepth() int {
	if c.MaxDepth > 0 {
		return c.MaxDepth
	}
	return DefaultDelegationMaxDepth
}

func (c DelegationConfig) ChildJoinEnabled() bool {
	if c.ChildJoin == nil {
		return true
	}
	return *c.ChildJoin
}

// IsSubAgent reports whether this agent is intended for use as a SubAgent.
// Agents default to subagent mode when Mode is empty.
func (c *AgentConfig) IsSubAgent() bool {
	return c.Mode == "" || c.Mode == "subagent"
}

// ParseModelRef splits a model reference into its base ref and optional variant.
// Format: "provider/model-id[@variant]" or "model-id[@variant]".
// If no @ is present, variant is empty.
func ParseModelRef(s string) (ref, variant string) {
	if i := strings.LastIndex(s, "@"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// NormalizeModelRef removes any inline @variant suffix from a model reference.
func NormalizeModelRef(s string) string {
	ref, _ := ParseModelRef(s)
	return ref
}

// LoadAgentConfig loads a single agent configuration from a Markdown or YAML file.
// Markdown files use frontmatter + body; YAML files use a plain YAML document.
func LoadAgentConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent config %s: %w", path, err)
	}

	ext := strings.ToLower(filepath.Ext(path))
	var cfg *AgentConfig
	switch ext {
	case ".md":
		cfg, err = loadMarkdownAgentConfig(path, data)
	case ".yaml", ".yml":
		cfg, err = loadYAMLAgentConfig(path, data)
	default:
		return nil, fmt.Errorf("agent config %s: unsupported file extension %q", path, ext)
	}
	if err != nil {
		return nil, err
	}
	return finalizeAgentConfig(path, cfg)
}

func loadMarkdownAgentConfig(path string, data []byte) (*AgentConfig, error) {
	if !bytes.HasPrefix(data, []byte("---")) {
		return nil, fmt.Errorf("agent config %s: missing frontmatter opening delimiter", path)
	}

	var cfg AgentConfig
	var body string

	// Frontmatter format: split on second "---".
	rest := data[3:] // skip leading "---"
	// Skip optional newline after opening "---"
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}
	idx := bytes.Index(rest, []byte("\n---"))
	if idx < 0 {
		return nil, fmt.Errorf("agent config %s: missing frontmatter closing delimiter", path)
	}
	frontmatter := rest[:idx]
	if err := yaml.Unmarshal(frontmatter, &cfg); err != nil {
		return nil, fmt.Errorf("parse agent config frontmatter %s: %w", path, err)
	}
	// Everything after the closing "---\n" is the body.
	bodyRaw := rest[idx+4:] // skip "\n---"
	if len(bodyRaw) > 0 && bodyRaw[0] == '\n' {
		bodyRaw = bodyRaw[1:]
	} else if len(bodyRaw) > 1 && bodyRaw[0] == '\r' && bodyRaw[1] == '\n' {
		bodyRaw = bodyRaw[2:]
	}
	body = strings.TrimSpace(string(bodyRaw))

	cfg.SystemPrompt = body
	return &cfg, nil
}

func loadYAMLAgentConfig(path string, data []byte) (*AgentConfig, error) {
	var cfg AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse agent config yaml %s: %w", path, err)
	}
	if cfg.Prompt != "" {
		cfg.SystemPrompt = strings.TrimSpace(cfg.Prompt)
	} else if cfg.PromptAlt != "" {
		cfg.SystemPrompt = strings.TrimSpace(cfg.PromptAlt)
	}
	return &cfg, nil
}

func finalizeAgentConfig(path string, cfg *AgentConfig) (*AgentConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("agent config %s: empty config", path)
	}

	// Default name to filename (without extension) if not set in YAML.
	if cfg.Name == "" {
		base := filepath.Base(path)
		cfg.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	// SubAgent must declare an explicit models list so the fallback chain is deterministic.
	// Primary agents may omit models and fall back to all configured providers.
	mode := strings.ToLower(cfg.Mode)
	if (mode == "subagent" || mode == "") && len(cfg.Models) == 0 {
		return nil, fmt.Errorf("agent config %s: subagent must define at least one model in 'models'", path)
	}

	return cfg, nil
}

// LoadAgentConfigs loads all agent configurations from a directory.
// It reads every *.yaml and *.yml file in dir and returns them keyed by agent name.
// Returns an empty (non-nil) map if the directory does not exist or is empty.
func LoadAgentConfigs(dir string) (map[string]*AgentConfig, error) {
	configs := make(map[string]*AgentConfig)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return configs, nil
		}
		return nil, fmt.Errorf("read agent config directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" && ext != ".md" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		cfg, err := LoadAgentConfig(path)
		if err != nil {
			return nil, err
		}

		configs[cfg.Name] = cfg
	}

	return configs, nil
}

// ---------------------------------------------------------------------------
// Built-in default agents
// ---------------------------------------------------------------------------

// BuiltinAgentConfigs returns the hardcoded default agent configurations.
// These serve as fallbacks when no user-defined YAML exists for the agent.
// Users can override any built-in by placing a same-named YAML file in
// <config-home>/agents/ (global) or .chord/agents/ (project).
func BuiltinAgentConfigs() map[string]*AgentConfig {
	return map[string]*AgentConfig{
		"planner": DefaultPlannerAgent(),
		"builder": DefaultBuilderAgent(),
	}
}

// DefaultPlannerAgent returns the built-in planner agent configuration.
// The planner agent is specialised for codebase exploration and plan generation.
// It is read-heavy by default: Read/Grep/Glob are allowed, plan-file writes ask,
// and Bash also asks so the prompt/runtime boundary stays conservative.
// It has access to Handoff to signal plan completion.
func DefaultPlannerAgent() *AgentConfig {
	// Build permission node: read-heavy, free exploration.
	permYAML := `
"*": deny
Read: allow
Grep: allow
Glob: allow
Bash: ask
Write: ask
Edit: ask
Handoff: allow
Skill: allow
Question: allow
`
	var permNode yaml.Node
	_ = yaml.Unmarshal([]byte(permYAML), &permNode)

	// permNode is a document node wrapping the mapping; extract the inner node.
	inner := permNode
	if permNode.Kind == yaml.DocumentNode && len(permNode.Content) > 0 {
		inner = *permNode.Content[0]
	}

	return &AgentConfig{
		Name:        "planner",
		Description: "Planning agent for requirement analysis, codebase exploration, and task decomposition. Explores the codebase, creates a plan document, and calls Handoff when done.",
		Mode:        "primary",
		Permission:  inner,
	}
}

// DefaultBuilderAgent returns the built-in builder agent configuration.
// The builder agent is the MainAgent's default role — the primary agent the user
// interacts with for coding tasks. Its permissions are from the MainAgent's
// perspective (Delegate, TodoWrite) not SubAgent's (Complete, Escalate, Notify).
//
// When this config is used to create SubAgents via the Delegate tool, the system
// automatically adapts: MainAgent-only tools are removed and SubAgent-specific
// tools (Complete, Escalate, Notify) are added.
func DefaultBuilderAgent() *AgentConfig {
	permYAML := `
"*": allow
Read: allow
Write: ask
Edit: ask
Grep: allow
Glob: allow
Bash: ask
Task: allow
TodoWrite: allow
Skill: allow
Question: allow
Handoff: deny
`
	var permNode yaml.Node
	_ = yaml.Unmarshal([]byte(permYAML), &permNode)

	inner := permNode
	if permNode.Kind == yaml.DocumentNode && len(permNode.Content) > 0 {
		inner = *permNode.Content[0]
	}

	return &AgentConfig{
		Name:        "builder",
		Description: "General-purpose coding agent — the default MainAgent role for implementing features, fixing bugs, writing tests, and refactoring code.",
		Mode:        "primary",
		Permission:  inner,
	}
}

// ResolveAgentConfigs merges agent configs from multiple sources with priority:
// builtIn (lowest) → global (<config-home>/agents/) → project (.chord/agents/) (highest).
// Later entries override earlier ones by name.
func ResolveAgentConfigs(projectDir, globalDir string) (map[string]*AgentConfig, error) {
	// Start with built-in defaults.
	merged := BuiltinAgentConfigs()

	// Layer global user configs.
	globalConfigs, err := LoadAgentConfigs(globalDir)
	if err != nil {
		return nil, fmt.Errorf("load global agent configs: %w", err)
	}
	for name, cfg := range globalConfigs {
		merged[name] = cfg
	}

	// Layer project-level configs (highest priority).
	projectConfigs, err := LoadAgentConfigs(projectDir)
	if err != nil {
		return nil, fmt.Errorf("load project agent configs: %w", err)
	}
	for name, cfg := range projectConfigs {
		merged[name] = cfg
	}

	return merged, nil
}
