package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

const (
	// defaultHostedToolTimeout bounds one hosted tool call across every
	// backend attempt; the sub-request chain is a multi-hop network path, so
	// it matches WebFetch's maximum budget instead of a single-request
	// timeout. A catalog entry can override it through timeout_s.
	defaultHostedToolTimeout = 120 * time.Second
	// hostedPayloadSnippetBytes bounds each raw payload embedded into the
	// generic tool result.
	hostedPayloadSnippetBytes = 2000
)

// HostedToolBackend runs a hosted tool through the model pool: it lowers the
// tool's declaration onto capable targets, walks them, and returns the
// observed provider-side activity. It is implemented outside this package
// (the agent wires the LLM sub-request path), keeping the tool itself free of
// provider side effects. Run returns an error unless at least one hosted call
// completed with a result.
type HostedToolBackend interface {
	// Available reports whether at least one target can carry the tool's
	// declaration. When false the tool is withheld from the LLM tool list.
	Available(tool string) bool
	Run(ctx context.Context, tool string, args map[string]any) (*message.HostedObservation, error)
	// ForCaller returns a backend view whose default caller is the given
	// agent instance. Unset model_pool routing follows that caller's own
	// pool, and availability is evaluated against it. SubAgent spawn uses
	// this to rebind hosted tools cloned from the shared registry.
	ForCaller(agentID string) HostedToolBackend
}

// HostedToolSpec is the resolved local-tool view of one hosted tool: the
// model-facing surface, the local traits, and the per-family wire
// declarations. It is built from the built-in catalog plus the user's
// hosted_tools configuration (see ResolveHostedToolCatalog).
type HostedToolSpec struct {
	Name            string
	Description     string
	Parameters      map[string]any
	Prompt          string
	ReadOnly        bool
	ConcurrencySafe bool
	RetrySafe       bool
	ImagePaths      []string
	TimeoutS        int
	Declarations    map[string]config.HostedToolDeclarationConfig
	// ModelPool routes the tool's sub-requests to a named model pool instead
	// of the caller's own pool. Empty follows the caller. Resolved and
	// validated by the agent layer at startup.
	ModelPool string
	// Validate checks the decoded arguments before any sub-request is sent. It
	// may normalize values in place so the wire declaration and the prompt see
	// the same canonical arguments. nil skips validation.
	Validate func(args map[string]any) error
	// Format renders a completed observation into the local tool result. nil
	// uses the generic formatter.
	Format func(obs *message.HostedObservation) string
}

// ResolveHostedToolCatalog merges the built-in entries with the user's
// hosted_tools catalog. User entries override built-ins field by field, so a
// user can retarget the built-in web_search declaration without restating its
// local tool surface. An entry that only exists in the user catalog starts
// from conservative traits (not read-only, not concurrency-safe).
func ResolveHostedToolCatalog(user map[string]config.HostedToolConfig) map[string]HostedToolSpec {
	catalog := BuiltinHostedToolSpecs()
	for rawName, cfg := range user {
		name := NormalizeName(rawName)
		if name == "" {
			continue
		}
		spec := catalog[name]
		spec.Name = name
		applyHostedToolConfig(&spec, cfg)
		catalog[name] = spec
	}
	return catalog
}

// applyHostedToolConfig overlays one user entry onto a catalog spec.
func applyHostedToolConfig(spec *HostedToolSpec, cfg config.HostedToolConfig) {
	if cfg.Description != "" {
		spec.Description = cfg.Description
	}
	if cfg.Parameters != nil {
		spec.Parameters = cfg.Parameters
	}
	if cfg.Prompt != "" {
		spec.Prompt = cfg.Prompt
	}
	if cfg.ReadOnly != nil {
		spec.ReadOnly = *cfg.ReadOnly
	}
	if cfg.ConcurrencySafe != nil {
		spec.ConcurrencySafe = *cfg.ConcurrencySafe
	}
	if cfg.RetrySafe != nil {
		spec.RetrySafe = *cfg.RetrySafe
	}
	if cfg.ImagePaths != nil {
		spec.ImagePaths = append([]string(nil), cfg.ImagePaths...)
	}
	if cfg.TimeoutSeconds > 0 {
		spec.TimeoutS = cfg.TimeoutSeconds
	}
	if trimmedPool := strings.TrimSpace(cfg.ModelPool); trimmedPool != "" {
		spec.ModelPool = trimmedPool
	}
	if len(cfg.Declarations) > 0 {
		if spec.Declarations == nil {
			spec.Declarations = make(map[string]config.HostedToolDeclarationConfig, len(cfg.Declarations))
		}
		for family, decl := range cfg.Declarations {
			spec.Declarations[family] = mergeHostedDeclaration(spec.Declarations[family], decl)
		}
	}
	if spec.Parameters == nil {
		spec.Parameters = emptyToolSchema()
	}
}

