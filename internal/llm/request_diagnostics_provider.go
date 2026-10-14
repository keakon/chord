package llm

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/contextdiag/input"
	"github.com/keakon/chord/internal/message"
)

const (
	diagnosticUpstream    = "upstream_route_unobservable"
	diagnosticServerReuse = "server_context_reuse"
)

type diagnosticScope struct {
	request          *RequestDiagnostics
	provider         *ProviderConfig
	snapshot         contextdiag.Snapshot
	baseline         *input.Baseline
	domain           [32]byte
	prepared         contextdiag.Snapshot
	preparedBaseline *input.Baseline
	preparedVersion  uint64
	active           contextdiag.RequestID
	ended            bool
}

// completeObservedProvider preserves admission and provider arguments, and
// settles each response before native continuations aggregate their usage.
func completeObservedProvider(ctx context.Context, impl Provider, provider *ProviderConfig, key, model, system string, messages []message.Message, defs []message.ToolDefinition, maxTokens int, tuning RequestTuning, cb StreamCallback) (*message.Response, error) {
	var scope *diagnosticScope
	if d, _ := ctx.Value(diagnosticsContextKey{}).(*RequestDiagnostics); d != nil {
		var nativeDefinitions func() []map[string]any
		if tuning.nativeTool != nil {
			nativeDefinitions = tuning.nativeTool.diagnosticDefinitions
		}
		scan := input.NewScan(ctx, system, messages, defs, input.Options{
			NativeDefinitions: nativeDefinitions,
			HostedTool:        tuning.HostedTool != nil,
			Overrides:         func() bool { return !requestOverridesEmpty(provider.RequestOverrides(model)) },
		})
		projected := messages
		if scan.CanProject() {
			projected = promptCacheMessagesForTarget(providerWireFamily(provider), model, tuning, messages, 0)
		}
		result := scan.Finish(projected)
		c := d.collector
		c.mu.Lock()
		reduction := contextdiag.CloneReduction(d.reduction)
		secret := c.secret
		c.mu.Unlock()
		modelRef, _ := ctx.Value(diagnosticTargetContextKey{}).(string)
		if modelRef == "" {
			modelRef = providerModelRef(provider, model)
		}
		scope = &diagnosticScope{request: d, provider: provider, baseline: result.Baseline, snapshot: contextdiag.Snapshot{HistoryGeneration: d.generation, ModelRef: modelRef, WireFamily: string(providerWireFamily(provider)), EstimateScope: contextdiag.ProviderInput, Estimator: contextdiag.UTF8BytesDiv3, Completeness: result.Completeness, Categories: result.Categories, Limitations: append(result.Limitations, diagnosticUpstream), Reduction: reduction, Usage: contextdiag.Usage{State: contextdiag.Missing}}}
		scope.domain = diagnosticDomain(secret[:], fmt.Sprintf("%p", provider), provider.APIURL(), key, modelRef, string(providerWireFamily(provider)), tuning.SessionKey, tuning.Anthropic.PromptCacheMode, tuning.Anthropic.PromptCacheTTL)
		ctx = context.WithValue(ctx, diagnosticScopeContextKey{}, scope)
	}
	lease, err := beginCustomProvider(ctx, impl)
	if err != nil {
		return nil, err
	}
	defer lease.Finish()
	resp, err := impl.CompleteStream(ctx, key, model, system, messages, defs, maxTokens, tuning, cb)
	if scope != nil {
		scope.finish(ctx, resp, err, "")
	}
	return resp, err
}
func limitationWithoutDuplicate(in []string, s string) []string {
	if slices.Contains(in, s) {
		return in
	}
	return append(in, s)
}
func (s *diagnosticScope) prepare(transport string) {
	c := s.request.collector
	c.mu.Lock()
	previous, version := c.baseline, c.version
	c.mu.Unlock()
	next := s.baseline.ForDomain(diagnosticDomain(s.domain[:], transport))
	snapshot := contextdiag.Clone(s.snapshot)
	snapshot.Transport = transport
	if transport == contextdiag.Custom {
		next = next.WithoutMessageComparison()
		snapshot.Limitations = limitationWithoutDuplicate(snapshot.Limitations, "custom_provider")
	}
	snapshot.Cache = input.Compare(previous, next)
	s.prepared, s.preparedBaseline, s.preparedVersion = snapshot, next, version
}
func (s *diagnosticScope) submit() {
	c := s.request.collector
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.invalid || c.current != s.request || s.preparedBaseline == nil {
		return
	}
	if s.active.Attempt > 0 && !s.ended {
		c.store.Finish(s.active, contextdiag.Superseded, contextdiag.Usage{State: contextdiag.Missing})
	}
	s.request.attempt++
	id := s.request.id
	id.Attempt = s.request.attempt
	snapshot := s.prepared
	snapshot.ID = id
	snapshot.StartedAt = time.Now()
	snapshot.State = contextdiag.Dispatched
	if c.version != s.preparedVersion {
		snapshot.Cache = contextdiag.Cache{BaselineState: contextdiag.Unavailable, PrefixState: contextdiag.Unknown, Explanation: contextdiag.NoReport}
	}
	if c.store.Publish(snapshot) {
		c.baseline = s.preparedBaseline
		c.version++
		s.active = id
		s.ended = false
	}
}
func (s *diagnosticScope) finish(ctx context.Context, resp *message.Response, err error, usageState string) {
	if s.ended {
		return
	}
	s.ended = true
	state := contextdiag.Completed
	if ctx.Err() != nil {
		state = contextdiag.Cancelled
	} else if err != nil {
		state = contextdiag.Failed
	}
	usage := diagnosticUsage(s.provider, resp, err != nil || ctx.Err() != nil)
	if usageState != "" {
		usage.State = usageState
	}
	s.request.collector.store.Finish(s.active, state, usage)
}
func diagnosticUsage(provider *ProviderConfig, resp *message.Response, partial bool) contextdiag.Usage {
	out := contextdiag.Usage{State: contextdiag.Missing}
	if resp == nil || resp.Usage == nil {
		return out
	}
	usage := *resp.Usage
	copyResponse := message.Response{Usage: &usage}
	normalizeResponseUsage(provider, &copyResponse)
	u := analytics.NormalizeBillingUsage(analytics.UsageSnapshotFromTokenUsage(*copyResponse.Usage))
	out.FullInput = analytics.FullInputTokens(u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	out.Output = u.OutputTokens
	out.CacheRead = u.CacheReadTokens
	out.CacheWrite = u.CacheWriteTokens
	if out.FullInput > 0 || out.Output > 0 || usage.ReasoningTokens > 0 {
		out.State = contextdiag.Reported
		if partial {
			out.State = contextdiag.Partial
		}
	}
	return out
}
func finishDiagnosticAttempt(ctx context.Context, resp *message.Response, err error, usageState string) {
	if s := diagnosticsScope(ctx); s != nil {
		s.finish(ctx, resp, err, usageState)
	}
}
func diagnosticServerContextReuse(ctx context.Context) {
	if s := diagnosticsScope(ctx); s != nil {
		s.baseline = s.baseline.WithoutMessageComparison()
		s.snapshot.Limitations = limitationWithoutDuplicate(s.snapshot.Limitations, diagnosticServerReuse)
	}
}
