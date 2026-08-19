package modelref

import "testing"

func TestSplitRunningModelRef(t *testing.T) {
	tests := []struct {
		name                  string
		ref                   string
		provider, model, var_ string
	}{
		{name: "provider model variant", ref: "sample/model-alpha@balanced", provider: "sample", model: "model-alpha", var_: "balanced"},
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
		{name: "appends active variant", ref: "sample/model-alpha", active: "balanced", want: "sample/model-alpha@balanced"},
		{name: "keeps existing variant", ref: "sample/model-alpha@high", active: "balanced", want: "sample/model-alpha@high"},
		{name: "ignores blank active", ref: "sample/model-alpha", active: " ", want: "sample/model-alpha"},
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
		{name: "matching selected appends", running: "sample/model-alpha", selected: "sample/model-alpha", active: "balanced", want: "sample/model-alpha@balanced"},
		{name: "different provider unchanged", running: "fallback/model-alpha", selected: "sample/model-alpha", active: "balanced", want: "fallback/model-alpha"},
		{name: "different model unchanged", running: "sample/model-beta", selected: "sample/model-alpha", active: "balanced", want: "sample/model-beta"},
		{name: "existing variant unchanged", running: "sample/model-alpha@high", selected: "sample/model-alpha", active: "balanced", want: "sample/model-alpha@high"},
		{name: "missing provider unchanged", running: "model-alpha", selected: "sample/model-alpha", active: "balanced", want: "model-alpha"},
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
		{running: "model-alpha@balanced", selected: "sample/model-alpha@balanced", want: "sample/model-alpha@balanced"},
		{running: "other/model-alpha", selected: "sample/model-alpha", want: "other/model-alpha"},
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
		{name: "fits unchanged", ref: "sample/gpt-5", max: 20, want: "sample/gpt-5"},
		{name: "strips nested model path", ref: "anthropic/family/claude-opus-4.6@high", max: 28, want: "anthropic/claude-opus-4.6"},
		{name: "drops variant then provider", ref: "sample/very-long-model-name@balanced", max: 20, want: "very-long-model-name"},
		{name: "ellipsis fallback", ref: "provider/supercalifragilistic", max: 10, want: "superca..."},
		{name: "non-positive default", ref: "sample/gpt-5", max: 0, want: "sample/gpt-5"},
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
	got := FormatRunningModelRefForDisplay("model-alpha", "sample/model-alpha", "balanced", 30)
	if got != "sample/model-alpha@balanced" {
		t.Fatalf("FormatRunningModelRefForDisplay = %q", got)
	}
}

func TestSplitRequestModelRefForDisplay(t *testing.T) {
	tests := []struct {
		name, running, selected, active string
		provider, model, variant        string
	}{
		{name: "running wins with own variant", running: "sample/model-alpha@high", selected: "sample/model-beta", active: "balanced", provider: "sample", model: "model-alpha", variant: "high"},
		{name: "selected fallback without variant backfill", selected: "sample/model-beta", active: "balanced", provider: "sample", model: "model-beta"},
		{name: "selected inline variant kept", selected: "sample/model-beta@low", active: "balanced", provider: "sample", model: "model-beta", variant: "low"},
		{name: "provider backfilled from selected", running: "model-alpha", selected: "sample/model-alpha", active: "balanced", provider: "sample", model: "model-alpha", variant: "balanced"},
		{name: "matching variant appended", running: "sample/model-alpha", selected: "sample/model-alpha", active: "balanced", provider: "sample", model: "model-alpha", variant: "balanced"},
		{name: "mismatched base keeps no variant", running: "sample/model-beta", selected: "sample/model-alpha", active: "balanced", provider: "sample", model: "model-beta"},
		{name: "all empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, m, v := SplitRequestModelRefForDisplay(tc.running, tc.selected, tc.active)
			if p != tc.provider || m != tc.model || v != tc.variant {
				t.Fatalf("SplitRequestModelRefForDisplay(%q, %q, %q) = (%q, %q, %q), want (%q, %q, %q)",
					tc.running, tc.selected, tc.active, p, m, v, tc.provider, tc.model, tc.variant)
			}
		})
	}
}

func TestFormatModelVariantForDisplay(t *testing.T) {
	tests := []struct {
		name, model, variant string
		max                  int
		want                 string
	}{
		{name: "fits unchanged", model: "gpt-5.5", variant: "xhigh", max: 20, want: "gpt-5.5@xhigh"},
		{name: "drops variant first", model: "gpt-5.5", variant: "xhigh", max: 10, want: "gpt-5.5"},
		{name: "no variant fits", model: "gpt-5.5", max: 10, want: "gpt-5.5"},
		{name: "strips leading segment", model: "anthropic/claude-opus", variant: "fast", max: 12, want: "claude-opus"},
		{name: "ellipsis fallback", model: "supercalifragilistic", max: 10, want: "superca..."},
		{name: "empty model", variant: "xhigh", max: 20, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatModelVariantForDisplay(tc.model, tc.variant, tc.max); got != tc.want {
				t.Fatalf("FormatModelVariantForDisplay(%q, %q, %d) = %q, want %q", tc.model, tc.variant, tc.max, got, tc.want)
			}
		})
	}
}