// mergeHostedDeclaration merges a user family declaration onto the built-in
// one: the declaration object and the include list replace their built-in
// counterparts outright, and headers merge key by key.
func mergeHostedDeclaration(base, override config.HostedToolDeclarationConfig) config.HostedToolDeclarationConfig {
	if override.Tool != nil {
		base.Tool = override.Tool
	}
	if override.Force != nil {
		base.Force = override.Force
	}
	if override.Include != nil {
		base.Include = override.Include
	}
	if len(override.Headers) > 0 {
		merged := make(map[string]string, len(base.Headers)+len(override.Headers))
		for name, value := range base.Headers {
			merged[http.CanonicalHeaderKey(name)] = value
		}
		for name, value := range override.Headers {
			merged[http.CanonicalHeaderKey(name)] = value
		}
		base.Headers = merged
	}
	return base
}

// emptyToolSchema is the schema of a tool without arguments. Providers reject
// a null input_schema, so a nil configuration still yields an object schema.
func emptyToolSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// HostedTool exposes one provider-side (hosted) tool as a local tool. Every
// call runs one independent sub-request whose tools array declares the hosted
// tool; the main conversation request never declares it, so the main history
// stays free of opaque provider blocks. All tool-specific behavior comes from
// the spec, so a new hosted tool is a configuration change.
type HostedTool struct {
	spec    HostedToolSpec
	backend HostedToolBackend
}

// NewHostedTool wires one catalog entry to the shared hosted backend.
func NewHostedTool(spec HostedToolSpec, backend HostedToolBackend) HostedTool {
	return HostedTool{spec: spec, backend: backend}
}

// WithBackend returns a copy of the tool bound to a backend view. SubAgent
// spawn uses it so cloned hosted tools follow the subagent as the default
// caller instead of the shared main-agent view.
func (t HostedTool) WithBackend(backend HostedToolBackend) HostedTool {
	return HostedTool{spec: t.spec, backend: backend}
}

func (t HostedTool) Name() string { return t.spec.Name }

func (t HostedTool) Description() string { return t.spec.Description }

func (t HostedTool) Parameters() map[string]any {
	if t.spec.Parameters == nil {
		return emptyToolSchema()
	}
	return t.spec.Parameters
}

func (t HostedTool) IsReadOnly() bool { return t.spec.ReadOnly }

// ConcurrencySafeReadOnly batches the call only when the catalog entry opts
// into both traits: a read-only tool may still need exclusive scheduling, and
// a concurrency-safe tool that is not read-only has no business in a read
// batch.
func (t HostedTool) ConcurrencySafeReadOnly(json.RawMessage) bool {
	return t.spec.ConcurrencySafe && t.spec.ReadOnly
}

// ConcurrencyPolicy scopes the call to the tool name: two invocations of the
// same hosted tool may overlap only when both are reads, and any catalog tool
// that is not read-only serializes as a write.
func (t HostedTool) ConcurrencyPolicy(json.RawMessage) ConcurrencyPolicy {
	mode := ConcurrencyModeRead
	if !t.spec.ReadOnly {
		mode = ConcurrencyModeWrite
	}
	return ConcurrencyPolicy{Resource: "hosted:" + t.spec.Name, Mode: mode}
}

func (t HostedTool) IsAvailable() bool {
	return t.backend != nil && t.backend.Available(t.spec.Name)
}

