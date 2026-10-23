package externalsupervisor

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Defaults for the two waits Chord performs on a supervisor: the startup
// handshake that binds the run's identity, and the SIGTERM → SIGKILL
// escalation both sides use when a process group has to be torn down.
const (
	defaultHandshakeTimeout = 30 * time.Second
	defaultKillGrace        = 5 * time.Second
	// readDrainGrace bounds how long Wait waits for the lifecycle reader once
	// the supervisor process is already gone.
	readDrainGrace = 500 * time.Millisecond
)

// Config describes one supervised launch.
type Config struct {
	// Command starts the supervisor. Empty resolves to the running Chord
	// executable with CommandName, so a supervisor is always the same build as
	// the Chord that spawned it.
	Command []string
	// Child is the managed process the supervisor owns.
	Child ChildSpec
	// RunID is the identity Chord persisted before this launch. The supervisor
	// writes its receipt under this ID, so recovery can look it up exactly.
	// Empty generates a fresh ID.
	RunID string
	// ReceiptDir is where the supervisor leaves lifecycle evidence for every
	// run. Empty disables the receipt.
	ReceiptDir string
	// KillGrace bounds the supervisor's SIGTERM → SIGKILL escalation and
	// Chord's own verification afterwards. Zero uses 5s.
	KillGrace time.Duration
	// HandshakeTimeout bounds the startup handshake: until it completes the
	// worker must not be treated as runnable. Zero uses 30s.
	HandshakeTimeout time.Duration
}

// ChildSpec is the managed process, exactly as the supervisor must exec it.
// The supervisor adds nothing to it: arguments and environment stay Chord's
// decision, and Chord already validated them.
type ChildSpec struct {
	Command string
	Args    []string
	Env     []string
	Dir     string
}

// StartError reports a launch that failed after the supervisor process was
// spawned. CleanupProven says whether the failed launch left no supervised
// process group behind. A caller that cannot prove cleanup must not treat the
// run as ended.
type StartError struct {
	Err           error
	CleanupProven bool
}

func (e *StartError) Error() string { return e.Err.Error() }

func (e *StartError) Unwrap() error { return e.Err }

// StartCleanupProven reports the cleanup evidence of a failed launch. known is
// false for errors raised before the supervisor was spawned; such a launch
// cannot have started a managed process.
func StartCleanupProven(err error) (proven, known bool) {
	if startErr, ok := errors.AsType[*StartError](err); ok {
		return startErr.CleanupProven, true
	}
	return false, false
}

// Report is the supervisor's account of the run. Reported is false when the
// supervisor never described the run, which leaves Chord with nothing but the
// identity it bound at handshake.
type Report struct {
	RunID      string
	PID        int
	PGID       int
	StartToken string
	Reason     string
	ExitCode   int
	ExitSignal int
	Cleanup    Cleanup
	Reported   bool
}

// Supervisor is Chord's handle on one independent lifecycle process. Chord
// keeps the business pipes and the host end of the lifecycle channel; the
// supervisor owns the managed process and is the only reason it can still be
// cleaned up after Chord is killed.
type Supervisor struct {
	cmd *exec.Cmd
	in  *os.File // host -> supervisor
	out *os.File // supervisor -> host

	stdin  *os.File // host end of the managed process's stdin
	stdout *os.File
	stderr *os.File

	runID      string
	receiptDir string
	killGrace  time.Duration
	startToken string

	handshake chan Message
	readDone  chan struct{}

	mu      sync.Mutex
	report  Report
	readErr error

	waitOnce sync.Once
	waitErr  error
	waited   chan struct{}
	writeMu  sync.Mutex
	stopOnce sync.Once
	stopErr  error
}

