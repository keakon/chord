package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
)

// Task usage report row kinds. Main and untracked calls have no durable task
// identity, so they use dedicated kinds instead of occupying a real task key.
const (
	TaskUsageKindMain      = "main"
	TaskUsageKindTask      = "task"
	TaskUsageKindUntracked = "untracked"
)

// TaskUsageRow is one line of the Tasks view. Ledger is Chord's own estimated
// usage, Reported is the external CLI's cumulative report, and a nil pointer
// means that source made no observation.
type TaskUsageRow struct {
	TaskID       string
	Kind         string
	AgentDefName string
	TaskDesc     string
	Backend      string
	Ledger       *analytics.AgentStats
	Reported     *AgentUsage
}

// TaskUsageReport is one query's ledger snapshot plus the task rows it can be
// attributed to. Total covers Chord's ledger only; external reports are not
// part of that total.
type TaskUsageReport struct {
	Total analytics.AgentStats
	Rows  []TaskUsageRow
}

// TaskUsageReporter loads the session Tasks report. It is optional for TUI
// agents; callers type-assert instead of requiring it on AgentForTUI.
type TaskUsageReporter interface {
	LoadTaskUsageReport(ctx context.Context) (*TaskUsageReport, error)
}

var (
	errTaskUsageSessionChanging = errors.New("session task usage is changing")
	errTaskUsageSessionChanged  = errors.New("task usage report invalidated by a session change")
)

// liveTaskUsage is the immutable part of a live runtime the report needs.
type liveTaskUsage struct {
	instanceID   string
	taskID       string
	agentDefName string
	taskDesc     string
	backend      string
	reported     AgentUsage
}

