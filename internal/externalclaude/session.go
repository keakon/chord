package externalclaude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/externalsupervisor"
)

const (
	defaultMaxLineBytes = 16 << 20
	defaultKillGrace    = 5 * time.Second
)

// ProcessConfig describes one Claude Code process.
type ProcessConfig struct {
	Command string
	Args    []string
	// Env holds already-expanded KEY=VALUE entries appended to the current
	// process environment.
	Env map[string]string
	Dir string
	// OnStderr receives complete stderr lines, including the stdout guard's
	// [stdout-guard] passthrough. It may be nil.
	OnStderr func(line string)
	// MaxLineBytes bounds one stdout JSON line. Zero uses 16 MiB.
	MaxLineBytes int
	KillGrace    time.Duration
	// MCPHandler serves SDK MCP requests sent by Claude Code over the control
	// channel. Nil rejects such requests without blocking initialization.
	MCPHandler MCPHandler
	// Supervisor runs the process under an independent lifecycle supervisor.
	// Nil keeps it in Chord's own process table, which cannot clean it up once
	// Chord is killed.
	Supervisor *SupervisorConfig
}

// SupervisorConfig opts a managed process into its own supervisor process. The
// supervisor starts the process on Chord's behalf and is the only reason the
// process group can still be torn down after Chord disappears.
type SupervisorConfig struct {
	// Command starts the supervisor. Empty resolves to the running Chord
	// executable, so a supervisor is always the build that spawned it.
	Command []string
	// RunID is the identity this launch was persisted under before it started.
	// The supervisor names the receipt after it, so recovery looks up exactly
	// this run instead of matching receipts by time and arguments. Empty
	// generates a fresh ID.
	RunID string
	// ReceiptDir is where the supervisor leaves a lifecycle receipt for the
	// run. Empty disables the receipt.
	ReceiptDir string
}

// StartError preserves the cleanup evidence of a launch that failed. A failed
// launch is only safe to treat as ended when CleanupProven is true.
type StartError struct {
	Err           error
	CleanupProven bool
}

func (e *StartError) Error() string { return e.Err.Error() }

func (e *StartError) Unwrap() error { return e.Err }

// StartFailureCleanupProven reports whether a failed Start proved that no
// managed process group survived it.
func StartFailureCleanupProven(err error) bool {
	var startErr *StartError
	return errors.As(err, &startErr) && startErr.CleanupProven
}

// Session owns one running Claude Code process and its stdout event stream.
type Session struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdinMu sync.Mutex
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	// sup owns the process when it runs under an independent supervisor; the
	// Session then keeps only the business pipes and the lifecycle channel.
	sup *externalsupervisor.Supervisor

	events   chan Event
	done     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	scanWG   sync.WaitGroup
	waitErr  error
	exited   chan struct{}
	errMu    sync.Mutex
	readErr  error

	terminateOnce sync.Once
	gracefulOnce  sync.Once
	cleanupDone   chan struct{}
	killGrace     time.Duration
	onStderr      func(string)
	mcpHandler    MCPHandler
}

// Start launches the process and begins streaming events. The caller must
// consume Events until it closes.
func Start(cfg ProcessConfig) (*Session, error) {
	if cfg.Supervisor != nil {
		return startSupervised(cfg)
	}
	session, err := startDirect(cfg)
	if err != nil {
		return nil, &StartError{Err: err, CleanupProven: true}
	}
	return session, nil
}

// startDirect keeps the process in Chord's own process table. Nothing can then
// clean it up after Chord is killed, so production workers use startSupervised.
func startDirect(cfg ProcessConfig) (*Session, error) {
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		command = "claude"
	}
	cmd := exec.Command(command, cfg.Args...)
	cmd.Dir = cfg.Dir
	if err := ValidateEnvironment(cfg.Env); err != nil {
		return nil, err
	}
	cmd.Env = processEnvironment(os.Environ(), cfg.Env)
	if err := configureProcessGroup(cmd); err != nil {
		return nil, err
	}
	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("claude stdout pipe: %w", err)
	}
	stderr, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("claude stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutW.Close()
		_ = stderr.Close()
		_ = stderrW.Close()
		return nil, fmt.Errorf("claude stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutW.Close()
		_ = stderr.Close()
		_ = stderrW.Close()
		return nil, fmt.Errorf("start %s: %w", command, err)
	}
	// The child inherited the write ends; close the parent copies so the read
	// ends see EOF when the process exits. cmd.Wait must not own these pipes:
	// it closes its own pipes as soon as the process exits, which can discard
	// stdout that a scanner has not read yet.
	_ = stdoutW.Close()
	_ = stderrW.Close()
	killGrace := cfg.KillGrace
	if killGrace <= 0 {
		killGrace = defaultKillGrace
	}
	maxLineBytes := cfg.MaxLineBytes
	if maxLineBytes <= 0 {
		maxLineBytes = defaultMaxLineBytes
	}
	session := &Session{
		cmd:         cmd,
		stdin:       stdin,
		stdout:      stdout,
		stderr:      stderr,
		events:      make(chan Event, 64),
		done:        make(chan struct{}),
		stop:        make(chan struct{}),
		exited:      make(chan struct{}),
		cleanupDone: make(chan struct{}),
		killGrace:   killGrace,
		onStderr:    cfg.OnStderr,
		mcpHandler:  cfg.MCPHandler,
	}
	session.scanWG.Add(2)
	go session.scanStdout(stdout, maxLineBytes)
	go session.scanStderr(stderr)
	go session.wait()
	return session, nil
}

