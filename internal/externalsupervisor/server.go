package externalsupervisor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
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
	out, err := lifecycleOutputFile(LifecycleOutFD)
	if err != nil {
		return ExitFailure
	}
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
	// Install the shutdown handler before the managed process exists. The
	// supervisor may otherwise receive SIGHUP/SIGQUIT in the launch window
	// and die without tearing the child group down or writing a receipt.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, shutdownSignals()...)
	defer signal.Stop(signals)
	select {
	case <-signals:
		return ExitOK
	default:
	}
	if request.Command == "" {
		return fail(out, request, fmt.Errorf("start request without a command"), false, 0, 0, "", time.Now(), Cleanup{Proven: true, Note: "launch failed before a process was started"})
	}
	killGrace := time.Duration(request.KillGraceMS) * time.Millisecond
	if killGrace <= 0 {
		killGrace = defaultKillGrace
	}
	return launchManaged(out, request, reader, childStdin, childStdout, childStderr, signals, killGrace)
}

func launchManaged(out *os.File, request Message, reader *bufio.Reader, childStdin, childStdout, childStderr *os.File, signals <-chan os.Signal, killGrace time.Duration) int {
	cmd := exec.Command(request.Command, request.Args...)
	cmd.Dir = request.Dir
	cmd.Env = request.Env
	cmd.Stdin = childStdin
	cmd.Stdout = childStdout
	cmd.Stderr = childStderr
	configureProcessGroup(cmd)
	startedAt := time.Now()
	select {
	case <-signals:
		return ExitOK
	default:
	}
	if err := cmd.Start(); err != nil {
		childStdin.Close()
		childStdout.Close()
		childStderr.Close()
		return fail(out, request, fmt.Errorf("start %s: %w", request.Command, err), false, 0, 0, "", startedAt, Cleanup{Proven: true, Note: "launch failed before a process was started"})
	}
	// One reaper owns cmd.Wait for the rest of the run. The handshake-failure
	// and supervision paths share it: a zombie leader is still a group member,
	// so without reaping first the cleanup probe waits out both grace periods
	// and reports an unproven receipt for a group that is already empty.
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()
	// The managed process owns its ends now. Holding any of them would hide
	// EOF from Chord and make a dead process look alive.
	closeAll(childStdin, childStdout, childStderr)
	pid, pgid, err := managedProcessGroupID(cmd)
	if err != nil {
		startToken, _ := processStartToken(pid)
		if pgid <= 0 {
			pgid = pid
		}
		cleanup, cleanupErr := cleanupUnboundProcess(cmd, pid, pgid, startToken, killGrace, childDone)
		if cleanupErr != nil {
			cleanup.Proven = false
			if cleanup.Note == "" {
				cleanup.Note = cleanupErr.Error()
			}
		}
		return fail(out, request, err, true, pid, pgid, startToken, startedAt, cleanup)
	}
	startToken, tokenErr := processStartToken(pid)
	if tokenErr != nil {
		cleanup, cleanupErr := cleanupUnboundProcess(cmd, pid, pgid, "", killGrace, childDone)
		if cleanupErr != nil {
			cleanup.Proven = false
			if cleanup.Note == "" {
				cleanup.Note = cleanupErr.Error()
			}
		}
		return fail(out, request, fmt.Errorf("read managed process identity: %w", tokenErr), true, pid, pgid, "", startedAt, cleanup)
	}

	run := &supervisedRun{
		request:    request,
		out:        out,
		reader:     reader,
		cmd:        cmd,
		pid:        pid,
		pgid:       pgid,
		startToken: startToken,
		childDone:  childDone,
		signals:    signals,
		killGrace:  killGrace,
		startedAt:  startedAt,
	}
	// A signal can arrive between cmd.Start and supervise. Consume it before
	// the handshake so this run still tears down the group it just created.
	select {
	case <-signals:
		return run.finish(ReasonSignal)
	default:
	}
	if err := run.handshake(); err != nil {
		// Chord is gone before the run was even acknowledged: clean up and
		// leave the receipt as the only trace.
		code := run.finish(ReasonHostGone)
		_ = run.out.Close()
		return code
	}
	return run.supervise()
}

var managedProcessGroupID = processGroupID

// cleanupUnboundProcess tears down a launch that started but whose group could
// not be read. A setsid child leads its own group, so the pid is the group id
// when the read itself failed; a different reported group is never signalled.
func cleanupUnboundProcess(cmd *exec.Cmd, pid, pgid int, startToken string, grace time.Duration, childDone <-chan error) (Cleanup, error) {
	if cmd == nil || cmd.Process == nil {
		return Cleanup{}, nil
	}
	if pgid != pid {
		_ = cmd.Process.Kill()
		waitForReap(childDone, grace)
		return Cleanup{Note: "managed process did not lead its own process group"}, fmt.Errorf("managed process %d leads group %d", pid, pgid)
	}
	cleanup, err := terminateGroup(pid, pgid, startToken, grace)
	waitForReap(childDone, grace)
	return cleanup, err
}

