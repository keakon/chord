package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

func TestCreateSubAgentFromSubAgentContextSetsOwnerAndDepth(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}

	handle, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), tools.SubAgentRequest{Description: "child work", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if handle.Status != "started" {
		t.Fatalf("handle.Status = %q, want started", handle.Status)
	}
	child := a.subAgentByTaskID(handle.TaskID)
	if child == nil {
		t.Fatal("expected child SubAgent to exist")
	}
	if child.ownerAgentID != parent.instanceID {
		t.Fatalf("child.ownerAgentID = %q, want %q", child.ownerAgentID, parent.instanceID)
	}
	if child.ownerTaskID != parent.taskID {
		t.Fatalf("child.ownerTaskID = %q, want %q", child.ownerTaskID, parent.taskID)
	}
	if child.depth != 2 {
		t.Fatalf("child.depth = %d, want 2", child.depth)
	}
	rec := a.taskRecordByTaskID(handle.TaskID)
	if rec == nil {
		t.Fatal("expected task record for child")
	}
	if rec.OwnerAgentID != parent.instanceID || rec.OwnerTaskID != parent.taskID {
		t.Fatalf("task record owner = (%q,%q), want (%q,%q)", rec.OwnerAgentID, rec.OwnerTaskID, parent.instanceID, parent.taskID)
	}
	if !rec.JoinToOwner {
		t.Fatal("expected child task record to default to join-to-owner for SubAgent caller")
	}
}

func TestCreateSubAgentRejectsTargetDeniedForCaller(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	a.activeConfig.Permission = parsePermissionNode(t, `
"*": allow
delegate:
  "*": deny
  reviewer: allow
`)
	a.rebuildRuleset()

	_, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "child work", AgentType: "worker"})
	if err == nil || !strings.Contains(err.Error(), "denied by Delegate permission policy") {
		t.Fatalf("CreateSubAgent() err = %v, want Delegate target denial", err)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("CreateSubAgent() consumed %d semaphore slots for denied target", got)
	}
}

func TestCreateSubAgentReturnsChildLimitReachedForDirectOwner(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 1, MaxDepth: 2}

	first, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), tools.SubAgentRequest{Description: "child one", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent(first): %v", err)
	}
	if first.Status != "started" {
		t.Fatalf("first.Status = %q, want started", first.Status)
	}
	second, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), tools.SubAgentRequest{Description: "child two", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent(second): %v", err)
	}
	if second.Status != "child_limit_reached" {
		t.Fatalf("second.Status = %q, want child_limit_reached", second.Status)
	}
}

func TestCreateSubAgentCountsNonTerminalDirectChildrenForLimit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 1, MaxDepth: 2}

	first, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), tools.SubAgentRequest{Description: "child one", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent(first): %v", err)
	}
	child := a.subAgentByTaskID(first.TaskID)
	if child == nil {
		t.Fatal("expected first child SubAgent to exist")
	}
	child.setState(SubAgentStateWaitingMain, "need decision")
	a.noteSubAgentStateTransition(child, SubAgentStateWaitingMain)
	a.syncTaskRecordFromSub(child, "")

	second, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), tools.SubAgentRequest{Description: "child two", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent(second): %v", err)
	}
	if second.Status != "child_limit_reached" {
		t.Fatalf("second.Status = %q, want child_limit_reached", second.Status)
	}
}

func TestConcurrentCreateSubAgentRespectsDirectChildLimit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 1, MaxDepth: 2}
	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)

	start := make(chan struct{})
	results := make(chan tools.TaskHandle, 2)
	errs := make(chan error, 2)
	for i := range 2 {
		go func() {
			<-start
			handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: fmt.Sprintf("child %d", i), AgentType: "worker"})
			results <- handle
			errs <- err
		}()
	}
	close(start)

	statuses := map[string]int{}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("CreateSubAgent: %v", err)
		}
		statuses[(<-results).Status]++
	}
	if statuses["started"] != 1 || statuses["child_limit_reached"] != 1 {
		t.Fatalf("statuses = %#v, want one started and one child_limit_reached", statuses)
	}
}

func TestCreateSubAgentRejectsBeforeLLMFactoryWhenChildLimitReached(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 1, MaxDepth: 2}
	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)

	first, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "child one", AgentType: "worker"})
	if err != nil || first.Status != "started" {
		t.Fatalf("CreateSubAgent(first) = (%#v, %v), want started", first, err)
	}
	var factoryCalls atomic.Int32
	a.llmFactory = func(string, []string, string) *llm.Client {
		factoryCalls.Add(1)
		return newTestLLMClient()
	}

	second, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "child two", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent(second): %v", err)
	}
	if second.Status != "child_limit_reached" {
		t.Fatalf("second.Status = %q, want child_limit_reached", second.Status)
	}
	if got := factoryCalls.Load(); got != 0 {
		t.Fatalf("LLM factory calls = %d, want 0 on rejected admission", got)
	}
}

