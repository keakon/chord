package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

func waitForGovernorSnapshot(t *testing.T, g *resourceGovernor, predicate func(resourceGovernorSnapshot) bool) resourceGovernorSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot := g.snapshot()
		if predicate(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("governor condition not met; last snapshot = %+v", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestResourceGovernorUsesConfiguredLimits(t *testing.T) {
	g := newResourceGovernor(config.OrchestrationConfig{
		MaxLiveRuntimes:           2,
		MaxBorrowedRuntimes:       3,
		MaxActiveLLMRequests:      4,
		ProviderMaxActiveRequests: map[string]int{"openai": 1},
	})
	got := g.snapshot()
	if got.RuntimeCapacity != 2 || got.BorrowedLimit != 3 || got.LLMLimit != 4 {
		t.Fatalf("limits = %+v", got)
	}
	if g.providerLimits["openai"] != 1 {
		t.Fatalf("provider limits = %#v", g.providerLimits)
	}
}

func TestResourceGovernorBoundsBorrowedRuntimeDebt(t *testing.T) {
	g := newResourceGovernor(config.OrchestrationConfig{MaxLiveRuntimes: 1, MaxBorrowedRuntimes: 1})
	if !g.tryAcquireRuntime() {
		t.Fatal("normal acquire failed with a free slot")
	}
	if g.tryAcquireRuntime() {
		t.Fatal("normal acquire succeeded beyond the live limit")
	}
	if !g.tryBorrowRuntime() {
		t.Fatal("borrow failed with free borrow debt")
	}
	if g.tryBorrowRuntime() {
		t.Fatal("borrow succeeded beyond the borrowed debt limit")
	}
	if got := g.snapshot().BorrowedInUse; got != 1 {
		t.Fatalf("borrowed in use = %d, want 1", got)
	}
	g.releaseRuntime(true)
	g.releaseRuntime(false)
	if got := g.snapshot(); got.RuntimeInUse != 0 || got.BorrowedInUse != 0 {
		t.Fatalf("after release = %+v", got)
	}
}

func TestResourceGovernorWorkspaceLeaseScopesExclusiveByResource(t *testing.T) {
	g := newResourceGovernor(config.OrchestrationConfig{})
	releaseShell, err := g.acquireWorkspaceLease(context.Background(), tools.ConcurrencyPolicy{Resource: "process:shell", Mode: tools.ConcurrencyModeExclusive})
	if err != nil {
		t.Fatal(err)
	}
	// An unrelated file access must not queue behind a running shell command.
	releaseRead, err := g.acquireWorkspaceLease(context.Background(), tools.ConcurrencyPolicy{Resource: "file:/workspace/a", Mode: tools.ConcurrencyModeRead})
	if err != nil {
		t.Fatal(err)
	}
	releaseRead()
	// A second shell still serializes on the shared process resource.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.acquireWorkspaceLease(ctx, tools.ConcurrencyPolicy{Resource: "process:shell", Mode: tools.ConcurrencyModeExclusive}); err == nil {
		t.Fatal("second shell lease succeeded while the first was held")
	}
	releaseShell()
	if got := g.snapshot(); got.LeaseActive != 0 || got.LeaseQueued != 0 {
		t.Fatalf("after release = %+v", got)
	}
}

func TestAcquireWakeReactivationSlotFallsBackToUncountedBypass(t *testing.T) {
	g := newResourceGovernor(config.OrchestrationConfig{MaxLiveRuntimes: 1, MaxBorrowedRuntimes: 1})
	a := &MainAgent{parentCtx: context.Background(), governor: g, sem: g.runtimeSlots}
	first, second, third := &SubAgent{}, &SubAgent{}, &SubAgent{}

	if err := a.acquireWakeReactivationSlot(first); err != nil {
		t.Fatal(err)
	}
	if err := a.acquireWakeReactivationSlot(second); err != nil {
		t.Fatal(err)
	}
	if !second.semBorrowed {
		t.Fatal("second wake grant did not borrow with the live pool full")
	}

	// Both pools are exhausted; the wake path runs on the main event loop and
	// must degrade to an uncounted bypass instead of blocking.
	done := make(chan error, 1)
	go func() { done <- a.acquireWakeReactivationSlot(third) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wake reactivation blocked with exhausted pools")
	}
	if !third.semHeld || third.semBorrowed || !third.semBypassed {
		t.Fatalf("third slot flags = held:%v borrowed:%v bypassed:%v, want uncounted bypass", third.semHeld, third.semBorrowed, third.semBypassed)
	}
	if got := a.orchestrationMetrics.runtimeBypassGrants.Load(); got != 1 {
		t.Fatalf("runtimeBypassGrants = %d, want 1", got)
	}
	if stats := a.OrchestrationStats(); stats.RuntimeBypassActive != 1 || stats.RuntimeBypassPeak != 1 {
		t.Fatalf("runtime bypass stats = active:%d peak:%d, want 1/1", stats.RuntimeBypassActive, stats.RuntimeBypassPeak)
	}

	a.releaseSubAgentSlot(third)
	if stats := a.OrchestrationStats(); stats.RuntimeBypassActive != 0 || stats.RuntimeBypassPeak != 1 {
		t.Fatalf("runtime bypass stats after release = active:%d peak:%d, want 0/1", stats.RuntimeBypassActive, stats.RuntimeBypassPeak)
	}
	if got := g.snapshot(); got.RuntimeInUse != 1 || got.BorrowedInUse != 1 {
		t.Fatalf("bypass release changed counted pools = %+v", got)
	}
	a.releaseSubAgentSlot(second)
	a.releaseSubAgentSlot(first)
	if got := g.snapshot(); got.RuntimeInUse != 0 || got.BorrowedInUse != 0 {
		t.Fatalf("after all releases = %+v", got)
	}
}

func TestResourceGovernorAllowsOtherProviderPastBlockedProvider(t *testing.T) {
	g := newResourceGovernor(config.OrchestrationConfig{MaxActiveLLMRequests: 2, ProviderMaxActiveRequests: map[string]int{"openai": 1}})
	releaseOpenAI, err := g.acquireLLM(context.Background(), "openai/gpt")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOpenAI()

	releaseOther, err := g.acquireLLM(context.Background(), "anthropic/claude")
	if err != nil {
		t.Fatal(err)
	}
	releaseOther()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.acquireLLM(ctx, "openai/gpt"); err == nil {
		t.Fatal("second request for capped provider succeeded")
	}
	if got := g.snapshot().LLMActive; got != 1 {
		t.Fatalf("active requests after cancelled waiter = %d, want 1", got)
	}
}

func TestResourceGovernorLLMReleaseIsIdempotent(t *testing.T) {
	g := newResourceGovernor(config.OrchestrationConfig{MaxActiveLLMRequests: 2})
	releaseOpenAI, err := g.acquireLLM(context.Background(), "openai/gpt")
	if err != nil {
		t.Fatal(err)
	}
	releaseAnthropic, err := g.acquireLLM(context.Background(), "anthropic/claude")
	if err != nil {
		t.Fatal(err)
	}

	releaseOpenAI()
	releaseOpenAI()
	if got := g.snapshot(); got.LLMActive != 1 || got.ProviderActive["anthropic"] != 1 {
		t.Fatalf("after duplicate release = %+v, want only anthropic active", got)
	}
	releaseAnthropic()
	if got := g.snapshot().LLMActive; got != 0 {
		t.Fatalf("active requests after release = %d, want 0", got)
	}
}

func TestResourceGovernorWorkspaceLeasesAllowDisjointWrites(t *testing.T) {
	g := newResourceGovernor(config.OrchestrationConfig{})
	releaseA, err := g.acquireWorkspaceLease(context.Background(), tools.ConcurrencyPolicy{Resource: "file:/workspace/a", Mode: tools.ConcurrencyModeWrite})
	if err != nil {
		t.Fatal(err)
	}
	releaseB, err := g.acquireWorkspaceLease(context.Background(), tools.ConcurrencyPolicy{Resource: "file:/workspace/b", Mode: tools.ConcurrencyModeWrite})
	if err != nil {
		t.Fatal(err)
	}
	if got := g.snapshot(); got.LeaseActive != 2 || got.LeaseQueued != 0 {
		t.Fatalf("disjoint lease snapshot = %+v", got)
	}
	releaseA()
	releaseB()
}

func TestResourceGovernorWorkspaceLeasesPreserveConflictingWaiterOrder(t *testing.T) {
	g := newResourceGovernor(config.OrchestrationConfig{})
	resource := "file:/workspace/a"
	releaseInitial, err := g.acquireWorkspaceLease(context.Background(), tools.ConcurrencyPolicy{Resource: resource, Mode: tools.ConcurrencyModeRead})
	if err != nil {
		t.Fatal(err)
	}

	type leaseResult struct {
		release func()
		err     error
	}
	writerResult := make(chan leaseResult, 1)
	go func() {
		release, err := g.acquireWorkspaceLease(context.Background(), tools.ConcurrencyPolicy{Resource: resource, Mode: tools.ConcurrencyModeWrite})
		writerResult <- leaseResult{release: release, err: err}
	}()
	waitForGovernorSnapshot(t, g, func(snapshot resourceGovernorSnapshot) bool { return snapshot.LeaseQueued == 1 })

	readerResult := make(chan leaseResult, 1)
	go func() {
		release, err := g.acquireWorkspaceLease(context.Background(), tools.ConcurrencyPolicy{Resource: resource, Mode: tools.ConcurrencyModeRead})
		readerResult <- leaseResult{release: release, err: err}
	}()
	waitForGovernorSnapshot(t, g, func(snapshot resourceGovernorSnapshot) bool { return snapshot.LeaseQueued == 2 })

	releaseInitial()
	writer := <-writerResult
	if writer.err != nil {
		t.Fatal(writer.err)
	}
	select {
	case reader := <-readerResult:
		if reader.release != nil {
			reader.release()
		}
		t.Fatal("later reader bypassed queued writer")
	case <-time.After(20 * time.Millisecond):
	}
	writer.release()
	reader := <-readerResult
	if reader.err != nil {
		t.Fatal(reader.err)
	}
	reader.release()
	if got := g.snapshot(); got.LeaseActive != 0 || got.LeaseQueued != 0 {
		t.Fatalf("after lease release = %+v", got)
	}
}

func TestResourceGovernorWorkspaceLeaseCancellationRemovesWaiter(t *testing.T) {
	g := newResourceGovernor(config.OrchestrationConfig{})
	release, err := g.acquireWorkspaceLease(context.Background(), tools.ConcurrencyPolicy{Resource: "workspace", Mode: tools.ConcurrencyModeExclusive})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.acquireWorkspaceLease(ctx, tools.ConcurrencyPolicy{Resource: "file:/workspace/a", Mode: tools.ConcurrencyModeRead}); err == nil {
		t.Fatal("conflicting lease acquire succeeded")
	}
	if got := g.snapshot(); got.LeaseActive != 1 || got.LeaseQueued != 0 {
		t.Fatalf("after cancelled lease waiter = %+v", got)
	}
	release()
	release()
	if got := g.snapshot().LeaseActive; got != 0 {
		t.Fatalf("active leases after duplicate release = %d, want 0", got)
	}
}

func TestAcquireWakeReactivationSlotIsIdempotent(t *testing.T) {
	parentCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	g := newResourceGovernor(config.OrchestrationConfig{MaxLiveRuntimes: 1, MaxBorrowedRuntimes: 1})
	a := &MainAgent{parentCtx: parentCtx, governor: g, sem: g.runtimeSlots}
	sub := &SubAgent{}

	const callers = 32
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			errs <- a.acquireWakeReactivationSlot(sub)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent slot acquire: %v", err)
		}
	}
	if got := g.snapshot(); got.RuntimeInUse+got.BorrowedInUse != 1 {
		t.Fatalf("concurrent acquire held %+v, want exactly one runtime", got)
	}
	a.releaseSubAgentSlot(sub)
}

func TestResourceGovernorProviderKeyStripsVariant(t *testing.T) {
	for ref, want := range map[string]string{
		"openai/gpt-5.5@high": "openai",
		"anthropic/claude":    "anthropic",
		"local-model":         "local-model",
	} {
		if got := llmProviderKey(ref); got != want {
			t.Fatalf("llmProviderKey(%q) = %q, want %q", ref, got, want)
		}
	}
}
