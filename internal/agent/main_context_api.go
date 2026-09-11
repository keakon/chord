package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// GetSidebarWalltimeStats returns wall-clock time stats for the TUI-focused
// agent only (main, live SubAgent, or a parked/settled task viewed through its
// durable record), mirroring GetSidebarUsageStats routing. All buckets are zero
// when no walltime has been recorded for the focused agent.
func (a *MainAgent) GetSidebarWalltimeStats() analytics.WalltimeStats {
	if a.walltime == nil {
		return analytics.WalltimeStats{}
	}
	target := a.focusedAgentSnapshot()
	if target.sub != nil {
		return a.walltime.statsForAgent(target.sub.instanceID)
	}
	if target.parked || target.settled {
		return a.walltime.statsForTask(target.task)
	}
	return a.walltime.statsForAgent(identity.MainAgentID)
}

// GetUsageStats returns session-wide usage statistics (all agents in the session).
func (a *MainAgent) GetUsageStats() analytics.SessionStats {
	if a.usageTracker == nil {
		return analytics.SessionStats{}
	}
	return a.usageTracker.SessionStats()
}

// GetSidebarUsageStats returns usage for the TUI-focused agent only (main or
// SubAgent), matching GetContextStats / GetTokenUsage routing.
//
// A focused worker is billed per task, not per runtime instance: a task resumed
// for a second attempt runs under a new instance ID, and reporting only that
// instance would erase the earlier attempts from the panel while the worker is
// live and bring them back the moment it parked.
func (a *MainAgent) GetSidebarUsageStats() analytics.SessionStats {
	if a.usageTracker == nil {
		return analytics.SessionStats{}
	}
	target := a.focusedAgentSnapshot()
	if target.sub == nil && target.task == nil {
		return a.usageTracker.SessionStatsForAgent(identity.MainAgentID)
	}
	live := ""
	if target.sub != nil {
		live = target.sub.instanceID
	}
	return a.usageStatsForTask(target.task, live)
}

// usageStatsForTask sums the usage of every runtime instance a task has run
// under. liveInstanceID is folded in so a worker that has not yet been written
// back to its record still reports its own usage.
func (a *MainAgent) usageStatsForTask(rec *DurableTaskRecord, liveInstanceID string) analytics.SessionStats {
	out := analytics.SessionStats{ByModel: make(map[string]*analytics.ModelStats), ByAgent: make(map[string]*analytics.AgentStats)}
	if a.usageTracker == nil {
		return out
	}
	instances := []string(nil)
	if rec != nil {
		instances = rec.InstanceHistory
	}
	if strings.TrimSpace(liveInstanceID) != "" {
		instances = append(append([]string(nil), instances...), liveInstanceID)
	}
	for _, instanceID := range dedupeTaskInstanceHistory(instances) {
		stats := a.usageTracker.SessionStatsForAgent(instanceID)
		out.InputTokens += stats.InputTokens
		out.OutputTokens += stats.OutputTokens
		out.CacheReadTokens += stats.CacheReadTokens
		out.CacheWriteTokens += stats.CacheWriteTokens
		out.ReasoningTokens += stats.ReasoningTokens
		out.LLMCalls += stats.LLMCalls
		out.EstimatedCost += stats.EstimatedCost
		for model, modelStats := range stats.ByModel {
			if modelStats == nil {
				continue
			}
			agg := out.ByModel[model]
			if agg == nil {
				agg = &analytics.ModelStats{}
				out.ByModel[model] = agg
			}
			agg.Calls += modelStats.Calls
			agg.InputTokens += modelStats.InputTokens
			agg.OutputTokens += modelStats.OutputTokens
			agg.CacheReadTokens += modelStats.CacheReadTokens
			agg.CacheWriteTokens += modelStats.CacheWriteTokens
			agg.ReasoningTokens += modelStats.ReasoningTokens
			agg.EstimatedCost += modelStats.EstimatedCost
		}
	}
	return out
}

