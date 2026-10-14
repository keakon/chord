// Package input estimates request input and compares bounded cache fingerprints.
package input

import (
	"context"
	"crypto/sha256"
	"slices"

	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/message"
)

// Options describes request features that affect observable input evidence.
type Options struct {
	// NativeDefinitions supplies protocol-native declarations at the point they
	// are examined. The supplier must return deterministic declarations without
	// side effects. Both calls run inside the scan's shared budget; nil means no
	// native declarations. The scan never calls back into request execution.
	NativeDefinitions func() []map[string]any
	HostedTool        bool
	// Overrides reports request configuration changes after fingerprinting.
	// It is evaluated once, preserving the adapter's observation order.
	Overrides func() bool
}

// Scan owns one request's estimation and fingerprint budget. It is used by one
// goroutine and finished once. Message projection may run between construction
// and Finish, so both operations consume the same elapsed-time budget.
type Scan struct {
	budget       *diagnosticBudgetState
	system       string
	defs         []message.ToolDefinition
	options      Options
	canProject   bool
	categories   []contextdiag.Category
	limitations  []string
	completeness string
}

// Result contains the input estimate and the corresponding cache fingerprint.
type Result struct {
	Categories   []contextdiag.Category
	Limitations  []string
	Completeness string
	Baseline     *Baseline
}

// NewScan estimates input before any provider-specific message projection.
// Input slices and declarations remain caller-owned and must not be mutated
// until Finish returns.
func NewScan(ctx context.Context, system string, messages []message.Message, defs []message.ToolDefinition, options Options) *Scan {
	b := newDiagnosticBudget(ctx)
	categories, limitations, completeness := estimateDiagnosticInput(b, system, messages, defs, options)
	return &Scan{budget: b, system: system, defs: defs, options: options,
		canProject: !b.incomplete && len(messages) <= diagnosticMaxMessages,
		categories: categories, limitations: limitations, completeness: completeness}
}

// CanProject reports whether estimation left sufficient evidence to attempt
// provider-specific message projection before fingerprinting.
func (s *Scan) CanProject() bool { return s.canProject }

// Finish fingerprints the provider projection using the remaining scan budget.
// If CanProject returned false, pass the original messages without projecting.
func (s *Scan) Finish(projected []message.Message) Result {
	b := s.budget
	baseline := buildDiagnosticBaseline(b, s.system, projected, s.defs)
	if s.options.NativeDefinitions != nil {
		h := sha256.New()
		h.Write(baseline.tools[:])
		for _, definition := range s.options.NativeDefinitions() {
			data, ok := boundedDiagnosticJSON(definition)
			if !ok || !diagnosticHashField(h, b, string(data)) {
				baseline.toolsOK = false
				break
			}
		}
		baseline.tools = diagnosticSum(h)
	}
	overrides := s.options.Overrides != nil && s.options.Overrides()
	limitations, completeness := s.limitations, s.completeness
	if overrides || s.options.HostedTool || s.options.NativeDefinitions != nil {
		baseline.messagesOK = false
		baseline.messages = nil
		limitations = append(limitations, diagnosticProjection)
	}
	if overrides {
		limitations = append(limitations, diagnosticOverrides)
	}
	if b.incomplete {
		completeness = contextdiag.Partial
		if !slices.Contains(limitations, diagnosticScanIncomplete) {
			limitations = append(limitations, diagnosticScanIncomplete)
		}
		limitations = append(limitations, diagnosticProjection)
	}
	return Result{Categories: s.categories, Limitations: limitations, Completeness: completeness, Baseline: baseline}
}

// ForDomain returns a baseline scoped to a caller-derived request domain.
// Provider credentials and transport identity stay with the request adapter.
func (b *Baseline) ForDomain(domain [32]byte) *Baseline {
	next := *b
	next.domain = domain
	return &next
}

// WithoutMessageComparison returns a baseline whose message projection cannot
// be compared precisely, for example after server-side context reuse.
func (b *Baseline) WithoutMessageComparison() *Baseline {
	next := *b
	next.messagesOK = false
	return &next
}
