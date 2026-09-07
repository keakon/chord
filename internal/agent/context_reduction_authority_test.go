package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestContextReductionAuthorityMatrix(t *testing.T) {
	policy := defaultContextReductionPolicy()
	large := strings.Repeat("output line\n", 800)
	tests := []struct {
		name string
		ctx  requestReductionContext
		want requestReductionClass
	}{
		{
			name: "current read remains authoritative",
			ctx: requestReductionContext{
				ToolName: tools.NameRead, Content: large, Age: policy.StaleAgeTurns + 10,
				Policy: policy, ToolStatus: "success",
			},
			want: requestReductionNone,
		},
		{
			name: "invalidated read becomes explicit stale evidence",
			ctx: requestReductionContext{
				ToolName: tools.NameRead, Content: large, Age: 0, Policy: policy,
				ReadInvalidated: true,
			},
			want: requestReductionReadLike,
		},
		{
			name: "recent error remains complete",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Content: large, Age: 0, Policy: policy,
				ToolStatus: "error",
			},
			want: requestReductionNone,
		},
		{
			name: "repeated result is lower priority than stale read",
			ctx: requestReductionContext{
				ToolName: tools.NameRead, Content: large, Age: 1, Policy: policy,
				Repeated: true, ReadInvalidated: true,
			},
			want: requestReductionReadLike,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyRequestReductionToolOutput(test.ctx); got != test.want {
				t.Fatalf("classifyRequestReductionToolOutput() = %q, want %q", got, test.want)
			}
		})
	}
}
