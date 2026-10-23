// Package externalsupervisor owns the process lifecycle of one managed
// external coding-agent process on Chord's behalf.
//
// Chord keeps the business pipes — the managed process's stdin, stdout and
// stderr — and speaks the agent's own protocol over them. This package never
// reads or parses that traffic. It exists because Chord itself can be killed:
// a worker's process group then outlives the only process that knew how to
// stop it. Every worker therefore gets one independent supervisor process whose
// only job is to tear that group down when the run ends, when Chord asks for
// it, when Chord disappears, or when the supervisor is signalled.
//
// Chord and the supervisor talk over a lifecycle channel made of two pipes the
// managed process never sees: the supervisor adopts the business pipes by
// number and marks every lifecycle descriptor close-on-exec before it starts
// the process.
package externalsupervisor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// CommandName is the hidden chord subcommand that runs a supervisor. Chord
// re-executes itself with it, so the supervisor is always the same build as
// the process that spawned it.
const CommandName = "external-supervisor"

// Inherited descriptor layout of a supervisor process. Chord passes the files
// as cmd.ExtraFiles in exactly this order and the supervisor adopts them by
// number: exec guarantees extra files start at 3.
const (
	LifecycleInFD  = 3 // host -> supervisor: start request, stop request, EOF
	LifecycleOutFD = 4 // supervisor -> host: handshake, exit, cleanup result
	ChildStdinFD   = 5 // business pipes the managed process uses
	ChildStdoutFD  = 6
	ChildStderrFD  = 7
)

// Lifecycle message types.
const (
	TypeStart   = "start"   // host -> supervisor
	TypeStarted = "started" // supervisor -> host
	TypeStop    = "stop"    // host -> supervisor
	TypeExited  = "exited"  // supervisor -> host
	TypeError   = "error"   // supervisor -> host
)

// Run outcomes recorded in a receipt.
const (
	ReasonChildExit     = "child_exit"
	ReasonStopRequested = "stop_requested"
	ReasonHostGone      = "host_gone"
	ReasonSignal        = "signal"
	ReasonLaunchFailed  = "launch_failed"
)

// Message is one JSON object on the lifecycle channel. The envelope is flat:
// fields that do not apply to Type are zero, and no message ever carries
// business traffic from the managed process.
type Message struct {
	Type  string `json:"type"`
	RunID string `json:"run_id,omitempty"`

	// start: what the supervisor must launch.
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	Env         []string `json:"env,omitempty"`
	Dir         string   `json:"dir,omitempty"`
	ReceiptDir  string   `json:"receipt_dir,omitempty"`
	KillGraceMS int64    `json:"kill_grace_ms,omitempty"`

	// started: the process-group identity the supervisor bound for this run.
	PID  int `json:"pid,omitempty"`
	PGID int `json:"pgid,omitempty"`

	// stop: how long the managed process may take to go away on its own.
	GraceMS int64 `json:"grace_ms,omitempty"`

	// exited and error: how the run ended and what the teardown achieved.
	Reason     string   `json:"reason,omitempty"`
	ExitCode   int      `json:"exit_code,omitempty"`
	ExitSignal int      `json:"exit_signal,omitempty"`
	Cleanup    *Cleanup `json:"cleanup,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// Cleanup is the evidence of one teardown: what was signalled, and whether the
// process group was confirmed gone afterwards. Chord treats Proven as the only
// proof that the worker left nothing behind.
type Cleanup struct {
	Terminated bool   `json:"terminated"`
	Killed     bool   `json:"killed"`
	Proven     bool   `json:"proven"`
	Note       string `json:"note,omitempty"`
}

// Receipt is the minimal lifecycle evidence the supervisor leaves behind when
// it cannot hand its result to Chord, or when teardown stayed unproven. It is
// not a task result: Chord still owns task settlement, tool results and
// mailboxes, and the supervisor never writes any of them.
type Receipt struct {
	Version        int       `json:"version"`
	RunID          string    `json:"run_id"`
	Command        string    `json:"command"`
	Args           []string  `json:"args,omitempty"`
	Dir            string    `json:"dir,omitempty"`
	ProcessStarted bool      `json:"process_started,omitempty"`
	PID            int       `json:"pid"`
	PGID           int       `json:"pgid"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	Reason         string    `json:"reason"`
	ExitCode       int       `json:"exit_code,omitempty"`
	ExitSignal     int       `json:"exit_signal,omitempty"`
	Cleanup        Cleanup   `json:"cleanup"`
}

const receiptVersion = 1

func writeMessage(writer io.Writer, message Message) error {
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode lifecycle message: %w", err)
	}
	data = append(data, '\n')
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("write lifecycle message: %w", err)
	}
	return nil
}

// readMessage decodes one lifecycle message. A closed channel reports io.EOF,
// which both sides treat as "the other process is gone".
func readMessage(reader *bufio.Reader) (Message, error) {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return Message{}, err
	}
	var message Message
	if err := json.Unmarshal(line, &message); err != nil {
		return Message{}, fmt.Errorf("decode lifecycle message: %w", err)
	}
	if message.Type == "" {
		return Message{}, fmt.Errorf("lifecycle message without a type")
	}
	return message, nil
}
