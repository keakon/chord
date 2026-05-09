package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type wrappedPolicyTool struct{}

func (wrappedPolicyTool) Name() string        { return "WrappedPolicy" }
func (wrappedPolicyTool) Description() string { return "" }
func (wrappedPolicyTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (wrappedPolicyTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return "ok", nil
}
func (wrappedPolicyTool) IsReadOnly() bool { return true }
func (wrappedPolicyTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return fileToolConcurrencyPolicy(args, true)
}

func TestPolicyForToolUnwrapsWrappedArgs(t *testing.T) {
	reg := NewRegistry()
	reg.Register(wrappedPolicyTool{})

	inner := `{"path":"/tmp/demo.txt"}`
	wrapped, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("Marshal wrapped: %v", err)
	}

	p := PolicyForTool(reg, "WrappedPolicy", wrapped)
	if p.Resource != "file:/tmp/demo.txt" {
		t.Fatalf("Resource = %q, want file:/tmp/demo.txt", p.Resource)
	}
	if p.Mode != ConcurrencyModeRead {
		t.Fatalf("Mode = %q, want read", p.Mode)
	}
}
