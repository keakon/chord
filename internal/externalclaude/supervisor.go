package externalclaude

import (
	"fmt"
	"os"
	"strings"

	"github.com/keakon/chord/internal/externalsupervisor"
)

// startSupervised hands the process to an independent supervisor: Chord keeps
// the business pipes and the lifecycle channel, the supervisor starts the
// process and owns its process group. That split is the whole point — a
// supervisor outlives Chord, so a worker's process group can still be torn
// down after Chord is killed instead of being left behind.
//
// It fails closed: until the supervisor answers the handshake with a process
// identity, the worker must not be treated as runnable.
func startSupervised(cfg ProcessConfig) (*Session, error) {
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		command = "claude"
	}
	if err := ValidateEnvironment(cfg.Env); err != nil {
		return nil, err
	}
	sup, err := externalsupervisor.Start(externalsupervisor.Config{
		Command: cfg.Supervisor.Command,
		Child: externalsupervisor.ChildSpec{
			Command: command,
			Args:    cfg.Args,
			Env:     processEnvironment(os.Environ(), cfg.Env),
			Dir:     cfg.Dir,
		},
		RunID:      strings.TrimSpace(cfg.Supervisor.RunID),
		ReceiptDir: strings.TrimSpace(cfg.Supervisor.ReceiptDir),
		KillGrace:  cfg.KillGrace,
	})
	if err != nil {
		return nil, fmt.Errorf("start external supervisor: %w", err)
	}
	killGrace := cfg.KillGrace
	if killGrace <= 0 {
		killGrace = defaultKillGrace
	}
	maxLineBytes := cfg.MaxLineBytes
	if maxLineBytes <= 0 {
		maxLineBytes = defaultMaxLineBytes
	}
	session := &Session{
		stdin:       sup.Stdin(),
		stdout:      sup.Stdout(),
		stderr:      sup.Stderr(),
		sup:         sup,
		events:      make(chan Event, 64),
		done:        make(chan struct{}),
		stop:        make(chan struct{}),
		exited:      make(chan struct{}),
		cleanupDone: make(chan struct{}),
		killGrace:   killGrace,
		onStderr:    cfg.OnStderr,
	}
	session.scanWG.Add(2)
	go session.scanStdout(session.stdout, maxLineBytes)
	go session.scanStderr(session.stderr)
	go session.wait()
	return session, nil
}

// waitSupervised ends a supervised run. Business output is drained first —
// until both pipes are at EOF the run's output is not complete — and only then
// does Chord ask the supervisor for the teardown it exists for.
func (s *Session) waitSupervised() {
	s.scanWG.Wait()
	// Business output at EOF is the run's exit signal: the managed process and
	// everything that inherited its pipes are done writing.
	close(s.exited)
	// Stop is idempotent and covers both directions of the split: it asks the
	// supervisor to tear the group down, and when the supervisor cannot — it is
	// gone, or it stopped answering — Chord cleans up by the identity it bound
	// at handshake instead of leaving the group behind. It also waits for the
	// supervisor's own exit, so the report read afterwards is complete.
	stopErr := s.sup.Stop(s.killGrace)
	report := s.sup.Report()
	if report.Reported {
		switch {
		case report.ExitSignal != 0:
			s.recordReadError(fmt.Errorf("external process was terminated by signal %d", report.ExitSignal))
		case report.ExitCode != 0:
			s.recordReadError(fmt.Errorf("external process exited with code %d", report.ExitCode))
		}
		if !report.Cleanup.Proven {
			s.recordReadError(fmt.Errorf("external process group termination is not proven"))
		}
	}
	// The run id ties a failure here to the receipt the supervisor may have
	// left behind.
	if stopErr != nil {
		s.recordReadError(fmt.Errorf("external supervisor run %s: %w", s.sup.RunID(), stopErr))
	}
	if !s.sup.GroupGone() {
		s.recordReadError(fmt.Errorf("external process group termination is not proven (run %s)", s.sup.RunID()))
	}
	s.errMu.Lock()
	if s.waitErr == nil {
		s.waitErr = s.readErr
	}
	s.errMu.Unlock()
	close(s.events)
	close(s.done)
}

// terminateSupervised is the forceful teardown of a supervised run: it closes
// input, asks the supervisor to end the run, and falls back to the bound
// process-group identity when the supervisor cannot answer.
func (s *Session) terminateSupervised() {
	go func() {
		if err := s.sup.Stop(s.killGrace); err != nil {
			s.recordReadError(fmt.Errorf("external supervisor run %s: %w", s.sup.RunID(), err))
		}
		if !s.sup.GroupGone() {
			s.recordReadError(fmt.Errorf("external process group termination is not proven (run %s)", s.sup.RunID()))
		}
		_ = s.stdout.Close()
		_ = s.stderr.Close()
	}()
}
