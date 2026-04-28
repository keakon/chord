package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/keakon/chord/internal/llm"
)

func TestClassifyAgentError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "llm api", err: &llm.APIError{StatusCode: 503, Message: "upstream unavailable"}, want: "llm"},
		{name: "llm cooling", err: &llm.AllKeysCoolingError{}, want: "llm"},
		{name: "tool permission", err: wrapToolPermissionDenied("Write"), want: "tool"},
		{name: "tool requires confirmation", err: wrapToolRequiresConfirmation("Write"), want: "tool"},
		{name: "tool confirmation failed", err: wrapToolConfirmationFailed("Write", errors.New("backend closed")), want: "tool"},
		{name: "tool rejected", err: wrapToolRejectedByUser("Write", "policy"), want: "tool"},
		{name: "tool cancelled", err: context.Canceled, want: "agent"},
		{name: "generic", err: errors.New("unknown failure"), want: "agent"},
	} {
		if got := classifyAgentError(tc.err); got != tc.want {
			t.Fatalf("%s: classifyAgentError(%v) = %q, want %q", tc.name, tc.err, got, tc.want)
		}
	}
}
