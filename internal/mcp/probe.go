package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
)

// ProbeOptions describes non-interactive behavior only. Probing never registers
// clients, opens a browser or calls a business tool.
type ProbeOptions struct{ Anonymous, UseSavedCredential, AllowRefresh bool }

type ProbeStageState string

const (
	ProbePending    ProbeStageState = "pending"
	ProbeSkipped    ProbeStageState = "skipped"
	ProbeFailed     ProbeStageState = "failed"
	ProbeOK         ProbeStageState = "ok"
	StageConfig                     = "config"
	StageCredential                 = "credential"
	StageTransport                  = "transport"
	StageDiscovery                  = "discovery"
	StageRefresh                    = "refresh"
	StageInitialize                 = "initialize"
	StageToolsList                  = "tools_list"
)

type ProbeStage struct {
	Stage           string          `json:"stage"`
	State           ProbeStageState `json:"state"`
	DurationMS      int64           `json:"duration_ms"`
	StartedAt       time.Time       `json:"started_at,omitzero"`
	FinishedAt      time.Time       `json:"finished_at,omitzero"`
	ErrorClass      ErrorClass      `json:"error_class,omitempty"`
	SuggestedAction string          `json:"suggested_action,omitempty"`
}
type ProbeResult struct {
	ScopeRequirements []ScopeRequirementView `json:"scope_requirements"`
	OK                bool                   `json:"ok"`
	Ref               ServerRef              `json:"ref"`
	Auth              AuthState              `json:"auth"`
	Connection        ConnectionState        `json:"connection"`
	Catalog           CatalogState           `json:"catalog"`
	ToolCount         int                    `json:"tool_count"`
	Stages            []ProbeStage           `json:"stages"`
	Diagnostic        Diagnostic             `json:"diagnostic"`
}

// ConnectionTicket fixes the publication generation before network work begins.
type ConnectionTicket struct {
	Ref                  ServerRef
	ConfigGeneration     uint64
	ConnectionGeneration uint64
	Identity             ExpectedIdentity
}

// CandidateResult transfers connection ownership to its installing caller.
// Public diagnostics are separate from the live client and tool schemas.
type CandidateResult struct {
	Attempts    int
	Report      ProbeResult
	Client      *Client
	Definitions []MCPToolDef
	Identity    ExpectedIdentity
	Err         error
}

func newProbeReport(target Target) ProbeResult {
	result := ProbeResult{Ref: target.Ref, Auth: AuthNone, Connection: ConnectionIdle, Catalog: CatalogUnknown, Stages: []ProbeStage{}}
	for _, name := range []string{StageConfig, StageCredential, StageTransport, StageDiscovery, StageRefresh, StageInitialize, StageToolsList} {
		result.Stages = append(result.Stages, ProbeStage{Stage: name, State: ProbeSkipped})
	}
	return result
}
func (r *ProbeResult) stage(name string, started time.Time, err error) {
	finished := time.Now()
	d := diagnoseAuth(err)
	for i := range r.Stages {
		stage := &r.Stages[i]
		if stage.Stage != name {
			continue
		}
		stage.State = ProbeOK
		if err != nil {
			stage.State = ProbeFailed
		}
		if stage.StartedAt.IsZero() {
			stage.StartedAt = started
		}
		stage.FinishedAt = finished
		stage.DurationMS += finished.Sub(started).Milliseconds()
		stage.ErrorClass = d.ErrorClass
		stage.SuggestedAction = d.SuggestedAction
		return
	}
}

// Probe returns a complete safe report and always closes its temporary client.
func (s *AuthService) Probe(ctx context.Context, target Target, opts ProbeOptions) ProbeResult {
	info := ClientInfo{}
	if s != nil {
		info = s.clientInfo
	}
	result := probeConnection(ctx, target, s, info, ConnectionTicket{}, opts, nil)
	if result.Client != nil {
		_ = result.Client.Close()
	}
	return result.Report
}