func waitForReap(childDone <-chan error, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-childDone:
	case <-timer.C:
	}
}

// supervisedRun is one managed process the supervisor owns.
type supervisedRun struct {
	request    Message
	out        *os.File
	reader     *bufio.Reader
	cmd        *exec.Cmd
	pid        int
	pgid       int
	startToken string
	childDone  chan error
	signals    <-chan os.Signal
	killGrace  time.Duration
	startedAt  time.Time
	reaped     bool
	childErr   error
}

// handshake binds the run to Chord: until it answers, the worker must not be
// treated as runnable.
func (r *supervisedRun) handshake() error {
	return writeLifecycleMessage(r.out, Message{Type: TypeStarted, RunID: r.request.RunID, PID: r.pid, PGID: r.pgid, StartToken: r.startToken}, time.Now().Add(r.killGrace))
}

func (r *supervisedRun) supervise() int {
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

	reason := ReasonChildExit
	select {
	case r.childErr = <-r.childDone:
		r.reaped = true
	case grace := <-stop:
		reason = ReasonStopRequested
		// Give the managed process its grace period to go on its own before
		// the teardown below escalates.
		select {
		case r.childErr = <-r.childDone:
			r.reaped = true
		case <-time.After(grace):
		}
	case <-hostGone:
		reason = ReasonHostGone
	case <-r.signals:
		// A terminal interrupt or a shutdown of the supervisor's own group
		// must not orphan the managed process either.
		reason = ReasonSignal
	}
	// The teardown has to happen before the status is read: a process that is
	// still running has no exit status yet.
	cleanup, err := terminateGroup(r.pid, r.pgid, r.startToken, r.killGrace)
	code, sig := r.reap()
	result := r.finishWith(cleanup, err, reason, code, sig)
	_ = r.out.Close()
	return result
}

// reap waits for the managed process after teardown so its status can be
// reported. A process that outlives SIGKILL is reported as unproven instead of
// blocking the supervisor forever.
func (r *supervisedRun) reap() (code, sig int) {
	if !r.reaped {
		select {
		case r.childErr = <-r.childDone:
			r.reaped = true
		case <-time.After(r.killGrace):
			return 0, 0
		}
	}
	return exitStatus(r.childErr)
}

// finish tears the process group down and reports how the run ended. Chord
// gets the report; when it cannot, the receipt is what is left behind.
func (r *supervisedRun) finish(reason string) int {
	cleanup, err := terminateGroup(r.pid, r.pgid, r.startToken, r.killGrace)
	code, sig := r.reap()
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
	// The receipt is written for every run, not only when delivery failed: a
	// successful pipe write only means Chord's supervisor client read the exit,
	// not that Chord persisted it before dying. Recovery looks up the receipt by
	// this run's exact ID; old receipts are pruned by the host.
	writeReceipt(r.request.ReceiptDir, Receipt{
		Version:        receiptVersion,
		RunID:          r.request.RunID,
		Command:        r.request.Command,
		Args:           slices.Clone(r.request.Args),
		Dir:            r.request.Dir,
		ProcessStarted: true,
		PID:            r.pid,
		PGID:           r.pgid,
		StartToken:     r.startToken,
		StartedAt:      r.startedAt,
		FinishedAt:     time.Now(),
		Reason:         reason,
		ExitCode:       code,
		ExitSignal:     sig,
		Cleanup:        cleanup,
	})
	_ = writeLifecycleMessage(r.out, message, time.Now().Add(r.killGrace))
	if !cleanup.Proven {
		return ExitFailure
	}
	return ExitOK
}

// fail reports a launch failure and leaves the receipt of that failure. A
// failed group lookup still started a process, so its cleanup evidence must
// travel with the receipt instead of being reported as an unbound success.
func fail(out *os.File, request Message, err error, processStarted bool, pid, pgid int, startToken string, startedAt time.Time, cleanup Cleanup) int {
	writeReceipt(request.ReceiptDir, Receipt{
		Version:        receiptVersion,
		RunID:          request.RunID,
		Command:        request.Command,
		Args:           slices.Clone(request.Args),
		Dir:            request.Dir,
		ProcessStarted: processStarted,
		PID:            pid,
		PGID:           pgid,
		StartToken:     startToken,
		StartedAt:      startedAt,
		FinishedAt:     time.Now(),
		Reason:         ReasonLaunchFailed,
		Cleanup:        cleanup,
	})
	if out != nil {
		grace := time.Duration(request.KillGraceMS) * time.Millisecond
		if grace <= 0 {
			grace = defaultKillGrace
		}
		_ = writeLifecycleMessage(out, Message{Type: TypeError, RunID: request.RunID, Error: err.Error()}, time.Now().Add(grace))
		_ = out.Close()
	}
	return ExitFailure
}

// writeReceipt leaves the lifecycle evidence of one run. It is best effort by
// design: a supervisor that cannot write must still finish the teardown it was
// started for.
func writeReceipt(dir string, receipt Receipt) {
	dir = filepath.Clean(dir)
	if dir == "" || dir == "." || !ValidRunID(receipt.RunID) {
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
