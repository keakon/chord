package modelcompat

import "testing"

func TestSupportsResponsesCacheBreakpoints(t *testing.T) {
	for _, model := range []string{"gpt-5.6", "gpt-5.6-sol", "gpt-5.10", "gpt-6", "openai/gpt-6.1-sol", "GPT-6-ASTRA"} {
		if !SupportsResponsesCacheBreakpoints(model) {
			t.Errorf("support missing for %q", model)
		}
	}
	for _, model := range []string{"", "test-model", "gpt-4.1", "gpt-5.5", "gpt-5", "gpt-6x", "gpt-6.1.2", "other-gpt-6", "claude-6", "gpt--6"} {
		if SupportsResponsesCacheBreakpoints(model) {
			t.Errorf("unexpected support for %q", model)
		}
	}
}
