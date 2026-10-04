package modelcatalog

import (
	"fmt"
	"slices"
)

// LimitOverride contains only the token limits verified for this route.
// An explicit input: 0 removes an inherited independent input limit.
type LimitOverride struct {
	Context *int `json:"context,omitempty" yaml:"context,omitempty"`
	Input   *int `json:"input,omitempty" yaml:"input,omitempty"`
	Output  *int `json:"output,omitempty" yaml:"output,omitempty"`
}

// ApplyTo returns route facts without mutating the shared model facts.
func (b Binding) ApplyTo(facts ModelFacts) ModelFacts {
	if b.Limit != nil {
		if b.Limit.Context != nil {
			facts.Context = *b.Limit.Context
		}
		if b.Limit.Input != nil {
			facts.Input = *b.Limit.Input
		}
		if b.Limit.Output != nil {
			facts.Output = *b.Limit.Output
		}
	}
	if len(b.InputModalities) > 0 {
		facts.InputModalities = slices.Clone(b.InputModalities)
	}
	return facts
}

func validateBindingOverrides(b Binding, facts ModelFacts) error {
	resolved := b.ApplyTo(facts)
	if resolved.Context <= 0 || resolved.Output <= 0 || resolved.Input < 0 || resolved.Input > resolved.Context {
		return fmt.Errorf("binding %s/%s: invalid resolved token limits; override input explicitly when changing its context", b.Endpoint, b.WireModelID)
	}
	for _, modality := range b.InputModalities {
		if !validModalities[modality] {
			return fmt.Errorf("binding %s/%s: unknown modality %q", b.Endpoint, b.WireModelID, modality)
		}
	}
	return nil
}
