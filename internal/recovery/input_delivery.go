package recovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/privatefs"
)

type InputDeliveryPolicy string

const (
	InputDeliveryNextRequest     InputDeliveryPolicy = "next_request"
	InputDeliveryAfterCompletion InputDeliveryPolicy = "after_completion"
	PendingInputStageClaimed                         = "claimed"
	PendingInputStageStarted                         = "started"
	InputDeliveryFile                                = "input-delivery.json"
	InputDeliveryMaxInputs                           = 128
	InputDeliveryMaxTextBytes                        = 4 << 20
	DeliveryWorkUser                                 = "user"
	DeliveryWorkFollowup                             = "followup"
	DeliveryWorkContext                              = "context"
	DeliveryWorkActive                               = "active"
	DeliveryWorkCompleted                            = "completed"
	DeliveryWorkFailed                               = "failed"
	DeliveryWorkCancelled                            = "cancelled"
	DeliveryWorkUnknown                              = "unknown"
)

var ErrInputDeliveryConflict = errors.New("input delivery revision conflict")

// InputDeliveryCommitUnknownError means rename succeeded but directory sync
// failed. Callers must reconcile the file, never automatically retry the input.
type InputDeliveryCommitUnknownError struct{ Cause error }

func (e *InputDeliveryCommitUnknownError) Error() string { return e.Cause.Error() }
func (e *InputDeliveryCommitUnknownError) Unwrap() error { return e.Cause }

type InputDeliveryState struct {
	SessionID    string         `json:"session_id"`
	Revision     uint64         `json:"revision"`
	NextSequence int64          `json:"next_sequence"`
	Inputs       []PendingInput `json:"inputs"`
	Works        []DeliveryWork `json:"works"`
	EditHolds    []string       `json:"edit_holds"`
}

type DeliveryWork struct {
	WorkID  string `json:"work_id"`
	Origin  string `json:"origin"`
	Outcome string `json:"outcome"`
}

func (s *InputDeliveryState) Clone() *InputDeliveryState {
	if s == nil {
		return nil
	}
	out := *s
	out.Inputs = clonePendingInputs(s.Inputs)
	out.Works = slices.Clone(s.Works)
	out.EditHolds = slices.Clone(s.EditHolds)
	return &out
}

// AfterCompletionWait reports the outcome of the work a queued
// after-completion input waits behind: "" when it waits on nothing, the
// recorded outcome otherwise, and DeliveryWorkUnknown when the work is missing
// from the durable state. Only DeliveryWorkCompleted (or "") lets it start.
func (s *InputDeliveryState) AfterCompletionWait(input PendingInput) string {
	if input.WaitWorkID == "" {
		return ""
	}
	if s != nil {
		for _, work := range s.Works {
			if work.WorkID == input.WaitWorkID {
				return work.Outcome
			}
		}
	}
	return DeliveryWorkUnknown
}

// ValidateInputDelivery is shared by read and write. A corrupt existing file
// must never be mistaken for a new queue.
func ValidateInputDelivery(s *InputDeliveryState, sessionID string) error {
	if s == nil || s.SessionID == "" || s.SessionID != sessionID {
		return fmt.Errorf("input delivery session mismatch")
	}
	if s.Revision == 0 || s.NextSequence < 0 {
		return fmt.Errorf("invalid input delivery revision or sequence")
	}
	if len(s.Inputs) > InputDeliveryMaxInputs {
		return fmt.Errorf("input delivery exceeds %d inputs", InputDeliveryMaxInputs)
	}
	works := make(map[string]bool, len(s.Works))
	for _, w := range s.Works {
		if w.WorkID == "" || works[w.WorkID] {
			return fmt.Errorf("invalid or duplicate work identity")
		}
		if w.Origin != DeliveryWorkUser && w.Origin != DeliveryWorkFollowup && w.Origin != DeliveryWorkContext {
			return fmt.Errorf("invalid work origin %q", w.Origin)
		}
		switch w.Outcome {
		case DeliveryWorkActive, DeliveryWorkCompleted, DeliveryWorkFailed, DeliveryWorkCancelled, DeliveryWorkUnknown:
		default:
			return fmt.Errorf("invalid work outcome %q", w.Outcome)
		}
		works[w.WorkID] = true
	}
	ids := make(map[string]string, len(s.Inputs))
	sequences := make(map[int64]bool, len(s.Inputs))
	textBytes := 0
	for _, input := range s.Inputs {
		if input.InputID == "" || ids[input.InputID] != "" || input.OwnerTaskID != identity.MainAgentID {
			return fmt.Errorf("invalid input identity or owner")
		}
		if input.Revision == 0 || input.Sequence <= 0 || input.Sequence > s.NextSequence || sequences[input.Sequence] {
			return fmt.Errorf("invalid input revision or sequence")
		}
		if input.DeliveryPolicy != InputDeliveryNextRequest && input.DeliveryPolicy != InputDeliveryAfterCompletion {
			return fmt.Errorf("invalid delivery policy %q", input.DeliveryPolicy)
		}
		switch input.Stage {
		case PendingInputStageQueued:
			if input.WorkID != "" {
				return fmt.Errorf("queued input has work identity")
			}
			if input.WaitWorkID != "" && !works[input.WaitWorkID] {
				return fmt.Errorf("queued input waits on missing work")
			}
		case PendingInputStageClaimed, PendingInputStageStarted:
			if !works[input.WorkID] {
				return fmt.Errorf("consumed input references missing work")
			}
			if input.WaitWorkID != "" {
				return fmt.Errorf("consumed input retains wait identity")
			}
		default:
			return fmt.Errorf("invalid delivery input stage %q", input.Stage)
		}
		ids[input.InputID] = input.Stage
		sequences[input.Sequence] = true
		textBytes += len(input.Content)
		for _, p := range input.Parts {
			if !p.IsBinary() {
				textBytes += len(p.Text)
			}
		}
		if textBytes > InputDeliveryMaxTextBytes {
			return fmt.Errorf("input delivery text exceeds %d bytes", InputDeliveryMaxTextBytes)
		}
	}
	holds := make(map[string]bool, len(s.EditHolds))
	for _, id := range s.EditHolds {
		if holds[id] || ids[id] != PendingInputStageQueued {
			return fmt.Errorf("invalid edit hold %q", id)
		}
		holds[id] = true
	}
	return nil
}