// Start creates the business pipes and the lifecycle channel, spawns the
// supervisor and waits for the handshake that binds this run's identity. It
// fails closed: no handshake means no managed process Chord may talk to.
func Start(cfg Config) (*Supervisor, error) {
	command, err := resolveChildCommand(cfg.Child.Command)
	if err != nil {
		return nil, err
	}
	cfg.Child.Command = command
	argv, err := supervisorCommand(cfg.Command)
	if err != nil {
		return nil, err
	}
	killGrace := cfg.KillGrace
	if killGrace <= 0 {
		killGrace = defaultKillGrace
	}
	handshakeTimeout := cfg.HandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = defaultHandshakeTimeout
	}
	runID, err := resolveRunID(cfg.RunID)
	if err != nil {
		return nil, err
	}
	// Lifecycle channel: two pipes, one per direction, so each side can see
	// the other's death as EOF.
	supInR, hostInW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create lifecycle pipe: %w", err)
	}
	hostOutR, supOutW, err := os.Pipe()
	if err != nil {
		closeAll(supInR, hostInW)
		return nil, fmt.Errorf("create lifecycle pipe: %w", err)
	}
	// Business pipes: Chord keeps the host ends for the whole run and hands
	// the rest to the supervisor, which passes them to the managed process.
	supStdinR, hostStdinW, err := os.Pipe()
	if err != nil {
		closeAll(supInR, hostInW, hostOutR, supOutW)
		return nil, fmt.Errorf("create managed stdin pipe: %w", err)
	}
	hostStdoutR, supStdoutW, err := os.Pipe()
	if err != nil {
		closeAll(supInR, hostInW, hostOutR, supOutW, supStdinR, hostStdinW)
		return nil, fmt.Errorf("create managed stdout pipe: %w", err)
	}
	hostStderrR, supStderrW, err := os.Pipe()
	if err != nil {
		closeAll(supInR, hostInW, hostOutR, supOutW, supStdinR, hostStdinW, hostStdoutR, supStdoutW)
		return nil, fmt.Errorf("create managed stderr pipe: %w", err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	// The supervisor needs no credentials and no configuration: it gets a
	// minimal environment and talks to the terminal through nothing at all.
	cmd.Env = supervisorEnvironment()
	cmd.ExtraFiles = []*os.File{supInR, supOutW, supStdinR, supStdoutW, supStderrW}
	if err := cmd.Start(); err != nil {
		closeAll(supInR, hostInW, hostOutR, supOutW, supStdinR, hostStdinW, hostStdoutR, supStdoutW, hostStderrR, supStderrW)
		return nil, fmt.Errorf("start supervisor %q: %w", argv[0], err)
	}
	// The supervisor owns the other ends from here on. Keeping any of them
	// would hide EOF from both sides and let a dead managed process look alive.
	closeAll(supInR, supOutW, supStdinR, supStdoutW, supStderrW)

	sup := &Supervisor{
		cmd:        cmd,
		in:         hostInW,
		out:        hostOutR,
		stdin:      hostStdinW,
		stdout:     hostStdoutR,
		stderr:     hostStderrR,
		runID:      runID,
		receiptDir: strings.TrimSpace(cfg.ReceiptDir),
		killGrace:  killGrace,
		handshake:  make(chan Message, 1),
		readDone:   make(chan struct{}),
		waited:     make(chan struct{}),
	}
	sup.report.RunID = runID
	go sup.readLoop(bufio.NewReader(hostOutR))
	if err := sup.bind(handshakeTimeout, cfg); err != nil {
		proven := sup.discard()
		// A supervisor that rejected the launch writes a receipt before it
		// exits. The run id is freshly generated for this launch, so a matching
		// receipt describes exactly this attempt and can supply the proof that
		// the unbound host end could not observe.
		if !proven {
			if receipt, loadErr := LoadReceipt(sup.receiptDir, sup.runID); loadErr == nil {
				proven = receipt.Cleanup.Proven
			}
		}
		return nil, &StartError{Err: err, CleanupProven: proven}
	}
	return sup, nil
}

// bind runs the startup handshake: Chord asks for one run under a fresh
// identity and the supervisor answers with the process group it bound.
func (s *Supervisor) bind(timeout time.Duration, cfg Config) error {
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	request := Message{
		Type:        TypeStart,
		RunID:       s.runID,
		Command:     cfg.Child.Command,
		Args:        slices.Clone(cfg.Child.Args),
		Env:         slices.Clone(cfg.Child.Env),
		Dir:         cfg.Child.Dir,
		ReceiptDir:  s.receiptDir,
		KillGraceMS: s.killGrace.Milliseconds(),
	}
	if err := s.write(request, deadline); err != nil {
		return fmt.Errorf("send supervisor start request: %w", err)
	}
	select {
	case message := <-s.handshake:
		if message.Type == TypeError {
			return fmt.Errorf("supervisor rejected the managed process: %s", message.Error)
		}
		if message.Type != TypeStarted {
			return fmt.Errorf("unexpected supervisor message before the handshake: %s", message.Type)
		}
		if message.RunID != s.runID {
			return fmt.Errorf("supervisor handshake answered a different run: %s", message.RunID)
		}
		if message.PID <= 0 || message.PGID <= 0 {
			return fmt.Errorf("supervisor handshake without a process identity")
		}
		s.mu.Lock()
		s.report.PID, s.report.PGID = message.PID, message.PGID
		s.report.StartToken = message.StartToken
		s.startToken = message.StartToken
		s.mu.Unlock()
		return nil
	case <-s.readDone:
		s.mu.Lock()
		reason := s.readErr
		s.mu.Unlock()
		if reason == nil {
			reason = io.EOF
		}
		return fmt.Errorf("supervisor exited before the handshake: %w", reason)
	case <-timer.C:
		return fmt.Errorf("supervisor handshake timed out after %s", timeout)
	}
}

// discard tears a supervisor down after a failed handshake. The run was never
// bound, but it may already have started a process: ask for the teardown
// before the supervisor is killed, so nothing it launched is left behind.
func (s *Supervisor) discard() bool {
	_ = s.write(Message{Type: TypeStop, RunID: s.runID}, time.Now().Add(s.killGrace))
	if !s.awaitExit(s.killGrace) {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		s.awaitExit(s.killGrace)
	}
	_ = s.Wait()
	if !s.GroupGone() {
		s.cleanupByIdentity()
	}
	closeAll(s.stdin, s.stdout, s.stderr)
	return s.GroupGone()
}

// Stdin is the host end of the managed process's stdin.
func (s *Supervisor) Stdin() io.WriteCloser { return s.stdin }

// Stdout is the host end of the managed process's stdout.
func (s *Supervisor) Stdout() io.ReadCloser { return s.stdout }

// Stderr is the host end of the managed process's stderr.
func (s *Supervisor) Stderr() io.ReadCloser { return s.stderr }

// RunID is the random identity Chord bound to this run before it started.
func (s *Supervisor) RunID() string { return s.runID }

// Identity is the process group the supervisor bound. ok is false when the
// handshake never completed: there is then nothing Chord may signal.
func (s *Supervisor) Identity() (pid, pgid int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.report.PID, s.report.PGID, s.report.PID > 0 && s.report.PGID > 0
}

// Report is the supervisor's latest account of the run.
func (s *Supervisor) Report() Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.report
}