func (t HostedTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.backend == nil {
		return "", fmt.Errorf("%s is not configured", t.spec.Name)
	}
	args, err := parseHostedToolArgs(raw)
	if err != nil {
		return "", err
	}
	if t.spec.Validate != nil {
		if err := t.spec.Validate(args); err != nil {
			return "", err
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, t.timeout())
	defer cancel()
	obs, err := t.backend.Run(runCtx, t.spec.Name, args)
	if err != nil {
		return "", err
	}
	if obs == nil {
		return "", fmt.Errorf("%s returned no observation", t.spec.Name)
	}
	return t.renderResult(ctx, obs)
}

func (t HostedTool) timeout() time.Duration {
	if t.spec.TimeoutS > 0 {
		return time.Duration(t.spec.TimeoutS) * time.Second
	}
	return defaultHostedToolTimeout
}

// parseHostedToolArgs decodes the model-authored arguments into the map shape
// that declaration placeholders and prompt templates resolve against. A
// missing or null argument object is treated as empty.
func parseHostedToolArgs(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(unwrapToolArgs(raw))
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]any{}, nil
	}
	var args map[string]any
	value, err := decodeArgsForSchema(nil, trimmed, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	args, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid arguments: expected an object")
	}
	return args, nil
}

// HostedDeclaration is the per-request wire form of one family declaration
// with its placeholders resolved against the call's arguments.
type HostedDeclaration struct {
	Tool    json.RawMessage
	Force   json.RawMessage
	Include []string
	Headers map[string]string
}

// ResolveHostedDeclaration returns the family declaration for one call. The
// declaration template is copied before placeholder resolution, so the shared
// catalog entry is never mutated. Errors describe the configuration, not the
// model's arguments.
func ResolveHostedDeclaration(spec HostedToolSpec, family string, args map[string]any) (HostedDeclaration, error) {
	decl, ok := spec.Declarations[family]
	if !ok || len(decl.Tool) == 0 {
		return HostedDeclaration{}, fmt.Errorf("hosted tool %q has no %s declaration", spec.Name, family)
	}
	template, err := copyHostedDeclaration(decl.Tool)
	if err != nil {
		return HostedDeclaration{}, fmt.Errorf("hosted tool %q %s declaration: %w", spec.Name, family, err)
	}
	resolved, _ := resolveHostedArgs(template, args)
	toolJSON, err := json.Marshal(resolved)
	if err != nil {
		return HostedDeclaration{}, fmt.Errorf("hosted tool %q %s declaration: %w", spec.Name, family, err)
	}
	out := HostedDeclaration{
		Tool:    toolJSON,
		Include: append([]string(nil), decl.Include...),
		Headers: maps.Clone(decl.Headers),
	}
	if decl.Force != nil {
		forceJSON, err := json.Marshal(decl.Force)
		if err != nil {
			return HostedDeclaration{}, fmt.Errorf("hosted tool %q %s tool_choice: %w", spec.Name, family, err)
		}
		out.Force = forceJSON
	}
	return out, nil
}

// RenderHostedPrompt returns the sub-request instruction for one call. The
// spec's {arg} placeholders are replaced with the matching argument value
// (strings verbatim, other values JSON-encoded). A spec without a prompt
// template serializes the arguments as JSON so the sub-request model still
// receives them.
func RenderHostedPrompt(spec HostedToolSpec, args map[string]any) string {
	template := strings.TrimSpace(spec.Prompt)
	if template == "" {
		if len(args) == 0 {
			return "{}"
		}
		encoded, err := json.Marshal(args)
		if err != nil || len(encoded) == 0 {
			return "{}"
		}
		return string(encoded)
	}
	replacements := make([]string, 0, len(args)*2)
	for name, value := range args {
		placeholder := "{" + name + "}"
		if !strings.Contains(template, placeholder) {
			continue
		}
		replacements = append(replacements, placeholder, hostedArgText(value))
	}
	// Replacer scans only the template; inserted argument text is never
	// interpreted as another placeholder.
	return strings.NewReplacer(replacements...).Replace(template)
}

