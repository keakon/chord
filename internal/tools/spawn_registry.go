package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/shell"
)

const maxSpawnedProcesses = 50

type spawnKind string

const (
	spawnKindService spawnKind = "service"
	spawnKindJob     spawnKind = "job"
)

type spawnedProcessStartRequest struct {
	Kind             spawnKind
	Command          string
	Description      string
	Workdir          string
	TimeoutInfo      BashTimeoutInfo
	ShellType        string
	LogDir           string
	ExposeLogToModel bool
}

type spawnedProcess struct {
	ID               string
	Kind             spawnKind
	AgentID          string
	Command          string
	Description      string
	LogFile          string
	ExposeLogToModel bool
	StartedAt        time.Time
	MaxRuntimeSec    int
	exitErr          error
	cmdMu            sync.Mutex
	cmd              *exec.Cmd
	cancelCh         chan string
	startedCh        chan struct{}
	logHandle        *os.File
	done             chan struct{}
}

type SpawnRegistry struct {
	mu        sync.Mutex
	processes map[string]*spawnedProcess
	seq       atomic.Uint64
}

var globalSpawnRegistry = &SpawnRegistry{processes: make(map[string]*spawnedProcess)}

func (r *SpawnRegistry) start(ctx context.Context, req spawnedProcessStartRequest) (*spawnedProcess, error) {
	r.mu.Lock()
	if len(r.processes) >= maxSpawnedProcesses {
		r.mu.Unlock()
		return nil, fmt.Errorf("maximum number of spawned processes (%d) reached", maxSpawnedProcesses)
	}
	prefix := "job"
	if req.Kind == spawnKindService {
		prefix = "svc"
	}
	id := fmt.Sprintf("%s-%d", prefix, r.seq.Add(1))
	proc := &spawnedProcess{
		ID:               id,
		Kind:             req.Kind,
		AgentID:          AgentIDFromContext(ctx),
		Command:          req.Command,
		Description:      req.Description,
		ExposeLogToModel: req.ExposeLogToModel,
		StartedAt:        time.Now(),
		cancelCh:         make(chan string, 1),
		startedCh:        make(chan struct{}),
		done:             make(chan struct{}),
	}
	if req.TimeoutInfo.HasLimit {
		proc.MaxRuntimeSec = req.TimeoutInfo.EffectiveSec
	}
	st := shell.ParseShellType(req.ShellType)
	binary, args := shell.GetShellCommand(st, req.Command)
	cmd := exec.Command(binary, args...)
	_, _ = configureCommandProcessGroup(cmd)
	if req.Workdir != "" {
		cmd.Dir = req.Workdir
	}
	if req.LogDir != "" {
		logPath := filepath.Join(req.LogDir, id+".log")
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			r.mu.Unlock()
			return nil, fmt.Errorf("creating log dir: %w", err)
		}
		f, err := openFileNoFollow(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			r.mu.Unlock()
			return nil, fmt.Errorf("creating log file: %w", err)
		}
		proc.LogFile = logPath
		proc.logHandle = f
		cmd.Stdout = f
		cmd.Stderr = f
	}
	proc.cmdMu.Lock()
	proc.cmd = cmd
	proc.cmdMu.Unlock()
	r.processes[id] = proc
	r.mu.Unlock()

	go r.run(ctx, proc)
	log.Debugf("spawned process started id=%v kind=%v command=%v max_runtime_sec=%v agent_id=%v", id, req.Kind, req.Command, proc.MaxRuntimeSec, proc.AgentID)
	return proc, nil
}