// GetContextStats returns the context-usage level shown for the focused agent
// and its usable input budget. current is the same effective reading the
// automatic-compaction decision compares against its threshold: the last
// post-response context baseline (full prompt including cache tokens plus
// generated output) or the calibrated estimate once the context has grown past
// it since that provider sample — so the sidebar Context value/gauge and the
// auto-compaction trigger always observe one value. limit is the usable input
// budget (the input limit minus reserved headroom). Focused SubAgents report
// the same frame from their own context manager; parked and settled targets
// have no live context manager and report zero.
func (a *MainAgent) GetContextStats() (current, limit int) {
	target := a.focusedAgentSnapshot()
	if target.sub != nil {
		return target.sub.GetContextStats()
	}
	if target.parked || target.settled {
		return 0, 0
	}
	return a.ctxMgr.EffectiveContextTokens(), a.ctxMgr.GetUsableInputBudget()
}

// ContextPressureLinesForModelRef returns the reminder and auto-compaction
// usage lines a model at modelRef would manage its context with, in the same
// frame as GetContextStats: usage at the reminder line starts context-pressure
// reminders, usage at the threshold line arms usage-driven compaction. The
// lines resolve exactly as the compaction policy sees them — the model-level
// config of the ref, then the global lines, then the derived reminder — and
// are independent of what the agent is currently running, so the TUI can
// preview the lines of the model shown as next up after a switch. Both lines
// are 0 when the ref is empty (no model selected) or automatic compaction is
// disabled (threshold 0); a model with no compaction config inherits the
// global threshold, and the reminder is derived when no explicit value is set.
// Focused SubAgents and parked targets manage their context with
// sliding-window compaction rather than usage lines, so the TUI does not query
// them through this method and keeps the fixed fallback lines.
func (a *MainAgent) ContextPressureLinesForModelRef(modelRef string) (reminder, threshold float64) {
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return 0, 0
	}
	threshold = a.effectiveCompactionThreshold(modelRef)
	reminder = a.effectiveReminderPctForModelRef(modelRef, threshold)
	return reminder, threshold
}

// GetContextMessageCount returns the number of messages in the focused agent's context (for sidebar).
func (a *MainAgent) GetContextMessageCount() int {
	target := a.focusedAgentSnapshot()
	if target.sub != nil {
		return target.sub.GetContextMessageCount()
	}
	if target.parked || target.settled {
		manager := a.recoveryManager()
		msgs, err := loadTaskHistoryMessages(manager, target.task, loadToolActivityStarted(manager))
		if err != nil {
			return 0
		}
		return len(msgs)
	}
	return a.ctxMgr.MessageCount()
}

func (a *MainAgent) GetContextBytes() int {
	target := a.focusedAgentSnapshot()
	if target.sub != nil {
		return target.sub.GetContextBytes()
	}
	if target.parked || target.settled {
		manager := a.recoveryManager()
		msgs, err := loadTaskHistoryMessages(manager, target.task, loadToolActivityStarted(manager))
		if err != nil {
			return 0
		}
		return ctxmgr.MessagePayloadBytes(msgs)
	}
	return a.ctxMgr.ContextPayloadBytes() + toolDefinitionBytes(a.mainLLMToolDefinitions())
}

func toolDefinitionBytes(defs []message.ToolDefinition) int {
	total := 0
	for _, def := range defs {
		total += len(def.Name) + len(def.Description)
	}
	return total
}

// KeyStats returns (healthy, total) API keys for the focused agent's provider
// (SubAgent when focused, else MainAgent), aligned with RunningModelRef.
// healthy = selectable AND not recovering (re-proven healthy since last failure/reset).
func (a *MainAgent) KeyStats() (confirmed, total int) {
	client, ref := a.tuiFocusedLLMAndRef()
	if client == nil {
		return 0, 0
	}
	return client.ConfirmedKeyStatsForRef(ref)
}

