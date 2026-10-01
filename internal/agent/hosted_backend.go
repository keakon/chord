package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
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

const (
	hostedRouteSourceCaller = "caller"
	hostedRouteSourcePool   = "pool"
)

// hostedRouteSource identifies one routing universe: whose pool answers this
// tool's sub-requests. kind "pool" pins a named model_pool snapshot; kind
// "caller" follows one agent instance's current client. The generation is
// derived from the pool contents. Caller sources also carry a binding epoch,
// so switching away and back cannot resurrect an old request's affinity.
type hostedRouteSource struct {
	kind       string
	id         string // agent instance id ("" for the main agent) or pool name
	generation uint64
	epoch      uint64
}

func (s hostedRouteSource) key() string {
	return s.kind + ":" + s.id + "@" + strconv.FormatUint(s.generation, 16) + ":" + strconv.FormatUint(s.epoch, 16)
}

// hostedBackend runs every configured hosted tool. Routing follows a route
// source: unset model_pool follows the calling agent's current pool (the main
// agent's pool for the main view, a subagent's own pool for its view), and a
// named model_pool pins an immutable pool snapshot built at startup. Each call
// tries every capable target in turn, and each target is one independent
// sub-request through a single-target client (a shared-pool CompleteStream
// would rebuild tuning per fallback target and drop the hosted marker). The
// main pool cursor is never advanced: ModelPoolSnapshot is an explicit copy.
type hostedBackend struct {
	agent           *MainAgent
	catalog         map[string]tools.HostedToolSpec
	pools           map[string]*hostedPoolSnapshot // named model_pool snapshots, built at startup
	mu              sync.Mutex
	routes          map[hostedRouteCacheKey]hostedRoute
	seenDiag        map[hostedDiagnosticKey]struct{}
	callerBindings  map[string]hostedCallerBinding
	nextCallerEpoch uint64
}

// hostedRoute is the remembered sticky target for one route source and tool.
// The route source generation in the key carries pool identity, so no client
// pointer is stored: an entry whose source generation changed simply stops
// matching and is replaced on the next success.
type hostedRoute struct {
	provider *llm.ProviderConfig
	model    string
	variant  string
}

// hostedBackendView binds the shared backend to one default caller. Each
// SubAgent spawn builds its own view so unset model_pool routing follows that
// subagent's current pool, and availability is evaluated against the subagent
// instead of the main agent.
type hostedBackendView struct {
	backend  *hostedBackend
	callerID string // "" or the main identity = main agent; otherwise a SubAgent instance id
}

func (v *hostedBackendView) Available(tool string) bool {
	return v.backend.availableFor(v.callerID, tool)
}

func (v *hostedBackendView) Run(ctx context.Context, tool string, args map[string]any) (*message.HostedObservation, error) {
	return v.backend.runForCaller(v.callerID, ctx, tool, args)
}

func (v *hostedBackendView) ForCaller(agentID string) tools.HostedToolBackend {
	return v.backend.ForCaller(agentID)
}

// NewHostedBackend wires the hosted tool backend for the resolved catalog and
// the final merged top-level model_pools map. Keeping the effective pool map
// explicit prevents routing from accidentally reapplying project/global
// precedence with a helper that treats empty project pools as unset.
// Named model_pool snapshots are built here, after the model switch factory is
// installed, so an unknown or empty pool fails startup instead of hiding the
// tool at runtime. Registration happens after NewMainAgent; subagents clone
// their tool registry at spawn time through per-caller backend views.
func NewHostedBackend(a *MainAgent, catalog map[string]tools.HostedToolSpec, modelPools map[string][]string) (tools.HostedToolBackend, error) {
	b := &hostedBackend{
		agent:          a,
		catalog:        catalog,
		pools:          make(map[string]*hostedPoolSnapshot),
		routes:         make(map[hostedRouteCacheKey]hostedRoute),
		seenDiag:       make(map[hostedDiagnosticKey]struct{}),
		callerBindings: make(map[string]hostedCallerBinding),
	}
	for _, name := range slices.Sorted(maps.Keys(catalog)) {
		poolName := strings.TrimSpace(catalog[name].ModelPool)
		if poolName == "" {
			continue
		}
		if _, ok := b.pools[poolName]; ok {
			continue
		}
		snap, err := a.buildHostedPoolSnapshot(poolName, modelPools)
		if err != nil {
			return nil, fmt.Errorf("hosted tool %q model_pool %q: %w", name, poolName, err)
		}
		b.pools[poolName] = snap
	}
	a.setHostedBackend(b)
	return b, nil
}

// ForCaller returns a backend view whose default caller is the given agent
// instance. Unset model_pool routing follows that caller's own pool, and
// availability is evaluated against it.
func (b *hostedBackend) ForCaller(agentID string) tools.HostedToolBackend {
	return &hostedBackendView{backend: b, callerID: strings.TrimSpace(agentID)}
}

