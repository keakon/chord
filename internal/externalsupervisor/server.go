package externalsupervisor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"
)

// Supervisor exit codes. The managed process's own status travels in the
// lifecycle message, never in the supervisor's: a non-zero status here means
// the supervisor could not prove it left nothing behind.
const (
	ExitOK      = 0
	ExitFailure = 1
)

// Run is the supervisor entrypoint. It adopts the descriptors Chord passed,
// starts the managed process, and then stays alive for exactly one purpose: to
// tear that process group down when the run ends, when Chord asks for it, when
// Chord disappears, or when the supervisor itself is signalled.
//
// It never reads the business pipes and never writes a task result: Chord owns
// the conversation and the settlement, the supervisor owns the process.
func Run(_ []string) int {
	in := os.NewFile(LifecycleInFD, "lifecycle-in")
	out := os.NewFile(LifecycleOutFD, "lifecycle-out")
	childStdin := os.NewFile(ChildStdinFD, "managed-stdin")
	childStdout := os.NewFile(ChildStdoutFD, "managed-stdout")
	childStderr := os.NewFile(ChildStderrFD, "managed-stderr")
	if in == nil || out == nil || childStdin == nil || childStdout == nil || childStderr == nil {
		return ExitFailure
	}
	// Extra files are inherited without the flag, so the lifecycle channel has
	// to be closed to the managed process before it exists.
	setCloseOnExec(in, out, childStdin, childStdout, childStderr)

	reader := bufio.NewReader(in)
	request, err := readMessage(reader)
	if err != nil || request.Type != TypeStart {
		// Chord never asked for a process, so there is nothing to clean up.
		return ExitOK
	}
	if request.Command == "" {
		return fail(out, request, fmt.Errorf("start request without a command"))
	}
	killGrace := time.Duration(request.KillGraceMS) * time.Millisecond
	if killGrace <= 0 {
		killGrace = defaultKillGrace
	}
	cmd := exec.Command(request.Command, request.Args...)
	cmd.Dir = request.Dir
	cmd.Env = request.Env
	cmd.Stdin = childStdin
	cmd.Stdout = childStdout
	cmd.Stderr = childStderr
	configureProcessGroup(cmd)
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		childStdin.Close()
		childStdout.Close()
		childStderr.Close()
		return fail(out, request, fmt.Errorf("start %s: %w", request.Command, err))
	}
	// The managed process owns its ends now. Holding any of them would hide
	// EOF from Chord and make a dead process look alive.
	closeAll(childStdin, childStdout, childStderr)
	pid, pgid, err := processGroupID(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return fail(out, request, err)
	}

	run := &supervisedRun{
		request:   request,
		out:       out,
		reader:    reader,
		cmd:       cmd,
		pid:       pid,
		pgid:      pgid,
		killGrace: killGrace,
		startedAt: startedAt,
	}
	if err := run.handshake(); err != nil {
		// Chord is gone before the run was even acknowledged: clean up and
		// leave the receipt as the only trace.
		code := run.finish(ReasonHostGone, 0, 0)
		_ = run.out.Close()
		return code
	}
	return run.supervise()
}

// supervisedRun is one managed process the supervisor owns.
type supervisedRun struct {
	request   Message
	out       *os.File
	reader    *bufio.Reader
	cmd       *exec.Cmd
	pid       int
	pgid      int
	killGrace time.Duration
	startedAt time.Time
}

// handshake binds the run to Chord: until it answers, the worker must not be
// treated as runnable.
func (r *supervisedRun) handshake() error {
	return writeMessage(r.out, Message{Type: TypeStarted, RunID: r.request.RunID, PID: r.pid, PGID: r.pgid})
}