func probeConnection(ctx context.Context, target Target, auth *AuthService, info ClientInfo, ticket ConnectionTicket, opts ProbeOptions, factory func(context.Context, ServerConfig) (*Client, error)) (result CandidateResult) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	target = target.Snapshot()
	result = CandidateResult{Report: newProbeReport(target), Identity: ticket.Identity}
	trace := newProbeTrace()
	ctx = context.WithValue(ctx, probeTraceKey{}, trace)
	defer func() {
		trace.apply(&result.Report)
		if managedAuth(target) && !opts.Anonymous && opts.UseSavedCredential {
			result.Report.Diagnostic = controlNetworkDiagnostic(result.Report.Diagnostic)
		}
	}()
	fail := func(err error) CandidateResult {
		result.Err = err
		result.Report.Diagnostic = diagnoseAuth(err)
		return result
	}
	started := time.Now()
	if opts.Anonymous && opts.UseSavedCredential || target.Config.URL == "" && target.Config.Command == "" {
		var err error = authError(ErrorProtocol)
		if target.Config.URL == "" && target.Config.Command == "" {
			err = fmt.Errorf("%s: %w", missingServerTransportMessage, err)
		}
		result.Report.stage(StageConfig, started, err)
		return fail(err)
	}
	result.Report.stage(StageConfig, started, nil)
	transportTarget := target.Snapshot()
	if opts.Anonymous || !opts.UseSavedCredential {
		transportTarget.Config.Auth = config.MCPAuthNone
		if opts.Anonymous {
			for k := range transportTarget.Config.Headers {
				if strings.EqualFold(k, "Authorization") {
					delete(transportTarget.Config.Headers, k)
				}
			}
		}
	}
	discover := func(challenges []BearerChallenge) error {
		_, err := auth.discoverer.Discover(ctx, target, DiscoveryOptions{BearerChallenges: challenges})
		return err
	}
	refresh := func() error {
		_, err := auth.Acquire(ctx, target, result.Identity)
		if err == nil {
			result.Report.Auth = AuthReady
		}
		return err
	}
	if managedAuth(transportTarget) {
		started = time.Now()
		if auth == nil {
			err := authError(ErrorAuthRequired)
			result.Report.stage(StageCredential, started, err)
			return fail(err)
		}
		view, err := auth.Inspect(ctx, target)
		result.Report.Auth = view.Auth
		result.Report.ScopeRequirements = view.ScopeRequirements
		result.Report.stage(StageCredential, started, err)
		if err != nil {
			return fail(err)
		}
		if result.Identity.BindingID == "" {
			result.Identity = ExpectedIdentity{BindingID: view.Version.BindingID, AuthGeneration: view.Version.AuthGeneration}
		}
		if view.Auth == AuthError || view.Auth == AuthReauthRequired {
			class := view.Diagnostic.ErrorClass
			if class == "" {
				class = ErrorAuthRequired
			}
			return fail(authError(class))
		}
		if view.Auth == AuthRefreshRequired {
			if !opts.AllowRefresh {
				return fail(authError(ErrorAuthRequired))
			}
			if err := refresh(); err != nil {
				return fail(err)
			}
		}
		if view.Auth == AuthMissing && target.Config.Auth == config.MCPAuthOAuth {
			if err := discover(nil); err != nil {
				return fail(err)
			}
			return fail(authError(ErrorAuthRequired))
		}
	} else {
		for k := range transportTarget.Config.Headers {
			if strings.EqualFold(k, "Authorization") {
				result.Report.Auth = AuthStatic
			}
		}
	}
	authRetried := false
	for attempt := 1; attempt <= defaultConnectAttempts; attempt++ {
		result.Attempts++
		attemptCtx, attemptCancel := context.WithTimeout(ctx, connectAttemptTimeout)
		started = time.Now()
		creator := func(context.Context) (*Client, error) {
			return createTargetClient(context.WithoutCancel(attemptCtx), transportTarget, auth, info, result.Identity, ticket.ConnectionGeneration)
		}
		if factory != nil {
			creator = func(ctx context.Context) (*Client, error) { return factory(ctx, target.Config) }
		}
		client, err := createProbeClientBounded(attemptCtx, creator)
		result.Report.stage(StageTransport, started, err)
		if err == nil {
			started = time.Now()
			err = client.Initialize(attemptCtx)
			result.Report.stage(StageInitialize, started, err)
		}
		if err == nil {
			result.Report.Connection = ConnectionConnected
			result.Report.Catalog = CatalogLoading
			started = time.Now()
			result.Definitions, err = client.ListTools(attemptCtx)
			result.Report.stage(StageToolsList, started, err)
			if err == nil {
				attemptCancel()
				result.Client = client
				result.Report.Catalog = CatalogReady
				result.Report.ToolCount = len(result.Definitions)
				result.Report.OK = true
				return result
			}
			result.Report.Catalog = CatalogError
		} else {
			result.Report.Connection = ConnectionError
		}
		attemptCancel()
		httpErr, isHTTP := errors.AsType[*HTTPError](err)
		authRejected := isHTTP && (httpErr.StatusCode == 401 || httpErr.StatusCode == 403)
		if client != nil && result.Report.Connection == ConnectionConnected && !authRejected {
			result.Client = client
			return fail(err)
		}
		if client != nil {
			_ = client.Close()
		}
		if authRejected {
			result.Report.Connection = ConnectionDisconnected
		}
		if authRejected && httpErr.StatusCode == 401 && auth != nil && opts.AllowRefresh && !authRetried && result.Identity.BindingID != "" {
			view, inspectErr := auth.Inspect(ctx, target)
			if inspectErr == nil && view.Auth == AuthRefreshRequired {
				authRetried = true
				if refreshErr := refresh(); refreshErr != nil {
					return fail(refreshErr)
				}
				attempt--
				continue
			}
		}
		if authRejected && httpErr.StatusCode == 401 && auth != nil && managedAuth(target) && !opts.Anonymous && result.Identity.BindingID == "" {
			result.Report.Auth = AuthMissing
			if discoveryErr := discover(httpErr.BearerChallenges); discoveryErr != nil {
				return fail(discoveryErr)
			}
		}
		if attempt == defaultConnectAttempts || !shouldRetryConnectError(err, target.Config) || authRejected || errors.Is(err, context.Canceled) {
			return fail(err)
		}
		if !sleepWithContext(ctx, connectRetryBackoff[attempt-1]) {
			return fail(ctx.Err())
		}
	}
	return fail(authError(ErrorProtocol))
}

