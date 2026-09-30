package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const (
	// hostedToolSystemPrompt is deliberately minimal: the sub-request carries
	// no main-conversation history, and the hosted tool is declared through
	// request tuning.
	hostedToolSystemPrompt = "You are a tool-use assistant. Run the requested tool and reply with a short factual summary of its output."
	// hostedToolMaxOutputTokens bounds one sub-request's summary text.
	hostedToolMaxOutputTokens = 4096
	// hostedToolRetryRounds bounds transient recovery per capable target.
	hostedToolRetryRounds = 3
)

// hostedBackend runs every configured hosted tool over the main agent's model
// pool. It implements tools.HostedToolBackend for the whole catalog: each call
// tries every capable target in turn, and each target is one independent
// sub-request through a single-target client (a shared-pool CompleteStream
// would rebuild tuning per fallback target and drop the hosted marker). The
// main pool cursor is never advanced: ModelPoolSnapshot is an explicit copy.
type hostedBackend struct {
	agent   *MainAgent
	catalog map[string]tools.HostedToolSpec
	mu      sync.Mutex
	routes  map[string]hostedRoute
}

type hostedRoute struct {
	client   *llm.Client
	provider *llm.ProviderConfig
	model    string
	variant  string
}

// NewHostedBackend wires the hosted tool backend for the resolved catalog.
// Registration happens after NewMainAgent; subagents clone their tool registry
// at spawn time, so they see the tools too.
func NewHostedBackend(a *MainAgent, catalog map[string]tools.HostedToolSpec) tools.HostedToolBackend {
	return &hostedBackend{agent: a, catalog: catalog}
}

// Available reports whether at least one pool target can carry the tool's
// declaration and has it enabled through compat.hosted_tools. When false the
// tool is withheld from the LLM tool list.
func (b *hostedBackend) Available(tool string) bool {
	targets, _ := b.capableTargets(tool)
	return len(targets) > 0
}

// Run walks the capable targets until one completes the hosted call and
// satisfies the success contract. Errors that no other target can fix (input
// level rejections) stop the walk; every other failure is attributed to the
// target and the next one is tried.
func (b *hostedBackend) Run(ctx context.Context, tool string, args map[string]any) (*message.HostedObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	spec, ok := b.catalogTool(tool)
	if !ok {
		return nil, fmt.Errorf("hosted tool %q is not configured", tool)
	}
	targets, mainClient := b.capableTargets(tool)
	if len(targets) == 0 {
		return nil, newHostedUnavailableError(tool)
	}
	targets = b.preferredTargets(tool, mainClient, targets)
	failures := make([]hostedTargetFailure, 0, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s cancelled: %w", tool, err)
		}
		obs, err := b.runTarget(ctx, target, spec, args)
		if err == nil {
			b.rememberTarget(tool, mainClient, target)
			return obs, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%s cancelled: %w", tool, ctxErr)
		}
		if _, approval := errors.AsType[*hostedApprovalRequiredError](err); approval {
			return nil, err
		}
		if _, unknown := errors.AsType[*llm.HostedOutcomeUnknownError](err); unknown {
			return nil, err
		}
		if hostedInputLevelError(err) {
			return nil, fmt.Errorf("%s request was rejected before running: %w", tool, err)
		}
		failures = append(failures, hostedTargetFailure{target: hostedTargetRef(target), cause: err})
	}
	return nil, newHostedAllTargetsFailedError(tool, failures)
}

// preferredTargets keeps a per-tool cursor independent of the main request
// cursor. A remembered route must still belong to this client's capable pool;
// removed targets and replaced clients never re-enter through this cache.
func (b *hostedBackend) preferredTargets(tool string, client *llm.Client, targets []llm.FallbackModel) []llm.FallbackModel {
	b.mu.Lock()
	route, ok := b.routes[tool]
	b.mu.Unlock()
	if !ok || route.client != client {
		return targets
	}
	for i, target := range targets {
		if target.ProviderConfig == route.provider && target.ModelID == route.model && target.Variant == route.variant {
			if i == 0 {
				return targets
			}
			return slices.Concat(targets[i:], targets[:i])
		}
	}
	return targets
}