// LoadInputDelivery returns nil only when the queue file does not exist.
func (r *RecoveryManager) LoadInputDelivery() (*InputDeliveryState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	return r.loadInputDeliveryLocked()
}

func (r *RecoveryManager) loadInputDeliveryLocked() (*InputDeliveryState, error) {
	data, err := os.ReadFile(filepath.Join(r.sessionDir, InputDeliveryFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read input delivery: %w", err)
	}
	var state InputDeliveryState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode input delivery: %w", err)
	}
	if err := ValidateInputDelivery(&state, filepath.Base(r.sessionDir)); err != nil {
		return nil, err
	}
	inputs, err := r.RestorePendingInputs(state.Inputs)
	if err != nil {
		return nil, err
	}
	state.Inputs = inputs
	sort.Slice(state.Inputs, func(i, j int) bool { return state.Inputs[i].Sequence < state.Inputs[j].Sequence })
	return &state, nil
}

// SaveInputDelivery performs CAS under the session manager lock and returns
// session-owned attachment references. The caller retains ownership of next.
func (r *RecoveryManager) SaveInputDelivery(expectedRevision uint64, next *InputDeliveryState) (*InputDeliveryState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	current, err := r.loadInputDeliveryLocked()
	if err != nil {
		return nil, err
	}
	var revision uint64
	if current != nil {
		revision = current.Revision
	}
	if revision != expectedRevision {
		return nil, ErrInputDeliveryConflict
	}
	if next == nil || expectedRevision == ^uint64(0) || next.Revision != expectedRevision+1 {
		return nil, fmt.Errorf("invalid next input delivery revision")
	}
	if err := ValidateInputDelivery(next, filepath.Base(r.sessionDir)); err != nil {
		return nil, err
	}
	candidate := next.Clone()
	sort.Slice(candidate.Inputs, func(i, j int) bool { return candidate.Inputs[i].Sequence < candidate.Inputs[j].Sequence })
	durableAttachments := make(map[string]int64)
	if current != nil {
		for _, input := range current.Inputs {
			for _, part := range input.Parts {
				if part.IsBinary() {
					durableAttachments[part.ImagePath] = part.DataBytes
				}
			}
		}
	}
	candidate.Inputs, err = r.persistPendingInputsLocked(candidate.Inputs, durableAttachments)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(candidate)
	if err != nil {
		return nil, fmt.Errorf("marshal input delivery: %w", err)
	}
	tmp := filepath.Join(r.sessionDir, fmt.Sprintf("input-delivery.%d.tmp", time.Now().UnixNano()))
	defer os.Remove(tmp)
	write := r.deliveryWrite
	if write == nil {
		write = privatefs.WriteFileSynced
	}
	if err := write(r.sessionDir, tmp, data); err != nil {
		return nil, fmt.Errorf("write input delivery: %w", err)
	}
	rename := r.deliveryRename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(tmp, filepath.Join(r.sessionDir, InputDeliveryFile)); err != nil {
		return nil, fmt.Errorf("rename input delivery: %w", err)
	}
	syncDir := r.deliverySyncDir
	if syncDir == nil {
		syncDir = privatefs.SyncDir
	}
	if err := syncDir(r.sessionDir); err != nil {
		return nil, &InputDeliveryCommitUnknownError{Cause: fmt.Errorf("sync input delivery directory: %w", err)}
	}
	return candidate, nil
}

// validateInputDeliveryCheckpointLocked prevents a pause checkpoint from
// becoming a second executable owner of managed inputs.
func (r *RecoveryManager) validateInputDeliveryCheckpointLocked(checkpoint *SessionPauseCheckpoint) error {
	state, err := r.loadInputDeliveryLocked()
	if err != nil {
		return err
	}
	if checkpoint.InputDeliveryRevision != 0 && (state == nil || state.Revision < checkpoint.InputDeliveryRevision) {
		return fmt.Errorf("input delivery checkpoint revision mismatch")
	}
	if state == nil {
		return nil
	}
	managed := make(map[string]bool, len(state.Inputs))
	for _, input := range state.Inputs {
		managed[input.InputID] = true
	}
	for _, input := range checkpoint.PendingInputs {
		if managed[input.InputID] {
			return fmt.Errorf("input %s already owned by delivery queue", input.InputID)
		}
	}
	return nil
}

func (r *RecoveryManager) ValidateInputDeliveryCheckpoint(checkpoint *SessionPauseCheckpoint) error {
	if checkpoint == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.validateInputDeliveryCheckpointLocked(checkpoint)
}
