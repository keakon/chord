package llm

import (
	"context"
	"crypto/rand"
	"sync"

	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/contextdiag/input"
)

type diagnosticsContextKey struct{}
type diagnosticTargetContextKey struct{}
type diagnosticScopeContextKey struct{}

type diagnosticTarget struct {
	modelRef    string
	windowLimit int64
}

// DiagnosticsCollector belongs to one runtime binding, never to the shared Client.
// Lock order is collector -> Store. Baselines contain hashes only.
type DiagnosticsCollector struct {
	mu       sync.Mutex
	store    *contextdiag.Store
	binding  contextdiag.Binding
	secret   [32]byte
	baseline *input.Baseline
	version  uint64
	invalid  bool
	current  *RequestDiagnostics
}

func NewDiagnosticsCollector(binding contextdiag.Binding, store *contextdiag.Store) *DiagnosticsCollector {
	c := &DiagnosticsCollector{binding: binding, store: store}
	rand.Read(c.secret[:])
	return c
}

// BeginRequest freezes reserved headroom; each dispatched attempt supplies its
// own model's compaction budget, including retries and native continuations.
func (c *DiagnosticsCollector) BeginRequest(generation uint64, reservedInput int64) *RequestDiagnostics {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := &RequestDiagnostics{collector: c, id: c.store.BeginRequest(c.binding, generation), generation: generation, reduction: contextdiag.Reduction{State: contextdiag.Unavailable}, reservedInput: reservedInput}
	c.current = d
	return d
}
func (c *DiagnosticsCollector) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalid = true
	c.store.Invalidate(c.binding)
	c.baseline = nil
	c.current = nil
}

type RequestDiagnostics struct {
	collector     *DiagnosticsCollector
	id            contextdiag.RequestID
	generation    uint64
	reduction     contextdiag.Reduction
	reservedInput int64
	attempt       uint64
}

func (d *RequestDiagnostics) SetReduction(r contextdiag.Reduction) {
	if d == nil {
		return
	}
	r = contextdiag.CloneReduction(r)
	d.collector.mu.Lock()
	d.reduction = r
	d.collector.mu.Unlock()
}
func withoutRequestDiagnostics(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, diagnosticsContextKey{}, (*RequestDiagnostics)(nil))
	return context.WithValue(ctx, diagnosticScopeContextKey{}, (*diagnosticScope)(nil))
}
func diagnosticsScope(ctx context.Context) *diagnosticScope {
	s, _ := ctx.Value(diagnosticScopeContextKey{}).(*diagnosticScope)
	return s
}
func prepareDiagnosticSubmission(ctx context.Context, transport string) {
	if s := diagnosticsScope(ctx); s != nil {
		s.prepare(transport)
	}
}
func submitDiagnosticAttempt(ctx context.Context) {
	if s := diagnosticsScope(ctx); s != nil {
		s.submit()
	}
}