// GroupGone reports whether the bound process group is confirmed empty. An
// unbound identity is never reported as gone: that would let Chord treat an
// unknown process as cleaned up.
func (s *Supervisor) GroupGone() bool {
	pid, pgid, ok := s.Identity()
	if !ok {
		return false
	}
	s.mu.Lock()
	startToken := s.startToken
	s.mu.Unlock()
	return probeGroup(pid, pgid, startToken) == GroupGone
}

// KillGroup tears the bound process group down from Chord's side, for the case
// where the supervisor is gone but the managed process is not. It signals only
// an identity that still matches, so a recycled pid never takes the hit.
func (s *Supervisor) KillGroup() (bool, error) {
	pid, pgid, ok := s.Identity()
	if !ok {
		return false, fmt.Errorf("supervised process identity is unknown: %w", ErrIdentityMismatch)
	}
	s.mu.Lock()
	startToken := s.startToken
	s.mu.Unlock()
	cleanup, err := terminateGroup(pid, pgid, startToken, s.killGrace)
	s.mu.Lock()
	s.report.Cleanup = cleanup
	s.mu.Unlock()
	return cleanup.Proven, err
}

// Stop asks the supervisor to end the run and waits for its cleanup report.
// It is safe to call more than once and from any goroutine.
//
// The supervisor is a helper, not a dependency: whatever it did or failed to
// do, Chord ends the run by checking the process group it bound at handshake
// and cleaning it up itself when the supervisor did not. A supervisor that
// never answers within grace is retired the same way.
func (s *Supervisor) Stop(grace time.Duration) error {
	s.stopOnce.Do(func() { s.stopErr = s.stop(grace) })
	return s.stopErr
}