func (r *SpawnRegistry) run(ctx context.Context, proc *spawnedProcess) {
	defer func() {
		if proc.logHandle != nil {
			_ = proc.logHandle.Close()
		}
		close(proc.done)
		r.mu.Lock()
		delete(r.processes, proc.ID)
		r.mu.Unlock()
		if sender := EventSenderFromContext(ctx); sender != nil {
			status := "finished (exit 0)"
			if proc.exitErr != nil {
				status = fmt.Sprintf("finished (error: %v)", proc.exitErr)
			}
			msg := fmt.Sprintf("[%s %s finished: %s]\n\nDescription: %s", displaySpawnKind(proc.Kind), proc.ID, status, proc.Description)
			sender.SendAgentEvent(
				"background_object_finished",
				proc.AgentID,
				&SpawnFinishedPayload{
					BackgroundID:  proc.ID,
					AgentID:       proc.AgentID,
					Kind:          string(proc.Kind),
					Status:        status,
					Command:       proc.Command,
					Description:   proc.Description,
					MaxRuntimeSec: proc.MaxRuntimeSec,
					Message:       msg,
					LogFile:       proc.LogFile,
				},
			)
		}
	}()

	proc.cmdMu.Lock()
	cmd := proc.cmd
	proc.cmdMu.Unlock()
	if cmd == nil {
		proc.exitErr = fmt.Errorf("starting command: missing process handle")
		return
	}
	if err := cmd.Start(); err != nil {
		proc.exitErr = fmt.Errorf("starting command: %w", err)
		return
	}
	close(proc.startedCh)

	waitCh := waitForCommand(cmd, proc.done)
	if proc.MaxRuntimeSec > 0 {
		timer := time.NewTimer(time.Duration(proc.MaxRuntimeSec) * time.Second)
		defer timer.Stop()
		select {
		case reason := <-proc.cancelCh:
			proc.exitErr = terminateSpawnProcessGroup(cmd, reason, waitCh)
		case err := <-waitCh:
			proc.exitErr = err
		case <-timer.C:
			proc.exitErr = terminateSpawnProcessGroup(cmd, fmt.Sprintf("timed out after %ds", proc.MaxRuntimeSec), waitCh)
		}
		return
	}

	select {
	case reason := <-proc.cancelCh:
		proc.exitErr = terminateSpawnProcessGroup(cmd, reason, waitCh)
	case err := <-waitCh:
		proc.exitErr = err
	}
}

func waitForCommand(cmd *exec.Cmd, done <-chan struct{}) <-chan error {
	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		select {
		case waitCh <- err:
		case <-done:
		}
	}()
	return waitCh
}

func (r *SpawnRegistry) cancel(id string, reason string) bool {
	r.mu.Lock()
	proc, ok := r.processes[id]
	if ok {
		delete(r.processes, id)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}

	select {
	case proc.cancelCh <- reason:
	default:
	}

	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
	}
	return true
}

func (r *SpawnRegistry) getState(id string) (SpawnState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	proc, ok := r.processes[id]
	if !ok {
		return SpawnState{}, false
	}
	return SpawnState{
		ID:            proc.ID,
		AgentID:       proc.AgentID,
		Kind:          string(proc.Kind),
		Description:   proc.Description,
		Command:       proc.Command,
		LogFile:       proc.LogFile,
		StartedAt:     proc.StartedAt,
		MaxRuntimeSec: proc.MaxRuntimeSec,
		Status:        "running",
	}, true
}

type SpawnState struct {
	ID            string
	AgentID       string
	Kind          string
	Description   string
	Command       string
	LogFile       string
	StartedAt     time.Time
	MaxRuntimeSec int
	Status        string
	FinishedAt    time.Time
}

func (r *SpawnRegistry) snapshotStates() []SpawnState {
	r.mu.Lock()
	defer r.mu.Unlock()
	states := make([]SpawnState, 0, len(r.processes))
	for _, proc := range r.processes {
		states = append(states, SpawnState{
			ID:            proc.ID,
			AgentID:       proc.AgentID,
			Kind:          string(proc.Kind),
			Description:   proc.Description,
			Command:       proc.Command,
			LogFile:       proc.LogFile,
			StartedAt:     proc.StartedAt,
			MaxRuntimeSec: proc.MaxRuntimeSec,
			Status:        "running",
		})
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].StartedAt.Equal(states[j].StartedAt) {
			return states[i].ID < states[j].ID
		}
		return states[i].StartedAt.Before(states[j].StartedAt)
	})
	return states
}