// LoadTaskUsageReport builds the session Tasks report from one ledger
// snapshot, the current task registry, live runtimes, and the archived task
// projection. It is safe to call from a background goroutine; it never takes
// the event loop's state and performs no work while holding the tracker lock.
//
// A failed archive read returns the ledger/current-task part together with a
// wrapped error; callers must present such a report as incomplete rather than
// as a complete history.
func (a *MainAgent) LoadTaskUsageReport(ctx context.Context) (*TaskUsageReport, error) {
	if a == nil || a.usageTracker == nil {
		return nil, fmt.Errorf("task usage is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner := a.currentRecoveryOwnership()
	if owner == nil {
		return nil, fmt.Errorf("task usage is unavailable while the session has no storage")
	}
	stats := a.usageTracker.SessionStats()

	marker, records, runtimes, captureErr := a.captureTaskUsageSnapshot(ctx, owner)
	if captureErr != nil {
		if errors.Is(captureErr, errTaskUsageSessionChanging) {
			return nil, captureErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return buildTaskUsageReport(stats, records, snapshotLiveTaskUsage(runtimes), nil), captureErr
	}

	var archiveRows map[string]taskUsageArchiveRow
	var reportErr error
	if cached, ok := a.taskUsageCache.lookup(marker); ok {
		archiveRows = cached
	} else {
		scanned, err := scanTaskUsageArchive(ctx, owner.dir, marker)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			reportErr = err
			archiveRows = scanned
		} else {
			archiveRows = scanned
			a.taskUsageCache.publish(marker, scanned)
		}
	}

	report := buildTaskUsageReport(stats, records, snapshotLiveTaskUsage(runtimes), archiveRows)
	if current := a.currentRecoveryOwnership(); current == nil || current.epoch != owner.epoch || current.dir != owner.dir {
		return nil, errTaskUsageSessionChanged
	}
	return report, reportErr
}

// captureTaskUsageSnapshot pins the current records, live runtime references,
// and archive file length to one instant. The whole capture runs under the
// archive writer's lock, so a task moving from the registry map into the
// archive cannot fall between the two sources; decoding happens after release.
func (a *MainAgent) captureTaskUsageSnapshot(ctx context.Context, owner *recoveryOwnership) (taskUsageArchiveMarker, map[string]*DurableTaskRecord, []subAgentRuntime, error) {
	if owner == nil {
		return taskUsageArchiveMarker{}, nil, nil, errTaskUsageSessionChanging
	}
	marker := taskUsageArchiveMarker{epoch: owner.epoch, sessionDir: owner.dir}
	if err := ctx.Err(); err != nil {
		return marker, nil, nil, err
	}

	a.taskRegistryPersistMu.Lock()
	a.subs.mu.RLock()
	if owner.epoch != a.subs.sessionEpoch {
		a.subs.mu.RUnlock()
		a.taskRegistryPersistMu.Unlock()
		return marker, nil, nil, errTaskUsageSessionChanging
	}
	records := cloneDurableTaskRecordMap(a.subs.taskRecords)
	runtimes := make([]subAgentRuntime, 0, len(a.subs.subAgents))
	for _, runtime := range a.subs.subAgents {
		if runtime != nil {
			runtimes = append(runtimes, runtime)
		}
	}
	var info os.FileInfo
	var statErr error
	if path := durableTaskArchivePath(owner.dir); path != "" {
		info, statErr = os.Stat(path)
	}
	a.subs.mu.RUnlock()
	a.taskRegistryPersistMu.Unlock()

	if statErr != nil {
		if os.IsNotExist(statErr) {
			return marker, records, runtimes, nil
		}
		return marker, records, runtimes, fmt.Errorf("stat task archive: %w", statErr)
	}
	if info != nil {
		marker.exists = true
		marker.size = info.Size()
		marker.modTime = info.ModTime()
	}
	return marker, records, runtimes, nil
}

// snapshotLiveTaskUsage reads the thread-safe parts of each live runtime after
// the registry locks are released. Native usage snapshots are deliberately not
// read: Chord's ledger is the only source for native billing.
func snapshotLiveTaskUsage(runtimes []subAgentRuntime) []liveTaskUsage {
	out := make([]liveTaskUsage, 0, len(runtimes))
	for _, runtime := range runtimes {
		entry := liveTaskUsage{
			instanceID:   strings.TrimSpace(runtime.InstanceID()),
			taskID:       strings.TrimSpace(runtime.TaskID()),
			agentDefName: strings.TrimSpace(runtime.AgentDefName()),
			taskDesc:     strings.TrimSpace(runtime.TaskDescription()),
		}
		if entry.taskID == "" {
			continue
		}
		if worker, ok := runtime.(*externalWorker); ok {
			entry.backend = config.BackendKindClaudeCode
			entry.reported = worker.UsageSnapshot()
		}
		out = append(out, entry)
	}
	return out
}

type taskUsageAccumulator struct {
	taskID   string
	ledger   *analytics.AgentStats
	current  *DurableTaskRecord
	archived *taskUsageArchiveRow
	live     []liveTaskUsage
	active   bool
}

func buildTaskUsageReport(stats analytics.SessionStats, records map[string]*DurableTaskRecord, live []liveTaskUsage, archived map[string]taskUsageArchiveRow) *TaskUsageReport {
	report := &TaskUsageReport{Total: sessionTotalAgentStats(stats)}
	if as := stats.ByAgent[identity.MainAgentID]; as != nil && as.LLMCalls > 0 {
		report.Rows = append(report.Rows, TaskUsageRow{Kind: TaskUsageKindMain, Ledger: as})
	}
	if stats.Untracked != nil && stats.Untracked.LLMCalls > 0 {
		report.Rows = append(report.Rows, TaskUsageRow{Kind: TaskUsageKindUntracked, Ledger: stats.Untracked})
	}

	accumulators := make(map[string]*taskUsageAccumulator)
	accumulate := func(taskID string) *taskUsageAccumulator {
		taskID = strings.TrimSpace(taskID)
		if taskID == "" {
			return nil
		}
		acc := accumulators[taskID]
		if acc == nil {
			acc = &taskUsageAccumulator{taskID: taskID}
			accumulators[taskID] = acc
		}
		return acc
	}
	for taskID, as := range stats.ByTask {
		if as == nil {
			continue
		}
		accumulate(taskID).ledger = as
	}
	for taskID, rec := range records {
		if rec == nil {
			continue
		}
		acc := accumulate(taskID)
		acc.current = rec
		if isNonTerminalTaskState(rec.State) {
			acc.active = true
		}
	}
	for taskID, row := range archived {
		archivedRow := row
		accumulate(taskID).archived = &archivedRow
	}
	for _, entry := range live {
		acc := accumulate(entry.taskID)
		acc.live = append(acc.live, entry)
		acc.active = true
	}

	ordered := make([]*taskUsageAccumulator, 0, len(accumulators))
	for _, acc := range accumulators {
		ordered = append(ordered, acc)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].taskID < ordered[j].taskID })
	for _, acc := range ordered {
		if row, ok := acc.reportRow(); ok {
			report.Rows = append(report.Rows, row)
		}
	}
	return report
}

