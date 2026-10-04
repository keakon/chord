package modelcatalog

import "testing"

func TestBindingOverridesDoNotChangeModelIdentityOrSharedFacts(t *testing.T) {
	base := ModelFacts{ID: "sample/model", Context: 1000000, Input: 900000, Output: 128000, InputModalities: []string{"text", "image"}}
	binding := Binding{Endpoint: "sample", WireModelID: "wire", ModelID: base.ID, Limit: &LimitOverride{Context: new(400000), Input: new(272000)}, InputModalities: []string{"text"}}
	if err := validateBindingOverrides(binding, base); err != nil {
		t.Fatal(err)
	}
	got := binding.ApplyTo(base)
	if got.ID != base.ID || got.Context != 400000 || got.Input != 272000 || got.Output != 128000 || len(got.InputModalities) != 1 {
		t.Fatalf("resolved facts: %+v", got)
	}
	got.InputModalities[0] = "image"
	if base.Context != 1000000 || base.Input != 900000 || binding.InputModalities[0] != "text" {
		t.Fatal("override mutated shared data")
	}
	binding.Limit.Input = nil
	if err := validateBindingOverrides(binding, base); err == nil {
		t.Fatal("inherited input above the route context accepted")
	}
	binding.Limit.Input = new(0)
	if err := validateBindingOverrides(binding, base); err != nil {
		t.Fatal(err)
	}
	if got := binding.ApplyTo(base); got.Input != 0 {
		t.Fatal("input override did not clear the independent limit")
	}
}
