package modelref

import "testing"

func TestSplitRunningModelRef(t *testing.T) {
	tests := []struct {
		name                  string
		ref                   string
		provider, model, var_ string
	}{
		{name: "provider model variant", ref: "qt/gpt-5.5@xhigh", provider: "qt", model: "gpt-5.5", var_: "xhigh"},
		{name: "provider nested model", ref: "anthropic/claude/opus@fast", provider: "anthropic", model: "claude/opus", var_: "fast"},
		{name: "model only variant", ref: "gpt-5.5@mini", model: "gpt-5.5", var_: "mini"},
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
		{name: "appends active variant", ref: "qt/gpt-5.5", active: "xhigh", want: "qt/gpt-5.5@xhigh"},
		{name: "keeps existing variant", ref: "qt/gpt-5.5@high", active: "xhigh", want: "qt/gpt-5.5@high"},
		{name: "ignores blank active", ref: "qt/gpt-5.5", active: " ", want: "qt/gpt-5.5"},
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
		{name: "matching selected appends", running: "qt/gpt-5.5", selected: "qt/gpt-5.5", active: "xhigh", want: "qt/gpt-5.5@xhigh"},
		{name: "different provider unchanged", running: "fallback/gpt-5.5", selected: "qt/gpt-5.5", active: "xhigh", want: "fallback/gpt-5.5"},
		{name: "different model unchanged", running: "qt/gpt-4.1", selected: "qt/gpt-5.5", active: "xhigh", want: "qt/gpt-4.1"},
		{name: "existing variant unchanged", running: "qt/gpt-5.5@high", selected: "qt/gpt-5.5", active: "xhigh", want: "qt/gpt-5.5@high"},
		{name: "missing provider unchanged", running: "gpt-5.5", selected: "qt/gpt-5.5", active: "xhigh", want: "gpt-5.5"},
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
		{running: "gpt-5.5@xhigh", selected: "qt/gpt-5.5@xhigh", want: "qt/gpt-5.5@xhigh"},
		{running: "other/gpt-5.5", selected: "qt/gpt-5.5", want: "other/gpt-5.5"},
		{running: "gpt-5.5", selected: "gpt-5.5", want: "gpt-5.5"},
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
		{name: "drops variant then provider", ref: "qt/very-long-model-name@xhigh", max: 20, want: "very-long-model-name"},
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
	got := FormatRunningModelRefForDisplay("gpt-5.5", "qt/gpt-5.5", "xhigh", 30)
	if got != "qt/gpt-5.5@xhigh" {
		t.Fatalf("FormatRunningModelRefForDisplay = %q", got)
	}
}
