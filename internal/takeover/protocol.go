// Package takeover implements the authenticated local session transfer endpoint.
package takeover

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
)

const (
	ProtocolVersion         = 1
	MaxFrameBytes           = 64 * 1024
	MaxWorkers              = 4
	MaxBlockers             = 64
	MaxSocketPathBytes      = 100
	HandshakeTimeout        = 5 * time.Second
	WriteTimeout            = 2 * time.Second
	SourceTransferExitCode  = 20
	OwnerTUI                = "tui"
	OwnerGateway            = "gateway"
	ActionStart             = "start"
	ActionPreflight         = "preflight"
	ActionStatus            = "status"
	ActionRetrySave         = "retry_save"
	CodeInvalidRequest      = "invalid_request"
	CodeUnsupportedPlatform = "unsupported_platform"
	CodeWrongOwnerKind      = "wrong_owner_kind"
	CodeWrongWorkspace      = "wrong_workspace"
	CodeStaleSource         = "stale_source"
	CodeTransferBusy        = "transfer_busy"
	CodeTargetNotReady      = "target_not_ready"
	CodeInteractionBlocked  = "interaction_blocked"
	CodePersistenceFailed   = "persistence_failed"
	CodeLockBusy            = "lock_busy"
	CodeRestoreFailed       = "restore_failed"
	CodeStatusUnknown       = "status_unknown"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	cause   error
}

func (e *Error) Error() string                 { return e.Code + ": " + e.Message }
func (e *Error) Unwrap() error                 { return e.cause }
func protocolError(code, message string) error { return &Error{Code: code, Message: message} }

// Record contains only public discovery identity. The secret stays in a
// separate private key file and authenticated request frames.
type Record struct {
	Protocol     int    `json:"protocol"`
	RuntimeID    string `json:"runtime_id"`
	SessionID    string `json:"session_id"`
	SessionEpoch uint64 `json:"session_epoch"`
	Workspace    string `json:"workspace"`
	OwnerKind    string `json:"owner_kind"`
	SocketPath   string `json:"socket_path"`
}

type Blocker struct {
	OwnerTaskID string    `json:"owner_task_id"`
	Type        string    `json:"type"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	Description string    `json:"description"`
}

// Status is the bounded CLI and local protocol projection. It never carries
// input contents, model output, secret values, or tool arguments.
type Status struct {
	BindingInstanceID string `json:"binding_instance_id,omitempty"`
	recovery.TransferIdentity
	State             string    `json:"state"`
	StartedAt         time.Time `json:"started_at,omitempty"`
	WaitExpired       bool      `json:"wait_expired,omitempty"`
	PendingInputs     int       `json:"pending_inputs,omitempty"`
	CheckpointID      string    `json:"checkpoint_id,omitempty"`
	ErrorCode         string    `json:"error_code,omitempty"`
	Message           string    `json:"message,omitempty"`
	RecoverableAction string    `json:"recoverable_action,omitempty"`
	Blockers          []Blocker `json:"blockers,omitempty"`
	BlockerCount      int       `json:"blocker_count"`
	BlockerOffset     int       `json:"blocker_offset,omitempty"`
	NextOffset        int       `json:"next_offset,omitempty"`
}

func PublicStatus(s agent.SessionPauseStatus) Status { return pageStatus(s, 0) }
func pageStatus(s agent.SessionPauseStatus, offset int) Status {
	total := max(s.BlockerCount, len(s.Blockers))
	out := Status{TransferIdentity: s.TransferIdentity, State: s.State, StartedAt: s.StartedAt, WaitExpired: s.WaitExpired, PendingInputs: s.PendingInputCount, CheckpointID: s.CheckpointID, ErrorCode: s.ErrorCode, Message: boundedText(s.Error, 512), RecoverableAction: boundedText(s.RecoverableAction, 128), BlockerCount: total, BlockerOffset: offset}
	start := min(max(offset, 0), len(s.Blockers))
	limit := min(start+MaxBlockers, len(s.Blockers))
	end := start
	base, _ := json.Marshal(out)
	encodedSize := len(base) + 32
	for _, b := range s.Blockers[start:limit] {
		blocker := Blocker{OwnerTaskID: boundedText(b.OwnerTaskID, 128), Type: boundedText(b.Kind, 64), StartedAt: b.StartedAt, Description: boundedText(b.Description, 256)}
		// Leave room for the authenticated response envelope. JSON escaping
		// can make short control-character descriptions much larger on wire.
		data, err := json.Marshal(blocker)
		if err != nil || encodedSize+len(data)+1 > MaxFrameBytes/2 {
			break
		}
		encodedSize += len(data) + 1
		out.Blockers = append(out.Blockers, blocker)
		end++
	}
	if end > start && end < total {
		out.NextOffset = end
	}
	return out
}
func (s Status) AgentStatus() agent.SessionPauseStatus {
	out := agent.SessionPauseStatus{TransferIdentity: s.TransferIdentity, State: s.State, StartedAt: s.StartedAt, WaitExpired: s.WaitExpired, PendingInputCount: s.PendingInputs, CheckpointID: s.CheckpointID, ErrorCode: s.ErrorCode, Error: s.Message, RecoverableAction: s.RecoverableAction, BlockerCount: s.BlockerCount}
	for _, b := range s.Blockers {
		out.Blockers = append(out.Blockers, agent.SessionPauseBlocker{OwnerTaskID: b.OwnerTaskID, Kind: b.Type, StartedAt: b.StartedAt, Description: b.Description})
	}
	return out
}
func boundedText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}

type request struct {
	Protocol      int                       `json:"protocol"`
	Secret        string                    `json:"secret"`
	Source        Record                    `json:"source"`
	Action        string                    `json:"action"`
	Identity      recovery.TransferIdentity `json:"identity"`
	BlockerOffset int                       `json:"blocker_offset,omitempty"`
}
type response struct {
	Protocol int     `json:"protocol"`
	Source   Record  `json:"source"`
	Status   *Status `json:"status,omitempty"`
	Error    *Error  `json:"error,omitempty"`
}

func readFrame(r io.Reader, dst any) error {
	line, err := bufio.NewReaderSize(r, MaxFrameBytes+1).ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > MaxFrameBytes {
		return protocolError(CodeInvalidRequest, "request exceeds frame limit")
	}
	if err != nil {
		return fmt.Errorf("read takeover frame: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return protocolError(CodeInvalidRequest, "invalid request frame")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return protocolError(CodeInvalidRequest, "request must contain one JSON value")
	}
	return nil
}
func writeFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode takeover frame: %w", err)
	}
	if len(data)+1 > MaxFrameBytes {
		return protocolError(CodeInvalidRequest, "response exceeds frame limit")
	}
	data = append(data, '\n')
	n, err := w.Write(data)
	if err != nil {
		return fmt.Errorf("write takeover frame: %w", err)
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func publicError(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return &Error{Code: e.Code, Message: boundedText(e.Message, 512)}
	}
	if e, ok := errors.AsType[*agent.SessionPauseError](err); ok {
		return &Error{Code: e.Code, Message: boundedText(e.Cause.Error(), 512)}
	}
	return &Error{Code: CodePersistenceFailed, Message: "session transfer operation failed"}
}
