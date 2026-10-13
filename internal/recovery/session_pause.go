package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
)

const (
	PendingInputStageQueued           = "queued"
	PendingInputStageTranscriptStaged = "transcript_staged"
)

// TransferIdentity binds a checkpoint to one source runtime and target.
type TransferIdentity struct {
	TransferID         string `json:"transfer_id"`
	SessionID          string `json:"session_id"`
	SourceRuntimeID    string `json:"source_runtime_id"`
	SourceSessionEpoch uint64 `json:"source_session_epoch"`
	Workspace          string `json:"workspace"`
	TargetKind         string `json:"target_kind"`
	TargetInstanceID   string `json:"target_instance_id"`
}

// SessionPauseCheckpoint preserves accepted input independently of runtime queues.
// Complete means contents are sealed, not that the source lock was released.
type SessionPauseCheckpoint struct {
	Identity           TransferIdentity `json:"identity"`
	CheckpointID       string           `json:"checkpoint_id"`
	Paused             bool             `json:"paused"`
	CheckpointComplete bool             `json:"checkpoint_complete"`
	PendingInputs      []PendingInput   `json:"pending_inputs,omitempty"`
	FrozenAt           time.Time        `json:"frozen_at"`
}

// PendingInput contains no runtime handles. OwnerTaskID is the durable owner;
// Sequence only orders inputs belonging to that owner.
type PendingInput struct {
	InputID             string                   `json:"input_id"`
	OwnerTaskID         string                   `json:"owner_task_id"`
	Sequence            int64                    `json:"sequence"`
	Revision            uint64                   `json:"revision"`
	DraftID             string                   `json:"draft_id,omitempty"`
	RequestID           string                   `json:"request_id,omitempty"`
	Content             string                   `json:"content"`
	Parts               []message.ContentPart    `json:"parts,omitempty"`
	Kind                string                   `json:"kind,omitempty"`
	FromUser            bool                     `json:"from_user"`
	MailboxAckID        string                   `json:"mailbox_ack_id,omitempty"`
	Mailbox             *message.MailboxMetadata `json:"mailbox,omitempty"`
	DrainContextAppends bool                     `json:"drain_context_appends,omitempty"`
	Stage               string                   `json:"stage"`
}

// Clone returns a checkpoint that can be handed to a persistence worker.
func (p *SessionPauseCheckpoint) Clone() *SessionPauseCheckpoint {
	if p == nil {
		return nil
	}
	out := *p
	out.PendingInputs = clonePendingInputs(p.PendingInputs)
	return &out
}

func clonePendingInputs(inputs []PendingInput) []PendingInput {
	out := append([]PendingInput(nil), inputs...)
	for i := range out {
		m := (message.Message{Parts: out[i].Parts, Mailbox: out[i].Mailbox}).Clone()
		out[i].Parts, out[i].Mailbox = m.Parts, m.Mailbox
	}
	return out
}

// PersistPendingInputs moves every attachment into session-owned storage and
// synchronizes it before returning references suitable for snapshot.json.
func (r *RecoveryManager) PersistPendingInputs(inputs []PendingInput) ([]PendingInput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	return r.persistPendingInputsLocked(inputs)
}