// Available reports whether at least one target in the view's routing source
// can carry the tool's declaration and has it enabled through
// compat.hosted_tools. When false the tool is withheld from the LLM tool list.
func (b *hostedBackend) Available(tool string) bool {
	return b.availableFor("", tool)
}

func (b *hostedBackend) availableFor(callerID, tool string) bool {
	spec, ok := b.catalogTool(tool)
	if !ok {
		return false
	}
	caller, err := b.resolveCaller(callerID, nil)
	if err != nil {
		return false
	}
	plan := b.planRoute(caller, spec)
	if len(plan.targets) == 0 {
		b.diagnoseOnce(plan.source, tool, plan.reason)
		return false
	}
	return true
}

// Run walks the compatible targets of the caller's routing source until one
// completes the hosted call and satisfies the success contract. Errors that no
// other target can fix (input level rejections) stop the walk; every other
// ordinary failure is attributed to the target and the next one is tried.
func (b *hostedBackend) Run(ctx context.Context, tool string, args map[string]any) (*message.HostedObservation, error) {
	return b.runForCaller("", ctx, tool, args)
}

func (b *hostedBackend) runForCaller(callerID string, ctx context.Context, tool string, args map[string]any) (*message.HostedObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	spec, ok := b.catalogTool(tool)
	if !ok {
		return nil, fmt.Errorf("hosted tool %q is not configured", tool)
	}
	caller, err := b.resolveCaller(callerID, ctx)
	if err != nil {
		return nil, err
	}
	plan := b.planRoute(caller, spec)
	if len(plan.targets) == 0 {
		b.diagnoseOnce(plan.source, tool, plan.reason)
		return nil, newHostedUnavailableError(tool, plan.unavailable)
	}
	targets := b.preferredTargets(plan, tool)
	failures := make([]hostedTargetFailure, 0, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s cancelled: %w", tool, err)
		}
		obs, err := b.runTarget(ctx, caller, target, spec, args)
		if err == nil {
			b.rememberTarget(caller, spec, plan, tool, target)
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

// hostedCaller is the resolved default caller for one hosted call: the agent
// instance, its current client for unset routing, and its effective agent
// config for named-pool authorization (nil = unrestricted).
type hostedCaller struct {
	id       string // "" for the main agent
	client   *llm.Client
	agentCfg *config.AgentConfig
	turnID   uint64 // captured when the request context does not carry one
	sub      *SubAgent
	pool     []llm.FallbackModel
	cursor   int
	source   hostedRouteSource
}

// resolveCaller validates the view default against the request context and
// resolves the caller's current client. It fails closed: a context agent id
// that does not match the view, an unknown id, or a subagent that has finished
// since spawn returns an error and never falls back to the main agent's pool.
func (b *hostedBackend) resolveCaller(callerID string, ctx context.Context) (*hostedCaller, error) {
	if ctx != nil {
		if ctxID := strings.TrimSpace(tools.AgentIDFromContext(ctx)); ctxID != "" && !b.sameCaller(callerID, ctxID) {
			return nil, fmt.Errorf("hosted tool caller mismatch: view=%q request=%q", viewCallerLabel(callerID), ctxID)
		}
	}
	if isMainCaller(callerID, b.agent.instanceID) {
		caller := &hostedCaller{agentCfg: b.agent.currentActiveConfig(), turnID: b.agent.currentTurnID()}
		b.agent.llmMu.RLock()
		defer b.agent.llmMu.RUnlock()
		caller.client = b.agent.llmClient
		return b.bindCaller(caller)
	}
	sub := b.agent.subAgentByID(callerID)
	if sub == nil {
		return nil, fmt.Errorf("hosted tool caller %q is no longer active", callerID)
	}
	caller := &hostedCaller{id: callerID, sub: sub, agentCfg: b.agent.agentConfigFor(sub.agentDefName), turnID: sub.currentTurnID()}
	sub.llmMu.RLock()
	defer sub.llmMu.RUnlock()
	caller.client = sub.llmClient
	return b.bindCaller(caller)
}

// sameCaller reports whether a context agent id names the view's caller. The
// main view accepts the main identity and the main agent's instance id; a
// subagent view matches only its own instance id.
func (b *hostedBackend) sameCaller(viewCallerID, ctxID string) bool {
	if isMainCaller(viewCallerID, b.agent.instanceID) {
		return ctxID == "" || ctxID == identity.MainAgentID || ctxID == b.agent.instanceID
	}
	return viewCallerID == ctxID
}

func isMainCaller(callerID, mainInstanceID string) bool {
	return callerID == "" || callerID == identity.MainAgentID || callerID == mainInstanceID
}

func viewCallerLabel(callerID string) string {
	if callerID == "" {
		return identity.MainAgentID
	}
	return callerID
}

// hostedRoutePlan is the resolved candidate list for one hosted call, plus the
// reason the list is empty when it is.
type hostedRoutePlan struct {
	source      hostedRouteSource
	targets     []llm.FallbackModel
	unavailable string // productized reason, "" when targets exist
	reason      string // stable category for one-time diagnostics, "" when targets exist
}

// planRoute resolves the routing source for one call and filters its targets
// by wire family and compat.hosted_tools enablement. Named pools use the
// immutable startup snapshot; unset routing snapshots the caller's current
// client pool, rotated from its cursor.
func (b *hostedBackend) planRoute(caller *hostedCaller, spec tools.HostedToolSpec) hostedRoutePlan {
	if poolName := strings.TrimSpace(spec.ModelPool); poolName != "" {
		snap := b.pools[poolName]
		source := hostedRouteSource{kind: hostedRouteSourcePool, id: poolName, generation: snap.generation}
		// agentCfg nil means the caller has no restricting role config: every
		// declared pool is authorized for it.
		if caller.agentCfg != nil && len(caller.agentCfg.Models) > 0 && !caller.agentCfg.HasPool(poolName) {
			return hostedRoutePlan{
				source:      source,
				unavailable: fmt.Sprintf("model pool %q is not in the calling agent's model_pools", poolName),
				reason:      "pool not authorized for caller",
			}
		}
		targets, reason := capableHostedTargets(snap.targets, spec)
		if len(targets) == 0 {
			if len(snap.constructionFailures) > 0 && len(snap.targets) == 0 {
				return hostedRoutePlan{
					source:      source,
					unavailable: fmt.Sprintf("all model refs failed to construct in model pool %q", poolName),
					reason:      "model ref construction failed",
				}
			}
			return hostedRoutePlan{source: source, unavailable: poolTargetReason(poolName, reason), reason: reason}
		}
		return hostedRoutePlan{source: source, targets: targets}
	}
	pool, cursor := caller.pool, caller.cursor
	if len(pool) == 0 {
		source := caller.source
		return hostedRoutePlan{source: source, unavailable: "the calling agent has no model pool", reason: "caller pool empty"}
	}
	ordered := make([]llm.FallbackModel, 0, len(pool))
	for i := range pool {
		ordered = append(ordered, pool[(cursor+i)%len(pool)])
	}
	targets, reason := capableHostedTargets(ordered, spec)
	source := caller.source
	if len(targets) == 0 {
		return hostedRoutePlan{source: source, unavailable: poolTargetReason("", reason), reason: reason}
	}
	return hostedRoutePlan{source: source, targets: targets}
}

func poolTargetReason(poolName, filterReason string) string {
	if filterReason == "no compatible protocol family" {
		return fmt.Sprintf("no Anthropic Messages or OpenAI Responses target in the routing pool%s", poolScopeSuffix(poolName))
	}
	return fmt.Sprintf("no target has the tool enabled in compat.hosted_tools%s", poolScopeSuffix(poolName))
}

func poolScopeSuffix(poolName string) string {
	if poolName == "" {
		return ""
	}
	return " in model pool " + strconv.Quote(poolName)
}

// capableHostedTargets filters targets that can lower the tool's declaration
// for their wire family and are opted in through compat.hosted_tools. The
// second return distinguishes the two ways a pool can come up empty so the
// failure message names the actual gate.
func capableHostedTargets(targets []llm.FallbackModel, spec tools.HostedToolSpec) ([]llm.FallbackModel, string) {
	out := make([]llm.FallbackModel, 0, len(targets))
	familyMatches := 0
	for _, target := range targets {
		if !hostedTargetFamilyCapable(target, spec) {
			continue
		}
		familyMatches++
		if !hostedTargetCompatCapable(target, spec) {
			continue
		}
		out = append(out, target)
	}
	if len(out) > 0 {
		return out, ""
	}
	if familyMatches == 0 {
		return nil, "no compatible protocol family"
	}
	return nil, "compat entry missing"
}

// hostedTargetFamilyCapable reports whether the target speaks a wire family
// that has a declaration for the tool.
func hostedTargetFamilyCapable(target llm.FallbackModel, spec tools.HostedToolSpec) bool {
	if target.ProviderConfig == nil || target.ProviderImpl == nil || strings.TrimSpace(target.ModelID) == "" {
		return false
	}
	family := target.ProviderConfig.Type()
	if family != config.ProviderTypeMessages && family != config.ProviderTypeResponses {
		return false
	}
	decl, ok := spec.Declarations[family]
	return ok && len(decl.Tool) > 0
}

// hostedTargetCompatCapable reports whether the target enabled the tool
// through compat.hosted_tools. Disabled by default: identifying official
// endpoints is unreliable and a wrong enable fails as a rejected request or a
// silently ignored declaration.
func hostedTargetCompatCapable(target llm.FallbackModel, spec tools.HostedToolSpec) bool {
	return slices.Contains(target.ProviderConfig.HostedToolsCompat(target.ModelID), spec.Name)
}

// hostedTargetCapable reports whether one pool target can lower the tool's
// declaration and is opted in for it.
func hostedTargetCapable(target llm.FallbackModel, spec tools.HostedToolSpec) bool {
	return hostedTargetFamilyCapable(target, spec) && hostedTargetCompatCapable(target, spec)
}

// hostedTargetsGeneration derives a route generation from the pool's target
// identities and order: a rebuild with different contents or order produces a
// different generation and invalidates sticky routes for the old contents.
func hostedTargetsGeneration(targets []llm.FallbackModel) uint64 {
	hash := fnv.New64a()
	for _, entry := range targets {
		name := ""
		if entry.ProviderConfig != nil {
			name = entry.ProviderConfig.Name()
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00", name, entry.ModelID, entry.Variant)
	}
	return hash.Sum64()
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
func (b *hostedBackend) runTarget(ctx context.Context, caller *hostedCaller, target llm.FallbackModel, spec tools.HostedToolSpec, args map[string]any) (*message.HostedObservation, error) {
	obs, err := b.runAttempt(ctx, caller, target, spec, args, false)
	if err == nil || hostedInputLevelError(err) || !hostedToolChoiceRejection(err) {
		return obs, err
	}
	// The endpoint refused the forced declaration. Retry the same target once
	// as a hint-only sub-request: the hosted tool stays declared and the
	// prompt asks for it, but nothing forces the call. The success contract is
	// unchanged, so a model that answers from memory still fails the target.
	hintObs, hintErr := b.runAttempt(ctx, caller, target, spec, args, true)
	if hintErr == nil {
		return hintObs, nil
	}
	return nil, fmt.Errorf("forced %s declaration was rejected (%v); the hint-only retry failed too: %w", spec.Name, err, hintErr)
}

// runAttempt issues one sub-request and enforces the success contract: at
// least one complete hosted call with a result payload. Zero hits count as
// success; per-call errors are annotations on a successful result. The
// sub-request model's own text never counts as a result by itself.
func (b *hostedBackend) runAttempt(ctx context.Context, caller *hostedCaller, target llm.FallbackModel, spec tools.HostedToolSpec, args map[string]any, hintOnly bool) (*message.HostedObservation, error) {
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
	defer client.Close()
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
		b.recordAttemptUsage(ctx, caller, client, spec.Name, resp)
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

func hostedToolHintSystemPrompt(tool string) string {
	return fmt.Sprintf("%s Always call the %s tool for the request before answering.", hostedToolSystemPrompt, tool)
}

// recordAttemptUsage books every attempt that produced a response, including
// attempts whose result later fails validation: those tokens were spent.
func (b *hostedBackend) recordAttemptUsage(ctx context.Context, caller *hostedCaller, client *llm.Client, tool string, resp *message.Response) {
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
	if turnID == 0 && caller != nil && caller.turnID != 0 {
		turnID = caller.turnID
	}
	if turnID == 0 {
		turnID = b.agent.currentTurnID()
	}
	if caller != nil && caller.id != "" {
		agentID, agentKind, agentName = caller.id, "sub", caller.id
		if sub := b.agent.subAgentByID(caller.id); sub != nil {
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

func newHostedUnavailableError(tool, reason string) error {
	if reason == "" {
		reason = fmt.Sprintf("no configured model target can carry its hosted declaration; add the %s entry to compat.hosted_tools for an Anthropic Messages or OpenAI Responses model", tool)
	}
	return fmt.Errorf("%s is not available: %s", tool, reason)
}

// setHostedBackend installs the shared hosted backend once at startup, after
// the hosted catalog is registered and named pool snapshots are built.
func (a *MainAgent) setHostedBackend(b tools.HostedToolBackend) {
	a.hostedBackend = b
}

// HostedBackendForCaller returns the hosted backend view whose default caller
// is the given agent instance, or nil when no hosted backend is registered.
// SubAgent spawn rebinds its cloned hosted tools through this so unset
// model_pool routing follows the subagent's own pool.
func (a *MainAgent) HostedBackendForCaller(agentID string) tools.HostedToolBackend {
	b := a.hostedBackend
	if b == nil {
		return nil
	}
	return b.ForCaller(agentID)
}

// agentConfigFor returns the named agent definition, or nil when unknown. The
// returned config's Models map is the agent's authorized pool set.
func (a *MainAgent) agentConfigFor(name string) *config.AgentConfig {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.agentConfigs[name]
}