// Initialize waits for the correlated initialization acknowledgment before the
// caller may submit a user turn. System events received before the
// acknowledgment are returned to the caller instead of being dropped.
func (s *Session) Initialize(ctx context.Context, prompt string) ([]Event, error) {
	const requestID = "chord-initialize"
	if err := s.WriteFrameContext(ctx, InitializeRequest(requestID, prompt)); err != nil {
		return nil, err
	}
	var deferred []Event
	for {
		select {
		case event, ok := <-s.events:
			if !ok {
				return nil, fmt.Errorf("external process exited before initialization")
			}
			if event.Type == TypeSystem {
				deferred = append(deferred, event)
				continue
			}
			if event.Type != TypeControlResponse || event.Response == nil || event.Response.RequestID != requestID {
				return nil, fmt.Errorf("unexpected external event before initialization: %s", event.Type)
			}
			if event.Response.Subtype != ControlResponseSuccess {
				return nil, fmt.Errorf("external initialization failed")
			}
			return deferred, nil
		case <-ctx.Done():
			return nil, fmt.Errorf("external initialization: %w", ctx.Err())
		}
	}
}

// Events is closed when the process exits.
func (s *Session) Events() <-chan Event {
	return s.events
}

// Wait blocks until the process exits and returns its wait error.
func (s *Session) Wait() error {
	<-s.done
	return s.waitErr
}

// WriteFrame sends one control/user frame to stdin.
func (s *Session) WriteFrame(frame Frame) error {
	return s.WriteFrameContext(context.Background(), frame)
}

func (s *Session) WriteFrameContext(ctx context.Context, frame Frame) error {
	return s.writeJSON(ctx, frame)
}

// WriteUserMessage sends one user turn in persistent stream-json mode.
func (s *Session) WriteUserMessage(ctx context.Context, text string) error {
	frame := struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}{Type: "user"}
	frame.Message.Role = "user"
	frame.Message.Content = append(frame.Message.Content, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: text})
	return s.writeJSON(ctx, frame)
}

func (s *Session) writeJSON(ctx context.Context, value any) error {
	stop := context.AfterFunc(ctx, func() { _ = s.stdin.Close() })
	defer stop()
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode control frame: %w", err)
	}
	data = append(data, '\n')
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write control frame: %w", err)
	}
	if _, err := s.stdin.Write(data); err != nil {
		return fmt.Errorf("write control frame: %w", err)
	}
	return nil
}

// Interrupt asks the CLI to cancel the current turn through the control
// channel. A cancelled turn is not the same as a stopped process: the caller
// decides whether to resume, terminate or wait.
func (s *Session) Interrupt(requestID string) error {
	return s.WriteFrame(InterruptRequest(requestID))
}

// Terminate sends SIGTERM to the process group, then SIGKILL after the grace
// period if the group has not exited.
func (s *Session) Terminate() {
	s.stopScanning()
	s.terminateOnce.Do(func() {
		_ = s.stdin.Close()
		if s.sup != nil {
			s.terminateSupervised()
			return
		}
		_ = terminateGroup(s.cmd)
		go func() {
			defer close(s.cleanupDone)
			select {
			case <-s.exited:
			case <-time.After(s.killGrace):
			}
			if processGroupAlive(s.cmd) {
				_ = killGroup(s.cmd)
				deadline := time.NewTimer(s.killGrace)
				defer deadline.Stop()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
			waitForGroup:
				for processGroupAlive(s.cmd) {
					select {
					case <-ticker.C:
					case <-deadline.C:
						s.recordReadError(fmt.Errorf("external process group termination is not proven"))
						break waitForGroup
					}
				}
			}
			_ = s.stdout.Close()
			_ = s.stderr.Close()
		}()
	})
}

// Stop ends the session gracefully without blocking the caller: it closes
// stdin (so a persistent CLI sees EOF) and asks the CLI to abandon the current
// turn, then schedules SIGTERM (and SIGKILL after the grace period) for a
// process that stays alive past grace. Waiting for the process to exit on its
// own first lets the CLI flush the session transcript that a later --resume
// reads; callers that must observe the exit use Wait. The graceful step
// runs at most once and Stop is safe to call from any goroutine.
func (s *Session) Stop(grace time.Duration) {
	if s == nil {
		return
	}
	if grace <= 0 {
		grace = s.killGrace
	}
	s.gracefulOnce.Do(func() {
		// Closing a pipe does not need the write lock and releases a writer
		// blocked by CLI stdin backpressure.
		go func() {
			go func() { _ = s.Interrupt(stopRequestID()) }()
			select {
			case <-s.exited:
			case <-time.After(min(grace, 100*time.Millisecond)):
			}
			_ = s.stdin.Close()
			select {
			case <-s.done:
			case <-time.After(grace):
				s.Terminate()
			}
		}()
	})
}