func TestConcurrentDuplicateCreateSharesAdmissionResult(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var factoryCalls atomic.Int32
	a.llmFactory = func(string, []string, string) *llm.Client {
		factoryCalls.Add(1)
		entered <- struct{}{}
		<-release
		return newTestLLMClient()
	}

	results := make(chan tools.TaskHandle, 2)
	errs := make(chan error, 2)
	create := func() {
		handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "same task", AgentType: "worker", PlanTaskRef: "plan-1", SemanticTaskKey: "semantic-1"})
		results <- handle
		errs <- err
	}
	go create()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first admission did not reach LLM factory")
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		create()
	}()
	<-secondStarted
	// Give the second admission a bounded chance to (incorrectly) reach the
	// LLM factory while the first is blocked: poll with a timeout instead of
	// a single fixed sleep so slow machines still observe the overlap and a
	// duplicate factory call fails fast. The window only has to cover one
	// scheduling delay of an already-started goroutine (secondStarted above),
	// and the correct path pays it in full, so keep it short.
	admissionDeadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(admissionDeadline) {
		if got := factoryCalls.Load(); got != 1 {
			t.Fatalf("LLM factory calls before release = %d, want 1", got)
		}
		time.Sleep(time.Millisecond)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("LLM factory calls before release = %d, want 1", got)
	}
	close(release)

	var handles []tools.TaskHandle
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("CreateSubAgent: %v", err)
		}
		handles = append(handles, <-results)
	}
	if handles[0].Status != "started" || handles[1].Status != "started" || handles[0].TaskID != handles[1].TaskID || handles[0].AgentID != handles[1].AgentID {
		t.Fatalf("shared admission handles = %#v, want identical started result", handles)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("LLM factory calls = %d, want 1", got)
	}
}

func TestCreateSubAgentPersistenceFailureDoesNotStartRuntime(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(blockedRoot): %v", err)
	}
	a.sessionDir = blockedRoot

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "must persist", AgentType: "worker", PlanTaskRef: "plan-persist", SemanticTaskKey: "semantic-persist"})
	if err == nil || !strings.Contains(err.Error(), "persist initial durable task registration") {
		t.Fatalf("CreateSubAgent() = (%#v, %v), want persistence failure", handle, err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 0 {
		t.Fatalf("live SubAgents = %d, want 0 after failed registration", got)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("semaphore use = %d, want 0 after failed registration", got)
	}
	if got := len(a.subs.admissions); got != 0 {
		t.Fatalf("pending admissions = %d, want 0 after failed registration", got)
	}
	if rec := a.taskRecordByTaskID("adhoc-1"); rec != nil {
		t.Fatalf("task record = %#v, want nil after failed registration", rec)
	}
}

func TestCreateSubAgentCancellationDuringPersistenceDoesNotStartRuntime(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	entered := make(chan struct{})
	release := make(chan struct{})
	a.taskRegistryPersistHook = func() {
		close(entered)
		<-release
	}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		handle tools.TaskHandle
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "cancel during persistence", AgentType: "worker", PlanTaskRef: "plan-cancel", SemanticTaskKey: "semantic-cancel"})
		result <- outcome{handle: handle, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("admission did not reach durable persistence")
	}
	cancel()
	close(release)
	got := <-result
	if got.err == nil || !strings.Contains(got.err.Error(), context.Canceled.Error()) {
		t.Fatalf("CreateSubAgent() = (%#v, %v), want cancellation", got.handle, got.err)
	}
	if live := len(a.subs.snapshotSubAgents()); live != 0 {
		t.Fatalf("live SubAgents = %d, want 0 after cancellation", live)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("semaphore use = %d, want 0 after cancellation", got)
	}
	if got := len(a.subs.admissions); got != 0 {
		t.Fatalf("pending admissions = %d, want 0 after cancellation", got)
	}
	if rec := a.taskRecordByTaskID("adhoc-1"); rec != nil {
		t.Fatalf("task record = %#v, want nil after cancellation", rec)
	}
}

func TestSubAgentRegistrationMergesWithLatestTaskRegistry(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	sub := newControllableTestSubAgent(t, a, "new-task")
	registration := buildTaskRecordFromSub(sub, nil, "", 0, time.Now())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"existing": {TaskID: "existing", State: string(SubAgentStateCompleted)},
	})
	if err := a.persistSubAgentRegistration(a.sessionDir, sub, registration); err != nil {
		t.Fatalf("persistSubAgentRegistration: %v", err)
	}
	records, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatalf("loadDurableTaskRecords: %v", err)
	}
	if got := records["existing"]; got == nil || got.State != string(SubAgentStateCompleted) {
		t.Fatalf("existing task = %#v, want latest completed state", got)
	}
	if got := records[sub.taskID]; got == nil || got.LatestInstanceID != sub.instanceID {
		t.Fatalf("registered task = %#v, want worker registration", got)
	}
}

