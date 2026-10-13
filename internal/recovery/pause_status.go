package recovery

import "time"

const (
	SessionPausePreflight            = "preflight"
	SessionPausePreparing            = "preparing"
	SessionPausePrepareFailed        = "prepare_failed"
	SessionPauseDraining             = "draining"
	SessionPauseSaving               = "saving"
	SessionPauseSaved                = "saved"
	SessionPauseReleased             = "released"
	SessionPauseSaveFailed           = "save_failed"
	SessionPauseReady                = "ready_paused"
	SessionPauseActionRetrySave      = "retry-save"
	SessionPauseActionManualRecovery = "manual-recovery"
)

type SessionPauseBlocker struct {
	OwnerTaskID string    `json:"owner_task_id"`
	Kind        string    `json:"kind"`
	StartedAt   time.Time `json:"started_at"`
	Description string    `json:"description"`
}

type SessionPauseStatus struct {
	TransferIdentity
	State             string                `json:"state"`
	StartedAt         time.Time             `json:"started_at"`
	WaitExpired       bool                  `json:"wait_expired,omitempty"`
	Blockers          []SessionPauseBlocker `json:"blockers,omitempty"`
	BlockerCount      int                   `json:"blocker_count"`
	PendingInputCount int                   `json:"pending_input_count"`
	CheckpointID      string                `json:"checkpoint_id,omitempty"`
	ErrorCode         string                `json:"error_code,omitempty"`
	Error             string                `json:"error,omitempty"`
	RecoverableAction string                `json:"recoverable_action,omitempty"`
}

type SessionPauseError struct {
	Code  string
	Cause error
}

func (e *SessionPauseError) Error() string { return e.Code + ": " + e.Cause.Error() }
func (e *SessionPauseError) Unwrap() error { return e.Cause }
