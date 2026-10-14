package main

import (
	"path/filepath"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// headlessBackend is the subset of MainAgent functionality required by headless mode.
type headlessBackend interface {
	SendUserMessageWithReceipt(content, requestID string) bool
	CancelCurrentTurn() bool
	ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string)
}

type headlessWorkDirBackend interface {
	WorkDirSnapshot() agent.WorkDirSnapshot
}

func headlessWorkDirSnapshot(backend headlessBackend) agent.WorkDirSnapshot {
	if backend == nil {
		return agent.WorkDirSnapshot{}
	}
	if provider, ok := backend.(headlessWorkDirBackend); ok {
		return provider.WorkDirSnapshot()
	}
	return agent.WorkDirSnapshot{}
}

func headlessWorkDirPayload(snapshot agent.WorkDirSnapshot) map[string]any {
	return map[string]any{
		"path":        snapshot.Path,
		"worktree_id": snapshot.WorktreeID,
		"generation":  snapshot.Generation,
	}
}

type headlessHandoffBackend interface {
	HandoffAgentOptions() []agent.HandoffAgentOption
	SetAgentModelPool(agentName, pool string) error
	// ResolveHandoff delivers the user's decision (approve/deny/cancel) for the
	// pending handoff. The runtime settles the user-wait, emits the deferred
	// handoff tool result, and continues the flow (execute plan / reject /
	// cancel).
	ResolveHandoff(requestID, action, agentName, denyReason string)
}

type headlessModelsBackend interface {
	ModelsStatusText() string
	SetCurrentModelPool(pool string) error
}

type headlessBackendWithRuleIntent interface {
	ResolveConfirmWithRuleIntent(action, finalArgsJSON, editSummary, denyReason, requestID string, ruleIntent *agent.ConfirmRuleIntent)
}

// headlessSessionBackend exposes the backend's committed session directory so
// session-switch pushes report the session the runtime actually runs, not a
// stale startup snapshot. MainAgent satisfies it.
type headlessSessionBackend interface {
	SessionDir() string
}

// headlessBackendSessionID resolves the active session id (directory base
// name, matching the ready/status_response convention), or "" when the backend
// cannot report one.
func headlessBackendSessionID(backend headlessBackend) string {
	sb, ok := backend.(headlessSessionBackend)
	if !ok {
		return ""
	}
	if dir := sb.SessionDir(); dir != "" {
		return filepath.Base(dir)
	}
	return ""
}

// headlessRunningJobs snapshots the active session's jobs that still owe the
// agent a completion notification: live jobs whose end will notify, plus
// finished jobs whose notification the agent has not consumed yet. The second
// group keeps the count above zero after a job finishes but before the queued
// JOB RESULT has been handled, so an integration cannot read a final idle and
// stop consuming events early. Idle is not held back for the live jobs
// themselves: services and watchers may keep running indefinitely. Silent
// cancellation removes a job from the next snapshot; tool-driven stops are
// reported by the active work's next idle, while session switches have their
// own explicit notification.
//
// status_response reads it live; idle carries the count the agent captured
// when it went idle (GlobalIdleEvent.RunningJobs), because a snapshot taken
// later on the output goroutine could miss a job whose notification the agent
// consumed in between.
func headlessRunningJobs(backend headlessBackend) int {
	sessionDir := ""
	if sb, ok := backend.(headlessSessionBackend); ok {
		sessionDir = sb.SessionDir()
	}
	return tools.PendingWakeJobs(sessionDir)
}