func (r *RecoveryManager) persistPendingInputsLocked(inputs []PendingInput) ([]PendingInput, error) {
	out := clonePendingInputs(inputs)
	for i := range out {
		if out[i].InputID == "" || out[i].OwnerTaskID == "" {
			return nil, fmt.Errorf("pending input requires input and owner identity")
		}
		if out[i].Stage != PendingInputStageQueued && out[i].Stage != PendingInputStageTranscriptStaged {
			return nil, fmt.Errorf("invalid pending input stage %q", out[i].Stage)
		}
		// Path-only parts may still point at temporary uploads. Read them before
		// persisting so source cleanup cannot remove a successfully saved input.
		for j := range out[i].Parts {
			part := &out[i].Parts[j]
			if !part.IsBinary() {
				continue
			}
			if len(part.Data) == 0 {
				if part.ImagePath == "" {
					return nil, fmt.Errorf("pending input %s attachment has no data", out[i].InputID)
				}
				if pathWithin(r.imagesDir(), part.ImagePath) {
					info, err := privatefs.Stat(r.sessionDir, part.ImagePath)
					if err != nil {
						return nil, fmt.Errorf("stat pending attachment: %w", err)
					}
					if !info.Mode().IsRegular() {
						return nil, fmt.Errorf("pending attachment is not a regular file")
					}
					f, err := privatefs.OpenFile(r.sessionDir, part.ImagePath, os.O_RDWR)
					if err != nil {
						return nil, fmt.Errorf("open pending attachment: %w", err)
					}
					syncErr := f.Sync()
					closeErr := f.Close()
					if syncErr != nil {
						return nil, fmt.Errorf("sync pending attachment: %w", syncErr)
					}
					if closeErr != nil {
						return nil, fmt.Errorf("close pending attachment: %w", closeErr)
					}
					if err := privatefs.SyncDir(r.imagesDir()); err != nil {
						return nil, fmt.Errorf("sync attachment directory: %w", err)
					}
					part.DataBytes = info.Size()
					continue
				}
				data, err := os.ReadFile(part.ImagePath)
				if err != nil {
					return nil, fmt.Errorf("read pending attachment: %w", err)
				}
				if len(data) == 0 {
					return nil, fmt.Errorf("pending attachment is empty")
				}
				part.Data = data
			}
			part.ImagePath = ""
		}
		msg, err := r.persistBinaryParts(message.Message{Parts: out[i].Parts}, true)
		if err != nil {
			return nil, fmt.Errorf("persist pending input %s: %w", out[i].InputID, err)
		}
		out[i].Parts = msg.Parts
	}
	return out, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// RestorePendingInputs verifies lazy attachment references without loading all
// payloads into memory. Missing or external blobs make restore fail explicitly.
func (r *RecoveryManager) RestorePendingInputs(inputs []PendingInput) ([]PendingInput, error) {
	out := clonePendingInputs(inputs)
	for i := range out {
		for j := range out[i].Parts {
			part := &out[i].Parts[j]
			if !part.IsBinary() {
				continue
			}
			if len(part.Data) > 0 || !pathWithin(r.imagesDir(), part.ImagePath) {
				return nil, fmt.Errorf("pending input %s attachment is not session-owned", out[i].InputID)
			}
			info, err := privatefs.Stat(r.sessionDir, part.ImagePath)
			if err != nil {
				return nil, fmt.Errorf("restore pending input %s attachment: %w", out[i].InputID, err)
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("pending attachment is not a regular file")
			}
			if part.DataBytes > 0 && part.DataBytes != info.Size() {
				return nil, fmt.Errorf("pending attachment size mismatch")
			}
			part.DataBytes = info.Size()
		}
	}
	return out, nil
}

// ReconcilePendingInputs derives staging from durable transcript identities.
// An in-memory append may not survive a crash, and a durable append may outlive
// its checkpoint update. Neither case proves that a model request was sent.
func ReconcilePendingInputs(inputs []PendingInput, transcripts map[string][]message.Message) []PendingInput {
	out := clonePendingInputs(inputs)
	committed := make(map[string]map[string]bool, len(transcripts))
	for owner, messages := range transcripts {
		ids := make(map[string]bool)
		for _, msg := range messages {
			if msg.InputID != "" {
				ids[msg.InputID] = true
			}
		}
		committed[owner] = ids
	}
	for i := range out {
		out[i].Stage = PendingInputStageQueued
		if committed[out[i].OwnerTaskID][out[i].InputID] {
			out[i].Stage = PendingInputStageTranscriptStaged
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OwnerTaskID != out[j].OwnerTaskID {
			return out[i].OwnerTaskID < out[j].OwnerTaskID
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out
}

// ValidatePendingInputRewrite checks that a rewrite preserves the original
// transcript evidence for input identities still present in the checkpoint.
func (r *RecoveryManager) ValidatePendingInputRewrite(agentID string, msgs []message.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.guardPendingInputRewriteLocked(agentID, msgs)
}

func (r *RecoveryManager) guardPendingInputRewriteLocked(agentID string, msgs []message.Message) error {
	snap, err := r.Recover()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read pending input checkpoint: %w", err)
	}
	if snap.Pause == nil || len(snap.Pause.PendingInputs) == 0 {
		return nil
	}
	existing, err := r.LoadMessages(agentID)
	if err != nil {
		return err
	}
	retained := make(map[string]message.Message)
	for _, msg := range msgs {
		if msg.InputID != "" {
			retained[msg.InputID] = msg
		}
	}
	pending := make(map[string]bool)
	for _, input := range snap.Pause.PendingInputs {
		pending[input.InputID] = true
	}
	for _, msg := range existing {
		if !pending[msg.InputID] {
			continue
		}
		replacement, ok := retained[msg.InputID]
		if !ok {
			return fmt.Errorf("rewrite would remove pending input %s before checkpoint acknowledgement", msg.InputID)
		}
		if replacement.Role != msg.Role || replacement.Content != msg.Content || !samePendingParts(msg.Parts, replacement.Parts) {
			return fmt.Errorf("rewrite would change pending input %s before checkpoint acknowledgement", msg.InputID)
		}
	}
	return nil
}

func samePendingParts(a, b []message.ContentPart) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Text != b[i].Text || a[i].MimeType != b[i].MimeType || a[i].FileName != b[i].FileName || a[i].PayloadBytes() != b[i].PayloadBytes() {
			return false
		}
	}
	return true
}
