package modelref

import "testing"

func TestSplitRunningModelRef(t *testing.T) {
	tests := []struct {
		name                  string
		ref                   string
		provider, model, var_ string
	}{
		{name: "provider model variant", ref: "qt/model-alpha@balanced", provider: "qt", model: "model-alpha", var_: "balanced"},
		{name: "provider nested model", ref: "anthropic/claude/opus@fast", provider: "anthropic", model: "claude/opus", var_: "fast"},
		{name: "model only variant", ref: "model-alpha@mini", model: "model-alpha", var_: "mini"},
		{name: "trim empty", ref: "  ", provider: "", model: "", var_: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, m, v := SplitRunningModelRef(tc.ref)
			if p != tc.provider || m != tc.model || v != tc.var_ {
				t.Fatalf("SplitRunningModelRef(%q) = (%q, %q, %q), want (%q, %q, %q)", tc.ref, p, m, v, tc.provider, tc.model, tc.var_)
			}
		})
	}
}

func TestEnsureRefShowsVariant(t *testing.T) {
	tests := []struct {
		name, ref, active, want string
	}{
		{name: "appends active variant", ref: "qt/model-alpha", active: "balanced", want: "qt/model-alpha@balanced"},
		{name: "keeps existing variant", ref: "qt/model-alpha@high", active: "balanced", want: "qt/model-alpha@high"},
		{name: "ignores blank active", ref: "qt/model-alpha", active: " ", want: "qt/model-alpha"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EnsureRefShowsVariant(tc.ref, tc.active); got != tc.want {
				t.Fatalf("EnsureRefShowsVariant() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEnsureRefShowsMatchingVariant(t *testing.T) {
	tests := []struct {
		name, running, selected, active, want string
	}{
		{name: "matching selected appends", running: "qt/model-alpha", selected: "qt/model-alpha", active: "balanced", want: "qt/model-alpha@balanced"},
		{name: "different provider unchanged", running: "fallback/model-alpha", selected: "qt/model-alpha", active: "balanced", want: "fallback/model-alpha"},
		{name: "different model unchanged", running: "qt/model-beta", selected: "qt/model-alpha", active: "balanced", want: "qt/model-beta"},
		{name: "existing variant unchanged", running: "qt/model-alpha@high", selected: "qt/model-alpha", active: "balanced", want: "qt/model-alpha@high"},
		{name: "missing provider unchanged", running: "model-alpha", selected: "qt/model-alpha", active: "balanced", want: "model-alpha"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EnsureRefShowsMatchingVariant(tc.running, tc.selected, tc.active); got != tc.want {
				t.Fatalf("EnsureRefShowsMatchingVariant() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEnsureRefShowsProvider(t *testing.T) {
	tests := []struct{ running, selected, want string }{
		{running: "model-alpha@balanced", selected: "qt/model-alpha@balanced", want: "qt/model-alpha@balanced"},
		{running: "other/model-alpha", selected: "qt/model-alpha", want: "other/model-alpha"},
		{running: "model-alpha", selected: "model-alpha", want: "model-alpha"},
	}
	for _, tc := range tests {
		if got := EnsureRefShowsProvider(tc.running, tc.selected); got != tc.want {
			t.Fatalf("EnsureRefShowsProvider(%q, %q) = %q, want %q", tc.running, tc.selected, got, tc.want)
		}
	}
}

func TestTruncateRunningModelRef(t *testing.T) {
	tests := []struct {
		name, ref string
		max       int
		want      string
	}{
		{name: "fits unchanged", ref: "qt/gpt-5", max: 20, want: "qt/gpt-5"},
		{name: "strips nested model path", ref: "anthropic/family/claude-opus-4.6@high", max: 28, want: "anthropic/claude-opus-4.6"},
		{name: "drops variant then provider", ref: "qt/very-long-model-name@balanced", max: 20, want: "very-long-model-name"},
		{name: "ellipsis fallback", ref: "provider/supercalifragilistic", max: 10, want: "superca..."},
		{name: "non-positive default", ref: "qt/gpt-5", max: 0, want: "qt/gpt-5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TruncateRunningModelRef(tc.ref, tc.max); got != tc.want {
				t.Fatalf("TruncateRunningModelRef(%q, %d) = %q, want %q", tc.ref, tc.max, got, tc.want)
			}
		})
	}
}

func TestFormatRunningModelRefForDisplay(t *testing.T) {
	got := FormatRunningModelRefForDisplay("model-alpha", "qt/model-alpha", "balanced", 30)
	if got != "qt/model-alpha@balanced" {
		t.Fatalf("FormatRunningModelRefForDisplay = %q", got)
	}
}