func SnapshotSpawnedProcesses() []SpawnState {
	if globalSpawnRegistry == nil {
		return nil
	}
	return globalSpawnRegistry.snapshotStates()
}

func StopAllSpawnedForAgent(agentID string, reason string) int {
	if globalSpawnRegistry == nil {
		return 0
	}
	return globalSpawnRegistry.stopForAgent(agentID, reason)
}

func StopAllSpawnedForSessionSwitch() int {
	if globalSpawnRegistry == nil {
		return 0
	}
	return globalSpawnRegistry.stopAll("terminated on session switch")
}

func StopAllSpawnedForShutdown() int {
	if globalSpawnRegistry == nil {
		return 0
	}
	return globalSpawnRegistry.stopAll("terminated on client exit")
}

func CleanupSpawnLogs(sessionDir string) error {
	logDir := sessionSpawnLogsDir(sessionDir)
	if logDir == "" {
		return nil
	}
	if err := os.RemoveAll(logDir); err != nil {
		return fmt.Errorf("remove spawn logs: %w", err)
	}
	return nil
}

func (r *SpawnRegistry) stopForAgent(agentID string, reason string) int {
	r.mu.Lock()
	ids := make([]string, 0)
	for id, proc := range r.processes {
		if proc.AgentID == agentID {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.cancel(id, reason)
	}
	return len(ids)
}

func (r *SpawnRegistry) stopAll(reason string) int {
	r.mu.Lock()
	ids := make([]string, 0, len(r.processes))
	for id := range r.processes {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.cancel(id, reason)
	}
	return len(ids)
}

func terminateSpawnProcessGroup(cmd *exec.Cmd, reason string, doneCh <-chan error) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("command %s", reason)
	}
	pid := cmd.Process.Pid
	_ = pid
	_ = terminateCommandProcessGroup(cmd)
	select {
	case err := <-doneCh:
		if err != nil {
			return fmt.Errorf("command %s: %w", reason, err)
		}
		return fmt.Errorf("command %s", reason)
	case <-time.After(killGracePeriod):
		_ = terminateCommandProcessGroup(cmd)
		if err := <-doneCh; err != nil {
			return fmt.Errorf("command %s: %w", reason, err)
		}
		return fmt.Errorf("command %s", reason)
	}
}

func displaySpawnKind(kind spawnKind) string {
	switch kind {
	case spawnKindService:
		return "Service"
	case spawnKindJob:
		return "Job"
	default:
		if kind == "" {
			return "Background"
		}
		return string(kind)
	}
}

func ExecuteSpawnForTest(ctx context.Context, kind string, command, description string, timeout *int) (string, error) {
	return ExecuteSpawnForTestWithShell(ctx, kind, command, description, timeout, "")
}

func ExecuteSpawnForTestWithShell(ctx context.Context, kind string, command, description string, timeout *int, shellType string) (string, error) {
	spawnKindValue := spawnKind(kind)
	if spawnKindValue != spawnKindService && spawnKindValue != spawnKindJob {
		return "", fmt.Errorf("invalid spawn kind %q", kind)
	}
	proc, err := globalSpawnRegistry.start(ctx, spawnedProcessStartRequest{
		Kind:             spawnKindValue,
		Command:          command,
		Description:      description,
		TimeoutInfo:      ResolveSpawnTimeout(timeout),
		ShellType:        shellType,
		ExposeLogToModel: spawnKindValue == spawnKindService,
	})
	if err != nil {
		return "", err
	}
	return proc.ID, nil
}

func ResetSpawnRegistryForTest() func() {
	old := globalSpawnRegistry
	globalSpawnRegistry = &SpawnRegistry{processes: make(map[string]*spawnedProcess)}
	return func() {
		globalSpawnRegistry = old
	}
}