func (b *hostedBackend) rememberTarget(tool string, client *llm.Client, target llm.FallbackModel) {
	b.agent.llmMu.RLock()
	defer b.agent.llmMu.RUnlock()
	if b.agent.llmClient != client {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.routes == nil {
		b.routes = make(map[string]hostedRoute)
	}
	b.routes[tool] = hostedRoute{client: client, provider: target.ProviderConfig, model: target.ModelID, variant: target.Variant}
}

// catalogTool looks up one catalog entry, defaulting a missing name to the
// lookup key so error paths stay self-describing.
func (b *hostedBackend) catalogTool(tool string) (tools.HostedToolSpec, bool) {
	if b == nil {
		return tools.HostedToolSpec{}, false
	}
	spec, ok := b.catalog[tool]
	if !ok {
		return tools.HostedToolSpec{}, false
	}
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = tool
	}
	return spec, true
}

// capableTargets snapshots the pool entries that can carry the tool's
// declaration for their wire family and are opted in via compat. The returned
// client is the main client the snapshot came from (nil when no session model
// is installed).
func (b *hostedBackend) capableTargets(tool string) ([]llm.FallbackModel, *llm.Client) {
	spec, ok := b.catalogTool(tool)
	if !ok || b.agent == nil {
		return nil, nil
	}
	b.agent.llmMu.RLock()
	mainClient := b.agent.llmClient
	b.agent.llmMu.RUnlock()
	if mainClient == nil {
		return nil, nil
	}
	pool, cursor := mainClient.ModelPoolSnapshot()
	targets := make([]llm.FallbackModel, 0, len(pool))
	for i := range pool {
		target := pool[(cursor+i)%len(pool)]
		if hostedTargetCapable(target, spec) {
			targets = append(targets, target)
		}
	}
	return targets, mainClient
}

// hostedTargetCapable reports whether one pool target can lower the tool's
// declaration (Anthropic Messages or OpenAI Responses) and has the capability
// enabled. Disabled by default: identifying official endpoints is unreliable
// and a wrong enable fails as a rejected request or a silently ignored
// declaration.
func hostedTargetCapable(target llm.FallbackModel, spec tools.HostedToolSpec) bool {
	if target.ProviderConfig == nil || target.ProviderImpl == nil || strings.TrimSpace(target.ModelID) == "" {
		return false
	}
	family := target.ProviderConfig.Type()
	switch family {
	case config.ProviderTypeMessages, config.ProviderTypeResponses:
	default:
		return false
	}
	decl, ok := spec.Declarations[family]
	if !ok || len(decl.Tool) == 0 {
		return false
	}
	return slices.Contains(target.ProviderConfig.HostedToolsCompat(target.ModelID), spec.Name)
}

func hostedTargetRef(target llm.FallbackModel) string {
	name := ""
	if target.ProviderConfig != nil {
		name = target.ProviderConfig.Name()
	}
	if name == "" {
		name = "unknown"
	}
	return name + "/" + target.ModelID
}

// runTarget issues the sub-request for one target: first with the forced
// declaration, then, when the endpoint refuses the forced tool_choice, once
// more as a hint-only sub-request.
func (b *hostedBackend) runTarget(ctx context.Context, target llm.FallbackModel, spec tools.HostedToolSpec, args map[string]any) (*message.HostedObservation, error) {
	obs, err := b.runAttempt(ctx, target, spec, args, false)
	if err == nil || hostedInputLevelError(err) || !hostedToolChoiceRejection(err) {
		return obs, err
	}
	// The endpoint refused the forced declaration. Retry the same target once
	// as a hint-only sub-request: the hosted tool stays declared and the
	// prompt asks for it, but nothing forces the call. The success contract is
	// unchanged, so a model that answers from memory still fails the target.
	hintObs, hintErr := b.runAttempt(ctx, target, spec, args, true)
	if hintErr == nil {
		return hintObs, nil
	}
	return nil, fmt.Errorf("forced %s declaration was rejected (%v); the hint-only retry failed too: %w", spec.Name, err, hintErr)
}

