package agent

import (
	"context"
	"maps"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

type llmRequestWaiter struct {
	provider string
	modelRef string
	ready    chan struct{}
	granted  bool
	released atomic.Bool
}

type workspaceLease struct {
	policy   tools.ConcurrencyPolicy
	released atomic.Bool
}

type workspaceLeaseRequest struct {
	policy  tools.ConcurrencyPolicy
	ready   chan struct{}
	granted bool
	lease   *workspaceLease
}

type resourceGovernor struct {
	runtimeSlots chan struct{}
	maxBorrowed  int64
	borrowed     atomic.Int64

	llmMu          sync.Mutex
	llmLimit       int
	llmActive      int
	providerLimits map[string]int
	providerActive map[string]int
	modelLimits    map[string]int
	modelActive    map[string]int
	llmWaiters     []*llmRequestWaiter

	leaseMu      sync.Mutex
	leases       []*workspaceLease
	leaseWaiters []*workspaceLeaseRequest
}

type resourceGovernorSnapshot struct {
	RuntimeCapacity int
	RuntimeInUse    int
	BorrowedLimit   int
	BorrowedInUse   int
	LLMLimit        int
	LLMActive       int
	LLMQueued       int
	ProviderActive  map[string]int
	ModelActive     map[string]int
	LeaseActive     int
	LeaseQueued     int
}

func newResourceGovernor(cfg config.OrchestrationConfig) *resourceGovernor {
	runtimeLimit := cfg.EffectiveMaxLiveRuntimes()
	providerLimits := make(map[string]int, len(cfg.ProviderMaxActiveRequests))
	for key, limit := range cfg.ProviderMaxActiveRequests {
		key = strings.TrimSpace(key)
		if key != "" && limit > 0 {
			providerLimits[key] = limit
		}
	}
	modelLimits := make(map[string]int, len(cfg.ModelMaxActiveRequests))
	for key, limit := range cfg.ModelMaxActiveRequests {
		key = normalizedLLMModelRef(key)
		if key != "" && limit > 0 {
			modelLimits[key] = limit
		}
	}
	return &resourceGovernor{
		runtimeSlots:   make(chan struct{}, runtimeLimit),
		maxBorrowed:    int64(cfg.EffectiveMaxBorrowedRuntimes()),
		llmLimit:       cfg.EffectiveMaxActiveLLMRequests(),
		providerLimits: providerLimits,
		providerActive: make(map[string]int),
		modelLimits:    modelLimits,
		modelActive:    make(map[string]int),
		llmWaiters:     make([]*llmRequestWaiter, 0),
	}
}

func effectiveOrchestrationConfig(globalCfg, projectCfg *config.Config) config.OrchestrationConfig {
	var out config.OrchestrationConfig
	if globalCfg != nil {
		out = globalCfg.Orchestration
	}
	if projectCfg == nil {
		return out
	}
	override := projectCfg.Orchestration
	if override.MaxLiveRuntimes > 0 {
		out.MaxLiveRuntimes = override.MaxLiveRuntimes
	}
	if override.MaxBorrowedRuntimes > 0 {
		out.MaxBorrowedRuntimes = override.MaxBorrowedRuntimes
	}
	if override.MaxActiveLLMRequests > 0 {
		out.MaxActiveLLMRequests = override.MaxActiveLLMRequests
	}
	if len(override.ProviderMaxActiveRequests) > 0 {
		baseLimits := out.ProviderMaxActiveRequests
		out.ProviderMaxActiveRequests = make(map[string]int, len(baseLimits)+len(override.ProviderMaxActiveRequests))
		maps.Copy(out.ProviderMaxActiveRequests, baseLimits)
		maps.Copy(out.ProviderMaxActiveRequests, override.ProviderMaxActiveRequests)
	}
	if len(override.ModelMaxActiveRequests) > 0 {
		baseLimits := out.ModelMaxActiveRequests
		out.ModelMaxActiveRequests = make(map[string]int, len(baseLimits)+len(override.ModelMaxActiveRequests))
		maps.Copy(out.ModelMaxActiveRequests, baseLimits)
		maps.Copy(out.ModelMaxActiveRequests, override.ModelMaxActiveRequests)
	}
	return out
}

func (g *resourceGovernor) tryAcquireRuntime() bool {
	if g == nil {
		return false
	}
	select {
	case g.runtimeSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g *resourceGovernor) tryBorrowRuntime() bool {
	if g == nil || g.maxBorrowed <= 0 {
		return false
	}
	for {
		current := g.borrowed.Load()
		if current >= g.maxBorrowed {
			return false
		}
		if g.borrowed.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (g *resourceGovernor) releaseRuntime(borrowed bool) {
	if g == nil {
		return
	}
	if borrowed {
		for {
			current := g.borrowed.Load()
			if current <= 0 || g.borrowed.CompareAndSwap(current, current-1) {
				break
			}
		}
		return
	}
	select {
	case <-g.runtimeSlots:
	default:
	}
}

func llmProviderKey(ref string) string {
	ref = normalizedLLMModelRef(ref)
	if slash := strings.IndexByte(ref, '/'); slash > 0 {
		return ref[:slash]
	}
	return ref
}

func normalizedLLMModelRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if at := strings.LastIndex(ref, "@"); at > 0 {
		ref = ref[:at]
	}
	return ref
}

func (g *resourceGovernor) acquireLLM(ctx context.Context, providerRef string) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	waiter := &llmRequestWaiter{
		provider: llmProviderKey(providerRef),
		modelRef: normalizedLLMModelRef(providerRef),
		ready:    make(chan struct{}),
	}
	g.llmMu.Lock()
	g.llmWaiters = append(g.llmWaiters, waiter)
	g.dispatchLLMWaitersLocked()
	g.llmMu.Unlock()

	select {
	case <-waiter.ready:
		return func() {
			if waiter.released.CompareAndSwap(false, true) {
				g.releaseLLM(waiter.provider, waiter.modelRef)
			}
		}, nil
	case <-ctx.Done():
		g.llmMu.Lock()
		if waiter.granted {
			if waiter.released.CompareAndSwap(false, true) {
				g.llmActive--
				if waiter.provider != "" {
					g.providerActive[waiter.provider]--
				}
				if waiter.modelRef != "" {
					g.modelActive[waiter.modelRef]--
				}
			}
			g.dispatchLLMWaitersLocked()
			g.llmMu.Unlock()
			return func() {}, ctx.Err()
		}
		for i := range g.llmWaiters {
			if g.llmWaiters[i] == waiter {
				g.llmWaiters = append(g.llmWaiters[:i], g.llmWaiters[i+1:]...)
				break
			}
		}
		g.dispatchLLMWaitersLocked()
		g.llmMu.Unlock()
		return nil, ctx.Err()
	}
}

func (g *resourceGovernor) dispatchLLMWaitersLocked() {
	for g.llmActive < g.llmLimit {
		index := g.nextEligibleLLMWaiterLocked()
		if index < 0 {
			break
		}
		waiter := g.llmWaiters[index]
		g.llmWaiters = append(g.llmWaiters[:index], g.llmWaiters[index+1:]...)
		waiter.granted = true
		g.llmActive++
		if waiter.provider != "" {
			g.providerActive[waiter.provider]++
		}
		if waiter.modelRef != "" {
			g.modelActive[waiter.modelRef]++
		}
		close(waiter.ready)
	}
}

func (g *resourceGovernor) nextEligibleLLMWaiterLocked() int {
	blockedKeys := make(map[string]struct{})
	for i, waiter := range g.llmWaiters {
		key := waiter.provider + "\x00" + waiter.modelRef
		if _, blocked := blockedKeys[key]; blocked {
			continue
		}
		limit := g.providerLimits[waiter.provider]
		if limit > 0 && g.providerActive[waiter.provider] >= limit {
			blockedKeys[key] = struct{}{}
			continue
		}
		if limit := g.modelLimits[waiter.modelRef]; limit > 0 && g.modelActive[waiter.modelRef] >= limit {
			blockedKeys[key] = struct{}{}
			continue
		}
		return i
	}
	return -1
}

func (g *resourceGovernor) releaseLLM(provider, modelRef string) {
	g.llmMu.Lock()
	if g.llmActive > 0 {
		g.llmActive--
	}
	if provider != "" && g.providerActive[provider] > 0 {
		g.providerActive[provider]--
	}
	if modelRef != "" && g.modelActive[modelRef] > 0 {
		g.modelActive[modelRef]--
	}
	g.dispatchLLMWaitersLocked()
	g.llmMu.Unlock()
}

func (g *resourceGovernor) snapshot() resourceGovernorSnapshot {
	if g == nil {
		return resourceGovernorSnapshot{}
	}
	snapshot := resourceGovernorSnapshot{
		RuntimeCapacity: cap(g.runtimeSlots),
		RuntimeInUse:    len(g.runtimeSlots),
		BorrowedLimit:   int(g.maxBorrowed),
		BorrowedInUse:   int(g.borrowed.Load()),
		ProviderActive:  make(map[string]int),
		ModelActive:     make(map[string]int),
	}
	g.llmMu.Lock()
	snapshot.LLMLimit = g.llmLimit
	snapshot.LLMActive = g.llmActive
	snapshot.LLMQueued = len(g.llmWaiters)
	for provider, active := range g.providerActive {
		if active > 0 {
			snapshot.ProviderActive[provider] = active
		}
	}
	for modelRef, active := range g.modelActive {
		if active > 0 {
			snapshot.ModelActive[modelRef] = active
		}
	}
	g.llmMu.Unlock()
	g.leaseMu.Lock()
	snapshot.LeaseActive = len(g.leases)
	snapshot.LeaseQueued = len(g.leaseWaiters)
	g.leaseMu.Unlock()
	return snapshot
}

func (g *resourceGovernor) acquireWorkspaceLease(ctx context.Context, policy tools.ConcurrencyPolicy) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	request := &workspaceLeaseRequest{
		policy: normalizeWorkspaceLeasePolicy(policy),
		ready:  make(chan struct{}),
	}
	g.leaseMu.Lock()
	g.leaseWaiters = append(g.leaseWaiters, request)
	g.dispatchWorkspaceLeasesLocked()
	g.leaseMu.Unlock()

	select {
	case <-request.ready:
		return func() { g.releaseWorkspaceLease(request.lease) }, nil
	case <-ctx.Done():
		g.leaseMu.Lock()
		if request.granted {
			lease := request.lease
			g.leaseMu.Unlock()
			g.releaseWorkspaceLease(lease)
			return func() {}, ctx.Err()
		}
		for i, queued := range g.leaseWaiters {
			if queued == request {
				g.leaseWaiters = append(g.leaseWaiters[:i], g.leaseWaiters[i+1:]...)
				break
			}
		}
		g.dispatchWorkspaceLeasesLocked()
		g.leaseMu.Unlock()
		return nil, ctx.Err()
	}
}

func normalizeWorkspaceLeasePolicy(policy tools.ConcurrencyPolicy) tools.ConcurrencyPolicy {
	if policy.Mode == "" {
		policy.Mode = tools.ConcurrencyModeExclusive
	}
	if strings.TrimSpace(policy.Resource) == "" {
		policy.Resource = "workspace"
	}
	return policy
}

func (g *resourceGovernor) dispatchWorkspaceLeasesLocked() {
	for {
		grantIndex := -1
		for i, request := range g.leaseWaiters {
			if g.workspaceLeaseConflictsLocked(request.policy) {
				continue
			}
			blockedByEarlier := false
			for _, earlier := range g.leaseWaiters[:i] {
				if tools.WorkspaceLeaseConflict(earlier.policy, request.policy) {
					blockedByEarlier = true
					break
				}
			}
			if !blockedByEarlier {
				grantIndex = i
				break
			}
		}
		if grantIndex < 0 {
			return
		}
		request := g.leaseWaiters[grantIndex]
		g.leaseWaiters = append(g.leaseWaiters[:grantIndex], g.leaseWaiters[grantIndex+1:]...)
		request.granted = true
		request.lease = &workspaceLease{policy: request.policy}
		g.leases = append(g.leases, request.lease)
		close(request.ready)
	}
}

func (g *resourceGovernor) workspaceLeaseConflictsLocked(policy tools.ConcurrencyPolicy) bool {
	for _, lease := range g.leases {
		if lease != nil && !lease.released.Load() && tools.WorkspaceLeaseConflict(lease.policy, policy) {
			return true
		}
	}
	return false
}

func (g *resourceGovernor) releaseWorkspaceLease(lease *workspaceLease) {
	if g == nil || lease == nil || !lease.released.CompareAndSwap(false, true) {
		return
	}
	g.leaseMu.Lock()
	for i, active := range g.leases {
		if active == lease {
			g.leases = append(g.leases[:i], g.leases[i+1:]...)
			break
		}
	}
	g.dispatchWorkspaceLeasesLocked()
	g.leaseMu.Unlock()
}
