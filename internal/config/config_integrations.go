package config

import (
	"gopkg.in/yaml.v3"
)

// LSPConfig holds LSP server configurations keyed by server name (e.g. gopls, pyright).
// YAML: lsp.gopls: { command: "gopls", ... } (no "servers" layer).
type LSPConfig map[string]LSPServerConfig

// LSPServerConfig configures a single LSP server.
type LSPServerConfig struct {
	Disabled    bool              `json:"disabled,omitempty" yaml:"disabled,omitempty"`
	Command     string            `json:"command,omitempty" yaml:"command,omitempty"`
	Args        []string          `json:"args,omitempty" yaml:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	FileTypes   []string          `json:"file_types,omitempty" yaml:"file_types,omitempty"`
	RootMarkers []string          `json:"root_markers,omitempty" yaml:"root_markers,omitempty"`
	InitOptions map[string]any    `json:"init_options,omitempty" yaml:"init_options,omitempty"`
	Options     map[string]any    `json:"options,omitempty" yaml:"options,omitempty"`
	Timeout     int               `json:"timeout,omitempty" yaml:"timeout,omitempty"` // seconds, default 30
}

// MCPServerConfig defines an MCP server connection.
// Either Command (stdio transport) or URL (HTTP transport) must be set.
type MCPServerConfig struct {
	Auth         string            `json:"auth,omitempty" yaml:"auth,omitempty"`
	OAuth        *MCPOAuthConfig   `json:"oauth,omitempty" yaml:"oauth,omitempty"`
	Command      string            `json:"command,omitempty" yaml:"command,omitempty"`             // executable (for stdio transport)
	Args         []string          `json:"args,omitempty" yaml:"args,omitempty"`                   // command arguments
	Env          []string          `json:"env,omitempty" yaml:"env,omitempty"`                     // optional environment variables
	URL          string            `json:"url,omitempty" yaml:"url,omitempty"`                     // HTTP URL (for HTTP transport)
	Headers      map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`             // extra HTTP headers (for HTTP transport); values starting with $ are expanded from the environment
	AllowedTools []string          `json:"allowed_tools,omitempty" yaml:"allowed_tools,omitempty"` // optional remote MCP tool allowlist
	Manual       bool              `json:"manual,omitempty" yaml:"manual,omitempty"`               // when true, do not auto-start; must be enabled via /mcp or shortcut
	Deferred     bool              `json:"deferred,omitempty" yaml:"deferred,omitempty"`           // expose schemas only after tool_search; connection/enable intent is unchanged
}

// MCPConfig holds MCP server configurations keyed by server name.
// YAML: mcp.exa: { url: "...", ... } (no "servers" layer).
type MCPConfig map[string]MCPServerConfig

// HookConfig holds hook definitions grouped by trigger point. Each trigger
// point maps to an ordered list of hook entries that are executed sequentially.
type HookConfig struct {
	OnToolCall               []HookEntry `json:"on_tool_call,omitempty" yaml:"on_tool_call,omitempty"`
	OnToolResult             []HookEntry `json:"on_tool_result,omitempty" yaml:"on_tool_result,omitempty"`
	OnBeforeToolResultAppend []HookEntry `json:"on_before_tool_result_append,omitempty" yaml:"on_before_tool_result_append,omitempty"`
	OnBeforeLLMCall          []HookEntry `json:"on_before_llm_call,omitempty" yaml:"on_before_llm_call,omitempty"`
	OnAfterLLMCall           []HookEntry `json:"on_after_llm_call,omitempty" yaml:"on_after_llm_call,omitempty"`
	OnBeforeCompress         []HookEntry `json:"on_before_compress,omitempty" yaml:"on_before_compress,omitempty"`
	OnAfterCompress          []HookEntry `json:"on_after_compress,omitempty" yaml:"on_after_compress,omitempty"`
	OnSessionStart           []HookEntry `json:"on_session_start,omitempty" yaml:"on_session_start,omitempty"`
	OnSessionEnd             []HookEntry `json:"on_session_end,omitempty" yaml:"on_session_end,omitempty"`
	OnIdle                   []HookEntry `json:"on_idle,omitempty" yaml:"on_idle,omitempty"`
	OnWaitConfirm            []HookEntry `json:"on_wait_confirm,omitempty" yaml:"on_wait_confirm,omitempty"`
	OnWaitQuestion           []HookEntry `json:"on_wait_question,omitempty" yaml:"on_wait_question,omitempty"`
	OnAgentError             []HookEntry `json:"on_agent_error,omitempty" yaml:"on_agent_error,omitempty"`
	OnToolBatchComplete      []HookEntry `json:"on_tool_batch_complete,omitempty" yaml:"on_tool_batch_complete,omitempty"`
}

// HookCommand is a union type that accepts either a shell string or an argv list.
type HookCommand struct {
	Shell string
	Args  []string
}

func (c HookCommand) IsZero() bool {
	return c.Shell == "" && len(c.Args) == 0
}

func (c *HookCommand) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		c.Shell = value.Value
		c.Args = nil
		return nil
	case yaml.SequenceNode:
		var args []string
		if err := value.Decode(&args); err != nil {
			return err
		}
		c.Shell = ""
		c.Args = args
		return nil
	default:
		return typeErrorAt(value, "hook command (string or string array)")
	}
}

func (c HookCommand) MarshalYAML() (any, error) {
	if len(c.Args) > 0 {
		return c.Args, nil
	}
	return c.Shell, nil
}

// HookEntry defines a single external command hook.
type HookEntry struct {
	Name            string            `json:"name,omitempty" yaml:"name,omitempty"`
	Command         HookCommand       `json:"command" yaml:"command"`
	Timeout         int               `json:"timeout,omitempty" yaml:"timeout,omitempty"` // seconds, default 30
	Tools           []string          `json:"tools,omitempty" yaml:"tools,omitempty"`
	Paths           []string          `json:"paths,omitempty" yaml:"paths,omitempty"`
	Agents          []string          `json:"agents,omitempty" yaml:"agents,omitempty"`
	AgentKinds      []string          `json:"agent_kinds,omitempty" yaml:"agent_kinds,omitempty"`
	Models          []string          `json:"models,omitempty" yaml:"models,omitempty"`
	MinChangedFiles int               `json:"min_changed_files,omitempty" yaml:"min_changed_files,omitempty"`
	OnlyOnError     bool              `json:"only_on_error,omitempty" yaml:"only_on_error,omitempty"`
	Join            string            `json:"join,omitempty" yaml:"join,omitempty"`
	Result          string            `json:"result,omitempty" yaml:"result,omitempty"`
	ResultFormat    string            `json:"result_format,omitempty" yaml:"result_format,omitempty"`
	MaxResultLines  int               `json:"max_result_lines,omitempty" yaml:"max_result_lines,omitempty"`
	MaxResultBytes  int               `json:"max_result_bytes,omitempty" yaml:"max_result_bytes,omitempty"`
	DebounceMS      int               `json:"debounce_ms,omitempty" yaml:"debounce_ms,omitempty"`
	Concurrency     string            `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
	RetryOnFailure  int               `json:"retry_on_failure,omitempty" yaml:"retry_on_failure,omitempty"`
	RetryDelayMS    int               `json:"retry_delay_ms,omitempty" yaml:"retry_delay_ms,omitempty"`
	Environment     map[string]string `json:"environment,omitempty" yaml:"environment,omitempty"`
}