func (s *Supervisor) stop(grace time.Duration) error {
	if grace <= 0 {
		grace = s.killGrace
	}
	// A supervisor that already died cannot read this, and the write error is
	// not a failure: the fallback below is what covers that case.
	_ = s.write(Message{Type: TypeStop, RunID: s.runID, GraceMS: grace.Milliseconds()}, time.Now().Add(s.killGrace))
	// The supervisor spends `grace` waiting for the managed process to leave on
	// its own and then escalates on its own budget — SIGTERM, killGrace,
	// SIGKILL, killGrace — before it reaps the child and answers. Waiting only
	// `grace` here would retire it in the middle of that escalation: Chord's
	// identity-bound cleanup would still end the group, but the supervisor's
	// report, and the receipt that exists for a Chord that is no longer around,
	// would never be written.
	budget := grace + 3*s.killGrace
	if !s.awaitExit(budget) {
		// A run the supervisor already accounted for has nothing left to
		// force: the group is gone and the report is in, so the supervisor is
		// only taking its time to exit. Killing it there would turn a clean
		// run into an error.
		if !s.settled() || !s.awaitExit(s.killGrace) {
			s.cleanupByIdentity()
			s.retire()
		}
	}
	err := s.Wait()
	if s.settled() {
		// How the supervisor's own process ended is not part of the outcome
		// Chord reports: the run was accounted for and the group is gone.
		err = nil
	}
	if !s.GroupGone() {
		s.cleanupByIdentity()
	}
	return err
}

// settled reports a run the supervisor accounted for with a proven teardown.
// Nothing about such a run is unknown, so Chord never has to force anything.
func (s *Supervisor) settled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.report.Reported && s.report.Cleanup.Proven
}

// cleanupByIdentity tears the bound process group down from Chord's side. It
// is what makes a dead supervisor survivable instead of fatal.
func (s *Supervisor) cleanupByIdentity() {
	proven, err := s.KillGroup()
	if err != nil {
		s.record(fmt.Errorf("clean up supervised process group: %w", err))
		return
	}
	if !proven {
		s.record(fmt.Errorf("supervised process group termination is not proven"))
	}
}

// retire ends a supervisor that stopped answering: it can no longer prove
// anything about the run, so Chord stops waiting for it.
func (s *Supervisor) retire() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	// The supervisor is dead, so nothing can arrive — but a process it spawned
	// may still hold the write end of the channel open. Closing our ends
	// releases the reader instead of leaving it waiting on a corpse.
	closeAll(s.in, s.out)
	s.awaitExit(s.killGrace)
}

// Wait blocks until the supervisor exits and the lifecycle channel is drained,
// then reports whether the supervisor accounted for the run.
func (s *Supervisor) Wait() error {
	s.waitOnce.Do(func() {
		var waitErr error
		processExited := make(chan error, 1)
		go func() { processExited <- s.cmd.Wait() }()
		select {
		case <-s.readDone:
			waitErr = <-processExited
		case waitErr = <-processExited:
			// The supervisor is gone, so nothing can arrive. Its reader may
			// still be stuck on the write end of a process the supervisor left
			// behind, and a corpse must not pin the teardown: give the reader a
			// bounded grace period and then release it.
			drain := time.NewTimer(readDrainGrace)
			defer drain.Stop()
			select {
			case <-s.readDone:
			case <-drain.C:
				closeAll(s.in, s.out)
			}
		}
		s.waitErr = waitErr
		closeAll(s.in, s.out)
		s.mu.Lock()
		reported := s.report.Reported
		readErr := s.readErr
		s.mu.Unlock()
		if s.waitErr == nil && !reported {
			if errors.Is(readErr, io.EOF) {
				s.waitErr = fmt.Errorf("supervisor exited without reporting the managed process")
			} else {
				s.waitErr = fmt.Errorf("supervisor exited without reporting the managed process: %w", readErr)
			}
		}
		close(s.waited)
	})
	<-s.waited
	return s.waitErr
}

func (s *Supervisor) awaitExit(timeout time.Duration) bool {
	go func() { _ = s.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.waited:
		return true
	case <-timer.C:
		return false
	}
}

func (s *Supervisor) readLoop(reader *bufio.Reader) {
	defer close(s.readDone)
	for {
		message, err := readMessage(reader)
		if err != nil {
			s.mu.Lock()
			if s.readErr == nil {
				s.readErr = err
			}
			s.mu.Unlock()
			// EOF means the supervisor is gone; a waiter still has to learn
			// that the run will never be bound.
			s.notify(Message{Type: TypeError, Error: err.Error()})
			return
		}
		s.apply(message)
	}
}