func hostedArgText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// copyHostedDeclaration deep-copies a declaration template through
// JSON so placeholder resolution cannot mutate the shared catalog entry.
func copyHostedDeclaration(tool map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(tool)
	if err != nil {
		return nil, err
	}
	return parseHostedToolArgs(encoded)
}

// resolveHostedArgs replaces {"$arg": "<name>"} placeholders in a decoded
// declaration template. The returned bool reports whether the node survived:
// a placeholder whose argument is missing or empty resolves to nothing, and a
// non-empty object whose every child was dropped is removed from its parent.
// An object that was empty from the start is kept, because an empty object is
// itself a valid declaration shape (for example {"google_search":{}}).
func resolveHostedArgs(node any, args map[string]any) (any, bool) {
	switch value := node.(type) {
	case map[string]any:
		if name, ok := hostedArgPlaceholder(value); ok {
			arg, present := args[name]
			if !present || hostedArgAbsent(arg) {
				return nil, false
			}
			return arg, true
		}
		original := len(value)
		for key, child := range value {
			next, keep := resolveHostedArgs(child, args)
			if !keep {
				delete(value, key)
				continue
			}
			value[key] = next
		}
		if original > 0 && len(value) == 0 {
			return nil, false
		}
		return value, true
	case []any:
		out := make([]any, 0, len(value))
		for _, child := range value {
			next, keep := resolveHostedArgs(child, args)
			if keep {
				out = append(out, next)
			}
		}
		return out, true
	default:
		return node, true
	}
}

// hostedArgPlaceholder reports whether a decoded node is exactly the
// single-key placeholder object {"$arg": "<name>"}.
func hostedArgPlaceholder(node map[string]any) (string, bool) {
	if len(node) != 1 {
		return "", false
	}
	name, ok := node["$arg"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return "", false
	}
	return name, true
}

// hostedArgAbsent reports whether a resolved argument counts as empty: nil,
// an empty string, an empty array, or an empty object.
func hostedArgAbsent(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case []string:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	case map[string]string:
		return len(v) == 0
	default:
		return false
	}
}

// formatHostedObservation renders the generic result: the sub-request model's
// summary followed by each call's native kind, status, and raw payloads. The
// built-in web_search entry replaces it with a source-numbered formatter.
func formatHostedObservation(obs *message.HostedObservation) string {
	if obs == nil {
		return "(no hosted tool output)"
	}
	var b strings.Builder
	if summary := strings.TrimSpace(obs.Summary); summary != "" {
		b.WriteString("Assistant summary:\n")
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	if len(obs.Calls) == 0 {
		b.WriteString("(the provider reported no tool call)\n")
	}
	for i := range obs.Calls {
		call := &obs.Calls[i]
		label := strings.TrimSpace(call.Kind)
		if label == "" {
			label = strings.TrimSpace(call.Name)
		}
		if label == "" {
			label = "hosted tool call"
		}
		fmt.Fprintf(&b, "%s %d", label, i+1)
		if status := strings.TrimSpace(call.Status); status != "" {
			fmt.Fprintf(&b, " (%s)", status)
		}
		b.WriteString("\n")
		if len(call.Input) > 0 {
			fmt.Fprintf(&b, "    Input: %s\n", hostedPayloadSnippet(call.Input))
		}
		if len(call.Result) > 0 {
			fmt.Fprintf(&b, "    Result: %s\n", hostedPayloadSnippet(call.Result))
		} else if call.Error != "" {
			fmt.Fprintf(&b, "    Error: %s\n", call.Error)
		}
	}
	for _, raw := range obs.Items {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &item) == nil && !strings.HasSuffix(item.Type, "_call") {
			fmt.Fprintf(&b, "Native output (%s): %s\n", item.Type, hostedPayloadSnippet(raw))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// hostedPayloadSnippet bounds one raw payload and never splits a UTF-8 rune.
func hostedPayloadSnippet(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if len(text) <= hostedPayloadSnippetBytes {
		return text
	}
	cut := text[:hostedPayloadSnippetBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return fmt.Sprintf("%s… (%d bytes truncated)", cut, len(text)-len(cut))
}