func TestCreateSubAgentInitializesConcurrentlyBeforeAdmission(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	a.llmFactory = func(string, []string, string) *llm.Client {
		entered <- struct{}{}
		<-release
		return newTestLLMClient()
	}

	results := make(chan tools.TaskHandle, 2)
	errs := make(chan error, 2)
	for i := range 2 {
		go func() {
			handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: fmt.Sprintf("child %d", i), AgentType: "worker", ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{fmt.Sprintf("module-%d", i)}}})
			results <- handle
			errs <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("SubAgent initialization was serialized by admission locking")
		}
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("CreateSubAgent: %v", err)
		}
		if handle := <-results; handle.Status != "started" {
			t.Fatalf("handle.Status = %q, want started", handle.Status)
		}
	}
}

func TestCreateSubAgentDoesNotHoldAdmissionLockDuringReliableOutput(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	for len(a.outputCh) < cap(a.outputCh) {
		a.outputCh <- InfoEvent{Message: "fill"}
	}

	createDone := make(chan error, 1)
	go func() {
		_, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "work", AgentType: "worker"})
		createDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(a.subs.snapshotSubAgents()) == 0 {
		time.Sleep(time.Millisecond)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 1 {
		t.Fatalf("live SubAgents = %d, want registered worker", got)
	}

	acquired := make(chan struct{})
	go func() {
		a.admissionMu.Lock()
		close(acquired)
		a.admissionMu.Unlock()
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("admission lock remained held while reliable output was blocked")
	}

	deadline = time.Now().Add(time.Second)
	for {
		select {
		case err := <-createDone:
			if err != nil {
				t.Fatalf("CreateSubAgent: %v", err)
			}
			a.cancelActiveWork()
			return
		case <-a.outputCh:
		case <-time.After(time.Until(deadline)):
			t.Fatal("CreateSubAgent remained blocked while output was drained")
		}
	}
}

func TestCreateSubAgentRejectsRegistrationAfterSessionSwitch(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	entered := make(chan struct{})
	release := make(chan struct{})
	a.llmFactory = func(string, []string, string) *llm.Client {
		close(entered)
		<-release
		return newTestLLMClient()
	}

	result := make(chan error, 1)
	go func() {
		_, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "work", AgentType: "worker"})
		result <- err
	}()
	<-entered
	a.prepareSessionSwitch()
	close(release)

	if err := <-result; err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("CreateSubAgent error = %v, want lifecycle invalidation", err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 0 {
		t.Fatalf("live SubAgents = %d, want 0 after session switch", got)
	}
}

func TestCreateSubAgentRejectsWhileSessionTransitionIsPaused(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	a.prepareSessionSwitch()

	if _, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "work", AgentType: "worker"}); err == nil || !strings.Contains(err.Error(), "session transition") {
		t.Fatalf("CreateSubAgent error = %v, want transition rejection", err)
	}
	a.finishSessionSwitch()
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "work", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent after transition: %v", err)
	}
	if handle.Status != "started" {
		t.Fatalf("handle.Status = %q, want started", handle.Status)
	}
}

func TestCreateSubAgentRejectsRegistrationAfterOwnerCompletes(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
	entered := make(chan struct{})
	release := make(chan struct{})
	a.llmFactory = func(string, []string, string) *llm.Client {
		close(entered)
		<-release
		return newTestLLMClient()
	}

	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)
	result := make(chan error, 1)
	go func() {
		_, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "child work", AgentType: "worker"})
		result <- err
	}()
	<-entered
	parent.setState(SubAgentStateCompleted, "done")
	close(release)

	if err := <-result; err == nil || !strings.Contains(err.Error(), "owner task is no longer active") {
		t.Fatalf("CreateSubAgent error = %v, want inactive owner rejection", err)
	}
	if child := a.subAgentByTaskID("adhoc-1"); child != nil {
		t.Fatalf("unexpected child registered after owner completion: %s", child.instanceID)
	}
	if got := len(a.subs.admissions); got != 0 {
		t.Fatalf("pending admissions = %d, want 0 after owner completion", got)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("semaphore use = %d, want 0 after owner completion", got)
	}
}

