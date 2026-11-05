package tools

import (
	"errors"
	"fmt"
	"testing"
)

func TestExternalCallErrorPreservesCauseAndOutcome(t *testing.T) {
	cause := errors.New("connection reset")
	wrapped := WrapExternalCallError(fmt.Errorf("remote call: %w", cause), ExternalCallOutcomeUnknown)
	if wrapped == nil || wrapped.Error() != "remote call: connection reset" {
		t.Fatalf("wrapped = %v", wrapped)
	}
	if !errors.Is(wrapped, cause) {
		t.Fatal("cause is not reachable through the chain")
	}
	outcome, ok := ExternalCallOutcomeOf(wrapped)
	if !ok || outcome != ExternalCallOutcomeUnknown {
		t.Fatalf("outcome = %q ok=%v", outcome, ok)
	}
	// A further wrap keeps the classification visible to errors.AsType users.
	nested := fmt.Errorf("tool: %w", wrapped)
	if outcome, ok := ExternalCallOutcomeOf(nested); !ok || outcome != ExternalCallOutcomeUnknown {
		t.Fatalf("nested outcome = %q ok=%v", outcome, ok)
	}
}

func TestExternalCallErrorNilAndOrdinaryError(t *testing.T) {
	if err := WrapExternalCallError(nil, ExternalCallOutcomeUnknown); err != nil {
		t.Fatalf("nil wrap = %v", err)
	}
	if outcome, ok := ExternalCallOutcomeOf(errors.New("ordinary")); ok || outcome != "" {
		t.Fatalf("ordinary error classified as %q", outcome)
	}
}
