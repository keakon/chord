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
	return fileToolConcurrencyPolicyInDir(args, true, "")
}

func TestPolicyForInstanceUnwrapsWrappedArgs(t *testing.T) {
	inner := `{"path":"/tmp/demo.txt"}`
	wrapped, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("Marshal wrapped: %v", err)
	}

	p := PolicyForInstance(wrappedPolicyTool{}, "WrappedPolicy", wrapped)
	if singleResource(p) != "file:/tmp/demo.txt" {
		t.Fatalf("Resources = %v, want file:/tmp/demo.txt", p.Resources)
	}
	if p.Mode != ConcurrencyModeRead {
		t.Fatalf("Mode = %q, want read", p.Mode)
	}
}