// runAttempt issues one sub-request and enforces the success contract: at
// least one complete hosted call with a result payload. Zero hits count as
// success; per-call errors are annotations on a successful result. The
// sub-request model's own text never counts as a result by itself.
func (b *hostedBackend) runAttempt(ctx context.Context, target llm.FallbackModel, spec tools.HostedToolSpec, args map[string]any, hintOnly bool) (*message.HostedObservation, error) {
	decl, err := tools.ResolveHostedDeclaration(spec, target.ProviderConfig.Type(), args)
	if err != nil {
		return nil, err
	}
	prompt := tools.RenderHostedPrompt(spec, args)
	clientTarget := target
	clientTarget.ProviderImpl = hostedRequestProvider{Provider: target.ProviderImpl, governor: b.agent.governor, providerName: target.ProviderConfig.Name()}
	client := newAuxClientFromPool([]llm.FallbackModel{clientTarget}, 0, hostedToolMaxOutputTokens, b.agent.ServiceTier())
	if client == nil {
		return nil, fmt.Errorf("could not build a client for the target")
	}
	systemPrompt := hostedToolSystemPrompt
	if hintOnly {
		systemPrompt = hostedToolHintSystemPrompt(spec.Name)
	}
	client.SetSystemPrompt(systemPrompt)
	if spec.RetrySafe {
		client.SetStreamRetryRounds(hostedToolRetryRounds)
	} else {
		client.SetStreamRetryRounds(1)
	}
	hosted := &llm.HostedToolRequest{
		Name:        spec.Name,
		Declaration: decl.Tool,
		Include:     decl.Include,
		Headers:     decl.Headers,
		RetrySafe:   spec.RetrySafe,
	}
	if !hintOnly {
		hosted.Force = decl.Force
	}
	var combined *message.HostedObservation
	for continuation := 0; ; continuation++ {
		if continuation > 0 {
			if err := b.checkContinuation(ctx, target, spec, args); err != nil {
				return nil, &llm.HostedOutcomeUnknownError{Cause: err}
			}
		}
		client.SetNextRequestTuningOverride(llm.RequestTuning{HostedTool: hosted})
		resp, err := client.CompleteStream(ctx, []message.Message{{Role: message.RoleUser, Content: prompt}}, nil, func(message.StreamDelta) {})
		if unknown, ok := errors.AsType[*llm.HostedOutcomeUnknownError](err); ok && resp == nil {
			resp = unknown.Response
		}
		b.recordAttemptUsage(ctx, client, spec.Name, resp)
		if err != nil {
			if continuation > 0 && ctx.Err() == nil {
				return nil, &llm.HostedOutcomeUnknownError{Cause: err, Response: resp}
			}
			return nil, err
		}
		if resp != nil && resp.Hosted != nil && resp.Hosted.RequiresApproval {
			return nil, &hostedApprovalRequiredError{tool: spec.Name}
		}
		if resp != nil {
			combined = mergeHostedObservations(combined, resp.Hosted)
		}
		if resp != nil && resp.StopReason == hostedPauseTurn {
			if continuation >= maxHostedContinuations || resp.Hosted == nil || len(resp.Hosted.Items) == 0 {
				return nil, &llm.HostedOutcomeUnknownError{Cause: fmt.Errorf("%s paused without a completed turn; continuation limit or native content unavailable", spec.Name), Response: resp}
			}
			if len(hosted.Messages) == 0 {
				user, marshalErr := json.Marshal(map[string]any{"role": "user", "content": prompt})
				if marshalErr != nil {
					return nil, fmt.Errorf("marshal hosted user message: %w", marshalErr)
				}
				hosted.Messages = append(hosted.Messages, user)
			}
			assistant, marshalErr := json.Marshal(map[string]any{"role": "assistant", "content": resp.Hosted.Items})
			if marshalErr != nil {
				return nil, fmt.Errorf("marshal hosted continuation: %w", marshalErr)
			}
			hosted.Messages = append(hosted.Messages, assistant)
			hosted.Container = combined.Container
			continue
		}
		if resp != nil {
			resp.Hosted = combined
		}
		if !spec.RetrySafe && combined != nil && combined.PendingCalls() > 0 {
			return nil, &llm.HostedOutcomeUnknownError{Cause: fmt.Errorf("%s has unfinished calls", spec.Name), Response: resp}
		}
		obs, validationErr := hostedObservationFromResponse(spec.Name, resp)
		if _, approval := errors.AsType[*hostedApprovalRequiredError](validationErr); approval {
			return nil, validationErr
		}
		if validationErr != nil && !spec.RetrySafe && ((combined != nil && len(combined.Calls) > 0) || (resp != nil && resp.StopReason == "interrupted")) {
			return nil, &llm.HostedOutcomeUnknownError{Cause: validationErr, Response: resp}
		}
		return obs, validationErr
	}
}

// hostedToolHintSystemPrompt backs the degraded retry: the endpoint refused
// to force the hosted tool, so the prompt has to ask for the call while the
// success contract still requires an observed hosted call.
func hostedToolHintSystemPrompt(tool string) string {
	return fmt.Sprintf("%s Always call the %s tool for the request before answering.", hostedToolSystemPrompt, tool)
}