// Finish closes input without interrupting a successfully completed turn.
func (s *Session) Finish(grace time.Duration) {
	_ = s.stdin.Close()
	go func() {
		select {
		case <-s.done:
		case <-time.After(grace):
			s.recordReadError(fmt.Errorf("external process did not exit after input closed"))
			s.Terminate()
		}
	}()
}

// stopRequestID names a host-initiated control request. The CLI only needs a
// stable id to correlate its response, not a UUID.
func stopRequestID() string {
	return fmt.Sprintf("chord-stop-%d", time.Now().UnixNano())
}

func (s *Session) wait() {
	if s.sup != nil {
		s.waitSupervised()
		return
	}
	s.waitErr = s.cmd.Wait()
	close(s.exited)
	_ = s.stdin.Close()
	if processGroupAlive(s.cmd) {
		s.recordReadError(fmt.Errorf("external child processes remained after CLI exit"))
		s.Terminate()
		<-s.cleanupDone
	}
	// The scanners own the read ends and finish on EOF or when stopScanning
	// unblocked a full channel; only then is the event stream complete.
	s.scanWG.Wait()
	s.errMu.Lock()
	if s.waitErr == nil {
		s.waitErr = s.readErr
	}
	s.errMu.Unlock()
	close(s.events)
	close(s.done)
}

// ProcessEnded reports whether the process group is known to be gone after Wait.
func (s *Session) ProcessEnded() bool {
	<-s.done
	if s.sup != nil {
		return s.sup.GroupGone()
	}
	return !processGroupAlive(s.cmd)
}

func (s *Session) TerminationProven() bool {
	if s == nil {
		return false
	}
	if s.sup != nil {
		return s.sup.GroupGone()
	}
	if s.cmd == nil || s.cmd.Process == nil {
		return false
	}
	return !processGroupAlive(s.cmd)
}

// ForceCleanup makes one more attempt to prove the process group is gone after
// an unproven teardown. It never reports success without that proof.
func (s *Session) ForceCleanup() bool {
	if s == nil {
		return false
	}
	if s.sup != nil {
		if s.sup.GroupGone() {
			return true
		}
		_, _ = s.sup.KillGroup()
		return s.sup.GroupGone()
	}
	if s.cmd == nil || s.cmd.Process == nil {
		return false
	}
	if !processGroupAlive(s.cmd) {
		return true
	}
	_ = killGroup(s.cmd)
	deadline := time.NewTimer(s.killGrace)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for processGroupAlive(s.cmd) {
		select {
		case <-ticker.C:
		case <-deadline.C:
			return !processGroupAlive(s.cmd)
		}
	}
	return true
}

func (s *Session) stopScanning() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *Session) scanStdout(reader io.Reader, maxLineBytes int) {
	defer s.scanWG.Done()
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		event, err := Decode(line)
		if err != nil {
			s.recordReadError(fmt.Errorf("invalid external protocol line: %w", err))
			s.Terminate()
			return
		}
		if event.Type == TypeControlRequest && event.Request != nil && event.Request.Subtype == ControlSubtypeMCPMessage {
			s.handleMCPControlRequest(event)
			continue
		}
		select {
		case s.events <- event:
		default:
			select {
			case s.events <- event:
			case <-s.stop:
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		s.recordReadError(fmt.Errorf("external stdout read: %w", err))
		s.Terminate()
	}
}

func (s *Session) handleMCPControlRequest(event Event) {
	response := json.RawMessage(nil)
	if s.mcpHandler != nil {
		response = s.mcpHandler(context.Background(), event.Request.ServerName, event.Request.Message)
	}
	if len(response) == 0 {
		response = json.RawMessage(`{"jsonrpc":"2.0","id":null,"error":{"code":-32601,"message":"MCP server is unavailable"}}`)
	}
	if err := s.WriteFrame(MCPMessageResponse(event.RequestID, response)); err != nil {
		s.recordReadError(fmt.Errorf("answer MCP control request: %w", err))
		s.Terminate()
	}
}

func (s *Session) recordReadError(err error) {
	s.errMu.Lock()
	if s.readErr == nil {
		s.readErr = err
	}
	s.errMu.Unlock()
}

func (s *Session) scanStderr(reader io.Reader) {
	defer s.scanWG.Done()
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 16<<10), 1<<20)
	for scanner.Scan() {
		s.reportStderr(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		s.recordReadError(fmt.Errorf("external stderr read: %w", err))
		s.Terminate()
	}
}

func (s *Session) reportStderr(line string) {
	if s.onStderr != nil {
		s.onStderr(line)
	}
}