// KeyPoolNextTransition returns how soon the key pool sidebar line may need a
// refresh (cooldown expiry or Codex rate-limit window reset). Zero means no
// scheduled transition or single-key pool. Uses the same agent as KeyStats.
func (a *MainAgent) KeyPoolNextTransition() time.Duration {
	client, ref := a.tuiFocusedLLMAndRef()
	if client == nil {
		return 0
	}
	return client.KeyPoolNextTransitionForRef(ref)
}

func (a *MainAgent) mainBackgroundResultContent(payload *tools.JobFinishedPayload) string {
	if payload == nil {
		return ""
	}
	content := strings.TrimSpace(payload.Message)
	if content != "" {
		return content
	}
	desc := strings.TrimSpace(payload.Description)
	if desc == "" {
		desc = payload.Command
	}
	return fmt.Sprintf("[Background job %s completed]\n\nDescription: %s\nStatus: %s", payload.EffectiveID(), desc, payload.Status)
}

// SetSessionArtifactsDirFunc installs a callback that returns the active
// session artifacts directory. When unset, exports fall back to the historical
// project-level path.
func (a *MainAgent) SetSessionArtifactsDirFunc(fn func() string) {
	a.sessionArtifactsDirFn = fn
}

// SetSessionTargetChangedFunc installs a callback invoked after the active
// session directory changes.
func (a *MainAgent) SetSessionTargetChangedFunc(fn func(string)) {
	a.sessionTargetChangedFn = fn
}

func (a *MainAgent) sessionArtifactsDir() string {
	if a.sessionArtifactsDirFn != nil {
		if dir := strings.TrimSpace(a.sessionArtifactsDirFn()); dir != "" {
			return dir
		}
	}
	if strings.TrimSpace(a.sessionDir) == "" {
		locator, err := config.DefaultPathLocator()
		if err == nil {
			if pl, err := locator.LocateProject(a.projectRoot); err == nil {
				return pl.ProjectExportsDir
			}
		}
		return ""
	}
	return filepath.Join(a.sessionDir, "artifacts")
}

// AllowMultipleInProgressTodos reports whether the current role can use
// TodoWrite to track multiple distinct workstreams as simultaneously active.
func (a *MainAgent) AllowMultipleInProgressTodos() bool {
	return a.hasTodoWriteAccess()
}

// UpdateTodos replaces the todo list and saves a snapshot via the recovery
// manager. It implements the tools.TodoStore interface.
func (a *MainAgent) UpdateTodos(todos []tools.TodoItem) error {
	a.todoMu.Lock()
	a.todoItems = make([]tools.TodoItem, len(todos))
	copy(a.todoItems, todos)
	a.todoMu.Unlock()

	a.saveRecoverySnapshot()

	todoCopy := make([]tools.TodoItem, len(todos))
	copy(todoCopy, todos)
	a.emitToTUI(TodosUpdatedEvent{Todos: todoCopy})

	return nil
}

// GetTodos returns a copy of the current todo list. It implements the
// tools.TodoStore interface.
func (a *MainAgent) GetTodos() []tools.TodoItem {
	a.todoMu.RLock()
	defer a.todoMu.RUnlock()
	out := make([]tools.TodoItem, len(a.todoItems))
	copy(out, a.todoItems)
	return out
}

// SendAgentEvent maps tool event type strings to internal event constants and
// forwards the event through the event bus. It implements the
// tools.EventSender interface.
func (a *MainAgent) SendAgentEvent(eventType, sourceID string, payload any) {
	mapped := eventType
	switch eventType {
	case "escalate":
		mapped = EventEscalate
	case "agent_notify":
		mapped = EventAgentNotify
	case "agent_done":
		mapped = EventAgentDone
	case "agent_log":
		mapped = EventAgentLog
	case "background_object_finished":
		mapped = EventJobFinished
	}

	a.sendEvent(Event{
		Type:     mapped,
		SourceID: sourceID,
		Payload:  payload,
	})
}