func (s *Supervisor) apply(message Message) {
	s.mu.Lock()
	switch message.Type {
	case TypeStarted:
		s.report.RunID = message.RunID
		s.report.PID = message.PID
		s.report.PGID = message.PGID
		s.report.StartToken = message.StartToken
		s.startToken = message.StartToken
	case TypeExited:
		s.report.Reason = message.Reason
		s.report.ExitCode = message.ExitCode
		s.report.ExitSignal = message.ExitSignal
		if message.Cleanup != nil {
			s.report.Cleanup = *message.Cleanup
		}
		s.report.Reported = true
	}
	s.mu.Unlock()
	s.notify(message)
}

// notify wakes a handshake waiter at most once; later messages only update the
// report the caller reads after Wait.
func (s *Supervisor) notify(message Message) {
	select {
	case s.handshake <- message:
	default:
	}
}

func (s *Supervisor) write(message Message, deadline time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.in == nil {
		return errors.New("lifecycle channel is closed")
	}
	return writeLifecycleMessage(s.in, message, deadline)
}

func (s *Supervisor) record(err error) {
	s.mu.Lock()
	if s.readErr == nil {
		s.readErr = err
	}
	s.mu.Unlock()
}

// NewRunID returns the identity Chord persists before a launch. The supervisor
// names the receipt after it, so recovery never has to guess from time,
// working directory or command arguments.
func NewRunID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate supervisor run id: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}

// ValidRunID reports whether id has the shape NewRunID produces. Receipt paths
// are built from this value, so anything else is refused instead of becoming a
// path component.
func ValidRunID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func resolveRunID(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return NewRunID()
	}
	if !ValidRunID(requested) {
		return "", fmt.Errorf("invalid supervisor run id %q", requested)
	}
	return requested, nil
}

// LoadReceipt reads the receipt of exactly runID. It validates the stored
// version and identity: a receipt that does not describe this run must never
// prove its process ended.
func LoadReceipt(dir, runID string) (Receipt, error) {
	if !ValidRunID(runID) {
		return Receipt{}, fmt.Errorf("invalid supervisor run id %q", runID)
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return Receipt{}, errors.New("supervisor receipt directory is empty")
	}
	data, err := os.ReadFile(filepath.Join(dir, runID+".json"))
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return Receipt{}, fmt.Errorf("decode supervisor receipt: %w", err)
	}
	if receipt.Version != receiptVersion {
		return Receipt{}, fmt.Errorf("unsupported supervisor receipt version %d", receipt.Version)
	}
	if receipt.RunID != runID {
		return Receipt{}, fmt.Errorf("supervisor receipt belongs to run %q", receipt.RunID)
	}
	if receipt.Cleanup.Proven && receipt.ProcessStarted {
		if receipt.PID <= 0 || receipt.PGID <= 0 {
			return Receipt{}, errors.New("supervisor receipt claims proven cleanup without a bound process identity")
		}
		if receipt.StartToken == "" {
			return Receipt{}, errors.New("supervisor receipt claims proven cleanup without a process start token")
		}
	}
	return receipt, nil
}

// PruneReceipts removes receipts that finished before cutoff. Receipts are
// written for every run and never deleted per-run, so a crash after spawn but
// before Chord persists the exit can still be proven; pruning is what bounds
// the directory. Malformed files are left for diagnosis.
func PruneReceipts(dir string, cutoff time.Time) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read supervisor receipts: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var receipt Receipt
		if json.Unmarshal(data, &receipt) != nil || !receipt.FinishedAt.Before(cutoff) {
			continue
		}
		_ = os.Remove(path)
	}
	return nil
}

func supervisorCommand(command []string) ([]string, error) {
	if len(command) == 0 {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate chord executable: %w", err)
		}
		return []string{executable, CommandName}, nil
	}
	return slices.Clone(command), nil
}

// resolveChildCommand pins the managed command to a concrete path so the
// supervisor — which inherits almost no environment — execs the same program
// Chord validated.
func resolveChildCommand(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("supervised command is required")
	}
	if strings.ContainsRune(command, os.PathSeparator) {
		return command, nil
	}
	if resolved, err := exec.LookPath(command); err == nil {
		return resolved, nil
	}
	return command, nil
}

// supervisorEnvironment is deliberately minimal: the supervisor needs no
// credentials, no configuration and no terminal, and a leaked environment
// would only widen what a compromised managed process could reach through it.
// GOCOVERDIR is the one exception: it is set only while a build is being
// measured, and dropping it would hide the supervisor's own coverage.
func supervisorEnvironment() []string {
	env := make([]string, 0, 4)
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "GOCOVERDIR"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func closeAll(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}