// recordAttemptUsage books every attempt that produced a response, including
// attempts whose result later fails validation: those tokens were spent.
func (b *hostedBackend) recordAttemptUsage(ctx context.Context, client *llm.Client, tool string, resp *message.Response) {
	if b == nil || b.agent == nil || resp == nil {
		return
	}
	selectedRef := client.PrimaryModelRef()
	runningRef := client.RunningModelRef()
	serviceTier := client.LastCallStatus().ServiceTier
	if serviceTier == "" {
		serviceTier = client.EffectiveServiceTierForModelRef(runningRef)
	}
	var diagnostic map[string]string
	if resp.Hosted != nil {
		diagnostic = map[string]string{tool + "_requests": strconv.Itoa(len(resp.Hosted.Calls))}
	}
	agentID, agentKind, agentName := identity.MainAgentID, identity.MainAgentID, b.agent.currentAgentName()
	turnID := tools.TurnIDFromContext(ctx)
	if turnID == 0 {
		turnID = b.agent.currentTurnID()
	}
	if id := tools.AgentIDFromContext(ctx); id != "" && id != b.agent.instanceID && id != identity.MainAgentID {
		agentID, agentKind, agentName = id, "sub", id
		if sub := b.agent.subAgentByID(id); sub != nil {
			agentName = sub.agentDefName
		}
	}
	b.agent.recordUsage(agentID, agentKind, agentName, tool, selectedRef, runningRef, turnID, resp.Usage, serviceTier, diagnostic)
}

// hostedObservationFromResponse enforces the success contract and annotates
// the calls that never completed, so the tool result reports the partial
// failure instead of presenting the run as fully successful.
func hostedObservationFromResponse(tool string, resp *message.Response) (*message.HostedObservation, error) {
	if resp == nil {
		return nil, fmt.Errorf("hosted %s returned no response", tool)
	}
	obs := resp.Hosted
	if obs == nil {
		return nil, &llm.HostedCallNotObservedError{Tool: tool}
	}
	if obs.RequiresApproval {
		return nil, &hostedApprovalRequiredError{tool: tool}
	}
	if resp.StopReason == hostedPauseTurn {
		return nil, fmt.Errorf("%s turn is paused and has not completed", tool)
	}
	if resp.StopReason == "interrupted" {
		return nil, fmt.Errorf("%s stream was interrupted before the call completed", tool)
	}
	if !obs.HasCompleteResult() {
		if pending := obs.PendingCalls(); pending > 0 {
			return nil, fmt.Errorf("%s did not complete (%d call(s) without a result)", tool, pending)
		}
		if errs := obs.CallErrors(); len(errs) > 0 {
			return nil, fmt.Errorf("%s returned only errors: %s", tool, strings.Join(errs, "; "))
		}
		return nil, &llm.HostedCallNotObservedError{Tool: tool}
	}
	for i := range obs.Calls {
		call := &obs.Calls[i]
		if len(call.Result) > 0 || call.Error != "" {
			continue
		}
		call.Error = tool + " call did not complete before the stream ended"
	}
	return obs, nil
}

// hostedInputLevelError reports rejections that no other target can fix: the
// request itself is invalid (for example an over-long query), so the pool walk
// stops instead of replaying a doomed request against every provider. The
// generic invalid_request_error bucket is deliberately absent: it is also how
// endpoints report a rejected hosted declaration, which the hint-only retry
// and the next target can still serve.
func hostedInputLevelError(err error) bool {
	apiErr, ok := errors.AsType[*llm.APIError](err)
	if !ok || apiErr == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(apiErr.Code)) {
	case "query_too_long", "invalid_tool_input":
		return true
	default:
		return false
	}
}

// hostedToolChoiceRejection reports the 4xx class that means the endpoint
// refused the forced hosted declaration or tool_choice: the hint-only retry
// may still get a call through. Auth, not-found, and rate-limit statuses are
// excluded because they are not about the declaration.
func hostedToolChoiceRejection(err error) bool {
	apiErr, ok := errors.AsType[*llm.APIError](err)
	if !ok || apiErr == nil {
		return false
	}
	if apiErr.Origin == llm.APIErrorOriginSSEEvent || apiErr.Origin == llm.APIErrorOriginWebSocketEvent {
		return false
	}
	if apiErr.StatusCode != 400 && apiErr.StatusCode != 422 {
		return false
	}
	detail := strings.ToLower(apiErr.Code + " " + apiErr.Message)
	return strings.Contains(detail, "tool_choice") || strings.Contains(detail, "tool choice") || strings.Contains(detail, "forced tool") || strings.Contains(detail, "force tool")
}

func newHostedUnavailableError(tool string) error {
	return fmt.Errorf("%s is not available: no configured model target can carry its hosted declaration; add the %s entry to compat.hosted_tools for an Anthropic Messages or OpenAI Responses model", tool, tool)
}
