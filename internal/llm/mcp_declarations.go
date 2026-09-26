package llm

import "github.com/keakon/chord/internal/message"

// targetAcceptsMCPDeclarations reports whether the target renders request-level
// dynamic MCP declarations in the shape its backend accepts: a role:system
// tools message on the Chat Completions wire (mcp_system_tools_message) or an
// input[type="additional_tools"] item on the Responses wire
// (mcp_additional_tools). Targets that return false must receive the declared
// tools inlined into the top-level tools array instead.
func targetAcceptsMCPDeclarations(target FallbackModel) bool {
	return kimiDynamicTargetSupported(target) || responsesAdditionalToolsTargetSupported(target)
}

// inlineMCPToolDeclarations folds the request's current dynamic MCP
// declarations into the top-level tools array and drops pure declaration
// messages. A message that carries declarations alongside other payload keeps
// its payload with MCPTools stripped. A target that does not accept the
// dynamic shape still has to receive a legal standard request, so it sees the
// currently effective declaration set as ordinary top-level tools; it never
// shares prompt cache with the selected target that introduced the
// declarations, so inlining for that target costs no cache reuse. Inputs are
// not modified; a request without declarations is returned unchanged.
//
// declarations is the set the agent currently considers effective, not the
// union of everything ever declared: the mount snapshots are append-only, so
// history keeps declaring tools that have since been disabled, and replaying
// them here would re-advertise tools the agent can no longer call. The
// top-level list this request was built from outranks the declarations on
// duplicate names.
func inlineMCPToolDeclarations(messages []message.Message, tools, declarations []message.ToolDefinition) ([]message.Message, []message.ToolDefinition) {
	if !hasMCPToolDeclarations(messages) {
		return messages, tools
	}
	current := make(map[string]message.ToolDefinition, len(tools)+len(declarations))
	for _, def := range declarations {
		current[def.Name] = def
	}
	for _, def := range tools {
		current[def.Name] = def
	}
	out := make([]message.Message, 0, len(messages))
	merged := make([]message.ToolDefinition, 0, len(current))
	placed := make(map[string]struct{}, len(current))
	add := func(def message.ToolDefinition) {
		if _, ok := placed[def.Name]; ok {
			return
		}
		placed[def.Name] = struct{}{}
		merged = append(merged, current[def.Name])
	}
	// Positions stay as they were: the top-level list keeps its order and the
	// declared-only names follow in the current declaration order.
	for _, def := range tools {
		add(def)
	}
	for _, def := range declarations {
		add(def)
	}
	for i := range messages {
		if len(messages[i].MCPTools) == 0 {
			out = append(out, messages[i])
			continue
		}
		// A declaration message may still carry other payload (see
		// NewSystemToolsMessage: a pure declaration is RoleSystem with only
		// MCPTools). Keep it with the declarations stripped; drop it only
		// when it carries nothing else.
		msg := messages[i]
		if msg.Content != "" || len(msg.Parts) > 0 || len(msg.ToolCalls) > 0 || len(msg.ResponsesOutput) > 0 {
			msg.MCPTools = nil
			out = append(out, msg)
		}
	}
	return out, merged
}

func hasMCPToolDeclarations(messages []message.Message) bool {
	for i := range messages {
		if len(messages[i].MCPTools) > 0 {
			return true
		}
	}
	return false
}