func createProbeClientBounded(ctx context.Context, factory func(context.Context) (*Client, error)) (*Client, error) {
	done := make(chan clientFactoryResult, 1)
	go func() {
		client, err := factory(context.WithoutCancel(ctx))
		done <- clientFactoryResult{client: client, err: err}
	}()
	select {
	case result := <-done:
		if ctx.Err() != nil {
			if result.client != nil {
				_ = result.client.Close()
			}
			return nil, ctx.Err()
		}
		return result.client, result.err
	case <-ctx.Done():
		go func() {
			result := <-done
			if result.client != nil {
				_ = result.client.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

func createTargetClient(ctx context.Context, target Target, auth *AuthService, info ClientInfo, expected ExpectedIdentity, generation uint64) (*Client, error) {
	if target.Config.URL == "" {
		return createClient(ctx, target.Config, info)
	}
	headers, err := expandHeaders(target.Config.Name, target.Config.Headers)
	if err != nil {
		return nil, err
	}
	opts := HTTPTransportOptions{Endpoint: target.Config.URL, Headers: headers, AuthService: auth, Target: target, ExpectedIdentity: expected, ConnectionGeneration: generation}
	if auth != nil {
		opts.Client = auth.mcpHTTPClient
	}
	return NewClientWithInfo(target.Config.Name, NewHTTPTransport(opts), info), nil
}
