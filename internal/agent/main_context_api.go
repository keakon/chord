package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

// GetUsageStats returns session-wide usage statistics (all agents in the session).
func (a *MainAgent) GetUsageStats() analytics.SessionStats {
	if a.usageTracker == nil {
		return analytics.SessionStats{}
	}
	return a.usageTracker.SessionStats()
}

// GetSidebarUsageStats returns usage for the TUI-focused agent only (main or SubAgent),
// matching GetContextStats / GetTokenUsage routing.
func (a *MainAgent) GetSidebarUsageStats() analytics.SessionStats {
	if a.usageTracker == nil {
		return analytics.SessionStats{}
	}
	if sub := a.validFocusedSubAgent(); sub != nil {
		return a.usageTracker.SessionStatsForAgent(sub.instanceID)
	}
	return a.usageTracker.SessionStatsForAgent("main")
}

// GetContextStats returns current context usage and limit for the focused agent.
// Current = input + output + cache + reasoning from last API response (all count toward context).
func (a *MainAgent) GetContextStats() (current, limit int) {
	if sub := a.validFocusedSubAgent(); sub != nil {
		return sub.GetContextStats()
	}
	return a.ctxMgr.LastTotalContextTokens(), a.ctxMgr.GetMaxTokens()
}

// GetContextMessageCount returns the number of messages in the focused agent's context (for sidebar).
func (a *MainAgent) GetContextMessageCount() int {
	if sub := a.validFocusedSubAgent(); sub != nil {
		return sub.GetContextMessageCount()
	}
	return a.ctxMgr.MessageCount()
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

func (a *MainAgent) mainBackgroundResultContent(payload *tools.SpawnFinishedPayload) string {
	if payload == nil {
		return ""
	}
	content := strings.TrimSpace(payload.Message)
	if content != "" {
		return content
	}
	kind := strings.TrimSpace(payload.Kind)
	if kind == "" {
		kind = "job"
	}
	desc := strings.TrimSpace(payload.Description)
	if desc == "" {
		desc = payload.Command
	}
	return fmt.Sprintf("[Background %s %s completed]\n\nDescription: %s\nStatus: %s\nReview this result before continuing.", kind, payload.EffectiveID(), desc, payload.Status)
}

func (a *MainAgent) handleSpawnResultForMain(payload *tools.SpawnFinishedPayload) {
	if payload == nil {
		return
	}
	content := a.mainBackgroundResultContent(payload)
	msg := message.Message{Role: "user", Content: content}
	a.ctxMgr.Append(msg)
	a.recordEvidenceFromMessage(msg)
	if a.recovery != nil {
		a.persistAsync("main", msg)
	}
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

// UpdateTodos replaces the todo list and saves a snapshot via the recovery
// manager. It implements the tools.TodoStore interface.
func (a *MainAgent) UpdateTodos(todos []tools.TodoItem) error {
	a.todoMu.Lock()
	a.todoItems = make([]tools.TodoItem, len(todos))
	copy(a.todoItems, todos)

	if a.recovery != nil && !a.shuttingDown.Load() {
		todoStates := snapshotTodos(todos)
		usageSnap := a.usageTracker.SessionStats()
		if err := a.recovery.SaveSnapshot(&recovery.SessionSnapshot{
			Todos:                  todoStates,
			ModelName:              a.ModelName(),
			ActiveRole:             a.CurrentRole(),
			LastInputTokens:        a.ctxMgr.LastInputTokens(),
			LastTotalContextTokens: a.ctxMgr.LastTotalContextTokens(),
			UsageInputTokens:       usageSnap.InputTokens,
			UsageOutputTokens:      usageSnap.OutputTokens,
			UsageCacheReadTokens:   usageSnap.CacheReadTokens,
			UsageCacheWriteTokens:  usageSnap.CacheWriteTokens,
			UsageReasoningTokens:   usageSnap.ReasoningTokens,
			UsageLLMCalls:          usageSnap.LLMCalls,
			UsageEstimatedCost:     usageSnap.EstimatedCost,
			UsageByModel:           usageSnap.ByModel,
			UsageByAgent:           usageSnap.ByAgent,
		}); err != nil {
			log.Warnf("failed to save todo snapshot error=%v", err)
		}
	}
	a.todoMu.Unlock()

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
	case "agent_idle":
		mapped = EventAgentIdle
	case "agent_log":
		mapped = EventAgentLog
	case "reset_nudge":
		mapped = EventResetNudge
	case "background_object_finished":
		mapped = EventSpawnFinished
	}

	a.sendEvent(Event{
		Type:     mapped,
		SourceID: sourceID,
		Payload:  payload,
	})
}
