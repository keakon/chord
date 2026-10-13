package mcp

import (
	"context"
	"os"
	"sync"
	"time"
)

func controlNetworkDiagnostic(d Diagnostic) Diagnostic {
	d.ControlNetworkMode = ControlNetworkDirect
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if os.Getenv(key) != "" {
			d.ProxyEnvironmentDetected = true
			break
		}
	}
	return d
}

func suggestedAuthAction(class ErrorClass) string {
	switch class {
	case ErrorAuthRequired, ErrorInvalidToken, ErrorRefreshInvalid, ErrorRefreshOutcomeUnknown, ErrorIdentityChanged:
		return ActionLoginOnHost
	case ErrorInsufficientScope:
		return ActionGrantScope
	case ErrorAccessDenied:
		return ActionCheckAccess
	case ErrorNetworkPolicyDenied, ErrorNetwork, ErrorTLS:
		return ActionCheckDirectConnectivity
	case ErrorRefreshBlocked, ErrorRegistrationFailed, ErrorIssuerMismatch, ErrorResourceMismatch, ErrorPKCEUnsupported, ErrorDiscoveryFailed, ErrorProtocol, ErrorEndpointRedirect:
		return ActionCheckServerConfiguration
	case ErrorCredentialStoreFailed, ErrorCredentialStoreCorrupt:
		return ActionCheckCredentialStore
	case ErrorCallbackUnavailable:
		return ActionCheckCallbackPort
	}
	return ""
}

type probeTraceKey struct{}
type probeTrace struct {
	mu     sync.Mutex
	stages []ProbeStage
}

func newProbeTrace() *probeTrace { return &probeTrace{} }
func traceFromContext(ctx context.Context) *probeTrace {
	trace, _ := ctx.Value(probeTraceKey{}).(*probeTrace)
	return trace
}
func (t *probeTrace) record(name string, started time.Time, err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	report := ProbeResult{Stages: []ProbeStage{{Stage: name}}}
	report.stage(name, started, err)
	t.stages = append(t.stages, report.Stages[0])
}
func (t *probeTrace) snapshot() []ProbeStage {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]ProbeStage(nil), t.stages...)
}
func (t *probeTrace) merge(other *probeTrace) {
	if t == nil || t == other {
		return
	}
	stages := other.snapshot()
	t.mu.Lock()
	t.stages = append(t.stages, stages...)
	t.mu.Unlock()
}
func (t *probeTrace) apply(report *ProbeResult) {
	for _, stage := range t.snapshot() {
		for i := range report.Stages {
			previous := &report.Stages[i]
			if previous.Stage != stage.Stage {
				continue
			}
			if previous.State == ProbeSkipped {
				*previous = stage
				continue
			}
			if previous.StartedAt.IsZero() || stage.StartedAt.Before(previous.StartedAt) {
				previous.StartedAt = stage.StartedAt
			}
			if stage.FinishedAt.After(previous.FinishedAt) {
				previous.FinishedAt = stage.FinishedAt
				previous.State = stage.State
				previous.ErrorClass = stage.ErrorClass
				previous.SuggestedAction = stage.SuggestedAction
			}
			previous.DurationMS += stage.DurationMS
		}
	}
}
