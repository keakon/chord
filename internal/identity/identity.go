package identity

import "context"

const (
	MainAgentID            = "main"
	MainSessionLogFilename = "main.jsonl"
)

// contextKey is an unexported type for identity values shared across packages.
type contextKey int

const agentIDContextKey contextKey = iota

// WithAgentID returns a new context that carries the given agent ID.
func WithAgentID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentIDContextKey, id)
}

// AgentIDFromContext extracts the agent ID from the context, or returns "" if
// absent.
func AgentIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(agentIDContextKey).(string); ok {
		return v
	}
	return ""
}
