package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentConfig holds agent-specific configuration loaded from .chord/agents/*.
//
// Supported formats:
//   - .md: YAML frontmatter + Markdown body, where the body becomes SystemPrompt.
//   - .yaml/.yml: plain YAML document; optional prompt/system_prompt fields become SystemPrompt.
type AgentConfig struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Mode        string `json:"mode" yaml:"mode"`
	// poolOrder preserves the user-declared pool ordering when the agent uses
	// model_pools: [...]. It is not serialized and may be nil.
	poolOrder []string `json:"-" yaml:"-"`
	// Models holds resolved model pool definitions after resolving ModelPools against
	// config.yaml's top-level model_pools.
	//
	// This field is runtime-facing and is populated by ResolveAgentModelPools.
	// Agent YAML must not define inline models.
	Models map[string][]string `json:"models,omitempty" yaml:"models,omitempty"`
	// ModelPools is the user-facing agent configuration: an ordered list of pool names
	// to look up in config.yaml's top-level model_pools.
	ModelPools       []string         `json:"model_pools,omitempty" yaml:"model_pools,omitempty"`
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

	AgentModeMain          = "main"
	AgentModeSubAgent      = "subagent"
	AgentModeSubAgentSnake = "sub_agent"
	AgentModeSubAgentShort = "sub"
)

func isAgentModeSubAgent(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case AgentModeSubAgent, AgentModeSubAgentSnake, AgentModeSubAgentShort:
		return true
	default:
		return false
	}
}

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
// Only subagent modes enable SubAgent behavior; empty and unknown modes are treated as main mode for compatibility.
func (c *AgentConfig) IsSubAgent() bool {
	return isAgentModeSubAgent(c.Mode)
}

// PoolNames returns the ordered list of model pool names defined in this agent config.
//
// When the agent config went through ResolveAgentModelPools, the user-declared
// model_pools order in YAML is preserved. For configs constructed directly
// (e.g. in tests), where poolOrder is unset, names are returned in sorted order
// since YAML map iteration does not preserve insertion order.
func (c *AgentConfig) PoolNames() []string {
	if len(c.Models) == 0 {
		return nil
	}
	if len(c.poolOrder) > 0 {
		out := make([]string, len(c.poolOrder))
		copy(out, c.poolOrder)
		return out
	}
	names := make([]string, 0, len(c.Models))
	for name := range c.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasPool reports whether this agent config defines the named model pool.
func (c *AgentConfig) HasPool(name string) bool {
	_, ok := c.Models[name]
	return ok
}

// PoolModels returns a copy of the model refs in the named pool.
// Returns nil if the pool does not exist or is empty.
func (c *AgentConfig) PoolModels(poolName string) []string {
	refs, ok := c.Models[poolName]
	if !ok || len(refs) == 0 {
		return nil
	}
	out := make([]string, len(refs))
	copy(out, refs)
	return out
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

	// Inline models: { ... } definitions are deprecated and no longer supported.
	// Model pools must be defined in config.yaml's top-level model_pools and referenced
	// from agents via model_pools: [...].
	if len(cfg.Models) > 0 {
		return nil, fmt.Errorf("agent config %s: inline models are not supported; define pools in config.yaml model_pools and reference them via model_pools", path)
	}

	if cfg.Name == "" {
		base := filepath.Base(path)
		cfg.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	// model_pools list validation.
	seenPools := make(map[string]struct{})
	for _, name := range cfg.ModelPools {
		if name == "" {
			return nil, fmt.Errorf("agent config %s: model_pools entry must not be empty", path)
		}
		if _, dup := seenPools[name]; dup {
			return nil, fmt.Errorf("agent config %s: model_pools contains duplicate entry %q", path, name)
		}
		seenPools[name] = struct{}{}
	}

	if len(cfg.ModelPools) == 0 {
		return nil, fmt.Errorf("agent config %s: must define at least one model pool via model_pools", path)
	}

	cfg.poolOrder = append([]string(nil), cfg.ModelPools...)
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
		Mode:        AgentModeMain,
		ModelPools:  []string{"default"},
		Permission:  inner,
	}
}

// DefaultBuilderAgent returns the built-in builder agent configuration.
// The builder agent is the MainAgent's default role — the main agent the user
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
		Mode:        AgentModeMain,
		ModelPools:  []string{"default"},
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

// ResolveAgentModelPools resolves model_pools references in agent configs
// using the global model_pools definitions from config.yaml.
// Agents that use model_pools get their Models map populated from the global definitions.
// Returns an error if any referenced pool name does not exist in globalPools.
func ResolveAgentModelPools(agents map[string]*AgentConfig, globalPools map[string][]string) error {
	for name := range globalPools {
		if name == "" {
			return fmt.Errorf("config model_pools: pool name must not be empty")
		}
	}
	for agentName, cfg := range agents {
		if cfg == nil {
			continue
		}
		if len(cfg.ModelPools) == 0 {
			continue
		}

		order := make([]string, 0, len(cfg.ModelPools))
		seen := make(map[string]struct{}, len(cfg.ModelPools))
		resolved := make(map[string][]string, len(cfg.ModelPools))
		for _, poolName := range cfg.ModelPools {
			if _, dup := seen[poolName]; dup {
				return fmt.Errorf("agent %q: model_pools contains duplicate entry %q", agentName, poolName)
			}
			seen[poolName] = struct{}{}
			order = append(order, poolName)

			refs, ok := globalPools[poolName]
			if !ok {
				return fmt.Errorf("agent %q: model_pools references %q which is not defined in config model_pools", agentName, poolName)
			}
			if len(refs) == 0 {
				return fmt.Errorf("agent %q: model_pools references %q which is empty in config model_pools", agentName, poolName)
			}
			for _, ref := range refs {
				if !strings.Contains(ref, "/") {
					return fmt.Errorf("agent %q: model_pools %q model ref %q must contain '/'", agentName, poolName, ref)
				}
			}
			copied := make([]string, len(refs))
			copy(copied, refs)
			resolved[poolName] = copied
		}

		cfg.poolOrder = append([]string(nil), order...)
		cfg.Models = resolved
		cfg.ModelPools = nil
	}

	return ValidateResolvedAgentModelPools(agents)
}

func ValidateResolvedAgentModelPools(agents map[string]*AgentConfig) error {
	for agentName, cfg := range agents {
		if cfg == nil {
			continue
		}
		if len(cfg.ModelPools) > 0 {
			return fmt.Errorf("agent %q: model_pools references were not resolved", agentName)
		}
		if len(cfg.Models) == 0 {
			return fmt.Errorf("agent %q: must define at least one model pool (via models or model_pools)", agentName)
		}
		hasNonEmptyPool := false
		for poolName, refs := range cfg.Models {
			if poolName == "" {
				return fmt.Errorf("agent %q: pool name must not be empty", agentName)
			}
			if len(refs) > 0 {
				hasNonEmptyPool = true
			}
			for _, ref := range refs {
				if !strings.Contains(ref, "/") {
					return fmt.Errorf("agent %q: pool %q model ref %q must contain '/'", agentName, poolName, ref)
				}
			}
		}
		if !hasNonEmptyPool {
			return fmt.Errorf("agent %q: must define at least one non-empty model pool", agentName)
		}
	}
	return nil
}