func (acc *taskUsageAccumulator) reportRow() (TaskUsageRow, bool) {
	row := TaskUsageRow{TaskID: acc.taskID, Kind: TaskUsageKindTask, Ledger: acc.ledger}
	if acc.archived != nil {
		row.AgentDefName = acc.archived.AgentDefName
		row.TaskDesc = acc.archived.TaskDesc
		row.Backend = acc.archived.Backend
	}
	if acc.current != nil {
		if v := strings.TrimSpace(acc.current.AgentDefName); v != "" {
			row.AgentDefName = v
		}
		if v := strings.TrimSpace(acc.current.TaskDesc); v != "" {
			row.TaskDesc = v
		}
		if acc.current.ExternalSession != nil {
			row.Backend = strings.TrimSpace(acc.current.ExternalSession.Backend)
			if row.Backend == "" {
				row.Backend = config.BackendKindClaudeCode
			}
		}
	}
	live := acc.pickLive()
	if live != nil {
		if live.agentDefName != "" {
			row.AgentDefName = live.agentDefName
		}
		if live.taskDesc != "" {
			row.TaskDesc = live.taskDesc
		}
		if live.backend != "" {
			row.Backend = live.backend
		}
	}
	row.Reported = acc.pickReported(live)
	if row.Ledger == nil && row.Reported == nil && !acc.active {
		return TaskUsageRow{}, false
	}
	return row, true
}

// pickLive favors the runtime the durable record names as current, then the
// newest instance ID. Live entries are never combined: the external CLI's
// report is cumulative, so summing snapshots would double count.
func (acc *taskUsageAccumulator) pickLive() *liveTaskUsage {
	if len(acc.live) == 0 {
		return nil
	}
	latest := ""
	if acc.current != nil {
		latest = strings.TrimSpace(acc.current.LatestInstanceID)
	}
	if latest != "" {
		for i := range acc.live {
			if acc.live[i].instanceID == latest {
				return &acc.live[i]
			}
		}
	}
	best := 0
	for i := 1; i < len(acc.live); i++ {
		if acc.live[i].instanceID > acc.live[best].instanceID {
			best = i
		}
	}
	return &acc.live[best]
}

// pickReported selects one external cumulative snapshot: live, then the
// current record, then the newest archived record. A zeroed snapshot is not an
// observation, so the next source is used; a known zero cost is an observation
// and is kept.
func (acc *taskUsageAccumulator) pickReported(live *liveTaskUsage) *AgentUsage {
	external := false
	if live != nil && live.backend != "" {
		external = true
	}
	if acc.current != nil && acc.current.ExternalSession != nil {
		external = true
	}
	if acc.archived != nil && acc.archived.Backend != "" {
		external = true
	}
	if !external {
		return nil
	}
	if live != nil && live.backend != "" && !live.reported.Zero() {
		usage := live.reported
		return &usage
	}
	if acc.current != nil && acc.current.ExternalSession != nil && acc.current.Usage != nil && !acc.current.Usage.Zero() {
		usage := *acc.current.Usage
		return &usage
	}
	if acc.archived != nil && acc.archived.Reported != nil {
		usage := *acc.archived.Reported
		return &usage
	}
	return nil
}

func sessionTotalAgentStats(stats analytics.SessionStats) analytics.AgentStats {
	return analytics.AgentStats{
		InputTokens:      stats.InputTokens,
		OutputTokens:     stats.OutputTokens,
		CacheReadTokens:  stats.CacheReadTokens,
		CacheWriteTokens: stats.CacheWriteTokens,
		ReasoningTokens:  stats.ReasoningTokens,
		LLMCalls:         stats.LLMCalls,
		EstimatedCost:    stats.EstimatedCost,
	}
}