func TestCreateSubAgentCapsActiveChildrenAtTen(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	// MaxChildren is honoured as configured rather than silently clamped to the
	// default. Set it to 10 so the cap fires at ten, and give each child a
	// distinct description so the semantic-key duplicate guard does not flag
	// the fan-out as a re-delegation of the same deliverable.
	parent.delegation = config.DelegationConfig{MaxChildren: 10, MaxDepth: 2}

	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)
	for i := range 10 {
		handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: fmt.Sprintf("child work %d", i), AgentType: "worker", ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{fmt.Sprintf("module-%d", i)}}})
		if err != nil {
			t.Fatalf("CreateSubAgent(%d): %v", i, err)
		}
		if handle.Status != "started" {
			t.Fatalf("handle.Status[%d] = %q, want started", i, handle.Status)
		}
	}

	overflow, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "child overflow", AgentType: "worker", ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"overflow"}}})
	if err != nil {
		t.Fatalf("CreateSubAgent(overflow): %v", err)
	}
	if overflow.Status != "child_limit_reached" {
		t.Fatalf("overflow.Status = %q, want child_limit_reached", overflow.Status)
	}
}

func TestCreateSubAgentUsesCachedWorkDir(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 1)
	wantWorkDir := filepath.Join(a.contentRoot, "workspace")
	setCachedWorkDirForTest(a, wantWorkDir)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "use stable workspace", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	sub := a.subAgentByTaskID(handle.TaskID)
	if sub == nil {
		t.Fatal("expected live SubAgent")
	}
	if sub.workDir != wantWorkDir {
		t.Fatalf("created workDir = %q, want %q", sub.workDir, wantWorkDir)
	}
}

func TestCreateSubAgentSnapshotPersistFailureDoesNotLeakSlot(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	installSnapshotBlockingRecoveryManager(t, a)

	// The snapshot write after the commit must be non-fatal: the registration
	// (task registry + instance meta) is already durable and the next
	// saveRecoverySnapshot rewrites the snapshot from live state. Failing the
	// creation here was the historical path that leaked the runtime slot.
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "snapshot write fails", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if handle.Status != "started" {
		t.Fatalf("handle.Status = %q, want started despite snapshot failure", handle.Status)
	}
	sub := a.subAgentByTaskID(handle.TaskID)
	if sub == nil {
		t.Fatal("expected the published SubAgent to stay live after a non-fatal snapshot failure")
	}
	if got := len(a.subs.snapshotSubAgents()); got != 1 {
		t.Fatalf("live SubAgents = %d, want 1", got)
	}
	snap := a.runtimeGovernorSnapshot()
	if snap.RuntimeInUse != 1 || snap.RuntimeHolders != 1 || snap.RuntimeSlotDrift != 0 {
		t.Fatalf("governor after publish = in_use:%d holders:%d drift:%d, want 1/1/0", snap.RuntimeInUse, snap.RuntimeHolders, snap.RuntimeSlotDrift)
	}
	if rec := a.taskRecordByTaskID(handle.TaskID); rec == nil || rec.LatestInstanceID != sub.instanceID {
		t.Fatalf("task record after publish = %#v, want the live instance", rec)
	}

	a.closeSubAgent(sub.instanceID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sub.waitDone(ctx); err != nil {
		t.Fatalf("wait for SubAgent shutdown: %v", err)
	}
	snap = a.runtimeGovernorSnapshot()
	if snap.RuntimeInUse != 0 || snap.RuntimeHolders != 0 || snap.RuntimeSlotDrift != 0 {
		t.Fatalf("governor after close = in_use:%d holders:%d drift:%d, want 0/0/0", snap.RuntimeInUse, snap.RuntimeHolders, snap.RuntimeSlotDrift)
	}
}

func TestCreateSubAgentMCPServerFailureReleasesAdmissionSlot(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	// Agent-scoped MCP servers cannot be enabled at runtime, so a manual entry
	// makes getOrCreateAgentMCP fail after the admission already holds a slot.
	a.agentConfigs["worker"].MCP = config.MCPConfig{"runtime-only": {Manual: true}}

	_, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "mcp must fail", AgentType: "worker"})
	if err == nil || !strings.Contains(err.Error(), "manual") {
		t.Fatalf("CreateSubAgent() err = %v, want agent-scoped MCP manual rejection", err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 0 {
		t.Fatalf("live SubAgents = %d, want 0 after MCP failure", got)
	}
	if got := len(a.subs.admissions); got != 0 {
		t.Fatalf("pending admissions = %d, want 0 after MCP failure", got)
	}
	if got := a.governor.snapshot().RuntimeInUse; got != 0 {
		t.Fatalf("runtime in use = %d, want 0 after MCP failure", got)
	}
}

func TestCreateSubAgentRejectsUnusableModelPoolAndReleasesAdmission(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 1)
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return nil })
	_, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "sample task", AgentType: "worker"})
	if err == nil || !strings.Contains(err.Error(), "model pool") {
		t.Fatalf("error = %v", err)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("semaphore slots = %d, want 0", got)
	}
}