func (r *supervisedRun) supervise() int {
	childDone := make(chan error, 1)
	go func() { childDone <- r.cmd.Wait() }()

	stop := make(chan time.Duration, 1)
	hostGone := make(chan struct{})
	go func() {
		defer close(hostGone)
		for {
			message, err := readMessage(r.reader)
			if err != nil {
				// EOF: Chord exited, was killed, or closed the channel. Either
				// way it will never ask again.
				return
			}
			if message.Type != TypeStop || message.RunID != r.request.RunID {
				continue
			}
			grace := time.Duration(message.GraceMS) * time.Millisecond
			if grace <= 0 {
				grace = r.killGrace
			}
			select {
			case stop <- grace:
			default:
			}
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, shutdownSignals()...)
	defer signal.Stop(signals)

	reason := ReasonChildExit
	var childErr error
	reaped := false
	select {
	case childErr = <-childDone:
		reaped = true
	case grace := <-stop:
		reason = ReasonStopRequested
		// Give the managed process its grace period to go on its own before
		// the teardown below escalates.
		select {
		case childErr = <-childDone:
			reaped = true
		case <-time.After(grace):
		}
	case <-hostGone:
		reason = ReasonHostGone
	case <-signals:
		// A terminal interrupt or a shutdown of the supervisor's own group
		// must not orphan the managed process either.
		reason = ReasonSignal
	}
	// The teardown has to happen before the status is read: a process that is
	// still running has no exit status yet.
	cleanup, err := terminateGroup(r.pid, r.pgid, r.killGrace)
	code, sig := r.reap(childDone, reaped, childErr)
	result := r.finishWith(cleanup, err, reason, code, sig)
	_ = r.out.Close()
	return result
}

// reap waits for the managed process after teardown so its status can be
// reported. A process that outlives SIGKILL is reported as unproven instead of
// blocking the supervisor forever.
func (r *supervisedRun) reap(childDone chan error, reaped bool, childErr error) (code, sig int) {
	if !reaped {
		select {
		case childErr = <-childDone:
		case <-time.After(r.killGrace):
			return 0, 0
		}
	}
	return exitStatus(childErr)
}

// finish tears the process group down and reports how the run ended. Chord
// gets the report; when it cannot, the receipt is what is left behind.
func (r *supervisedRun) finish(reason string, code, sig int) int {
	cleanup, err := terminateGroup(r.pid, r.pgid, r.killGrace)
	return r.finishWith(cleanup, err, reason, code, sig)
}

// finishWith reports a run whose teardown already happened.
func (r *supervisedRun) finishWith(cleanup Cleanup, err error, reason string, code, sig int) int {
	if err != nil {
		cleanup.Proven = false
		if cleanup.Note == "" {
			cleanup.Note = err.Error()
		}
	}
	message := Message{
		Type:       TypeExited,
		RunID:      r.request.RunID,
		Reason:     reason,
		ExitCode:   code,
		ExitSignal: sig,
		Cleanup:    &cleanup,
	}
	delivered := writeMessage(r.out, message) == nil
	if !delivered || !cleanup.Proven {
		writeReceipt(r.request.ReceiptDir, Receipt{
			Version:    receiptVersion,
			RunID:      r.request.RunID,
			Command:    r.request.Command,
			Dir:        r.request.Dir,
			PID:        r.pid,
			PGID:       r.pgid,
			StartedAt:  r.startedAt,
			FinishedAt: time.Now(),
			Reason:     reason,
			ExitCode:   code,
			ExitSignal: sig,
			Cleanup:    cleanup,
		})
	}
	if !cleanup.Proven {
		return ExitFailure
	}
	return ExitOK
}

// fail reports a run that never started. There is no process group to tear
// down, so the only job left is telling Chord, or leaving a receipt.
func fail(out *os.File, request Message, err error) int {
	message := Message{Type: TypeError, RunID: request.RunID, Error: err.Error()}
	delivered := writeMessage(out, message) == nil
	if !delivered {
		writeReceipt(request.ReceiptDir, Receipt{
			Version:    receiptVersion,
			RunID:      request.RunID,
			Command:    request.Command,
			Dir:        request.Dir,
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
			Reason:     ReasonLaunchFailed,
			Cleanup:    Cleanup{Proven: true, Note: "no process was started"},
		})
	}
	if out != nil {
		_ = out.Close()
	}
	return ExitFailure
}

// writeReceipt leaves the minimal evidence of a cleanup Chord did not see. It
// is best effort by design: a supervisor that cannot write must still finish
// the teardown it was started for.
func writeReceipt(dir string, receipt Receipt) {
	dir = filepath.Clean(dir)
	if dir == "" || dir == "." {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return
	}
	path := filepath.Join(dir, receipt.RunID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}
