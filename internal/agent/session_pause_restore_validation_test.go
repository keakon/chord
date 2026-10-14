package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestPausedSessionRestoreValidatesDependenciesBeforeActivation(t *testing.T) {
	for _, paused := range []bool{true, false} {
		t.Run(map[bool]string{true: "paused", false: "pending_before_dispatch"}[paused], func(t *testing.T) {
			// Rejected restores must leave this live owner untouched. Keep one
			// owner for the matrix and give every invalid target an isolated path.
			a := newReadyTestMainAgent(t)
			installTakeoverTestLock(t, a)
			oldPath, oldLock, oldGate := a.SessionDir(), a.sessionLock, a.sessionExecutionGate()
			for _, consumer := range []string{"load", "startup", "takeover"} {
				for _, dependency := range []string{"main_role", "task_role", "main_role_mode", "task_role_mode", "metadata", "working_directory", "working_directory_file"} {
					t.Run(consumer+"/"+dependency, func(t *testing.T) {
						path, id := createPausedTakeoverTarget(t, a, consumer+"-"+dependency)
						a.agentConfigs = map[string]*config.AgentConfig{
							"main-sample":   {Name: "main-sample"},
							"worker-sample": {Name: "worker-sample", Mode: config.AgentModeSubAgent},
						}
						manager := recovery.NewRecoveryManager(path)
						snapshot, err := manager.Recover()
						if err != nil {
							t.Fatal(err)
						}
						snapshot.Pause.Paused = paused
						if !paused {
							if err := manager.PersistMessage(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "earlier instruction"}); err != nil {
								t.Fatal(err)
							}
						}
						switch dependency {
						case "main_role":
							snapshot.ActiveRole = "unavailable-sample"
						case "main_role_mode":
							snapshot.ActiveRole = "worker-sample"
						case "task_role", "task_role_mode":
							name := "unavailable-sample"
							if dependency == "task_role_mode" {
								name = "main-sample"
							}
							if err := persistDurableTaskRecords(path, map[string]*DurableTaskRecord{"sample-task": {TaskID: "sample-task", AgentDefName: name, State: string(SubAgentStateIdle), Attempt: 1}}); err != nil {
								t.Fatal(err)
							}
						case "metadata":
							if err := os.WriteFile(filepath.Join(path, "session-meta.json"), []byte("{"), 0600); err != nil {
								t.Fatal(err)
							}
						case "working_directory", "working_directory_file":
							workDir := filepath.Join(t.TempDir(), "sample-checkout")
							if dependency == "working_directory_file" {
								if err := os.WriteFile(workDir, []byte("sample"), 0600); err != nil {
									t.Fatal(err)
								}
							}
							if err := recovery.SaveSessionMeta(path, recovery.SessionMeta{WorktreePath: workDir}); err != nil {
								t.Fatal(err)
							}
						}
						if err := manager.SaveSnapshot(snapshot); err != nil {
							t.Fatal(err)
						}
						if err := manager.Close(); err != nil {
							t.Fatal(err)
						}
						switch consumer {
						case "load":
							_, err = a.loadSessionState(path)
						case "startup":
							a.sessionDir = path
							err = a.RestoreSessionAtStartup()
							a.sessionDir = oldPath
						case "takeover":
							reply := make(chan error, 1)
							a.handleTakeoverTarget(sessionTakeoverTarget{ctx: t.Context(), sessionID: id.SessionID, identity: id, reply: reply})
							err = <-reply
						}
						if err == nil || !strings.Contains(err.Error(), "restore_failed") {
							t.Fatalf("missing dependency did not reject restore: %v", err)
						}
						if a.SessionDir() != oldPath || a.sessionLock != oldLock || oldGate.Snapshot().Paused {
							t.Fatal("invalid checkpoint changed current ownership or execution")
						}
						if a.sessionExecutionPaused() || len(a.PendingSessionInputs()) != 0 {
							t.Fatal("invalid paused checkpoint was installed")
						}
						if consumer == "takeover" {
							lock, err := recovery.AcquireSessionLock(path)
							if err != nil {
								t.Fatal("invalid restore retained target lock")
							}
							_ = lock.Release()
						}
					})
				}
			}
		})
	}
}

func TestPausedSessionRestoreAllowsAvailableRolesAndTerminalMissingRole(t *testing.T) {
	a := newReadyTestMainAgent(t)
	path, _ := createPausedTakeoverTarget(t, a, "paused-target")
	a.agentConfigs = map[string]*config.AgentConfig{
		"main-sample":   {Name: "main-sample"},
		"worker-sample": {Name: "worker-sample", Mode: config.AgentModeSubAgent},
	}
	manager := recovery.NewRecoveryManager(path)
	snapshot, err := manager.Recover()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.ActiveRole = "main-sample"
	if err := manager.SaveSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := persistDurableTaskRecords(path, map[string]*DurableTaskRecord{
		"idle-task": {TaskID: "idle-task", AgentDefName: "worker-sample", State: string(SubAgentStateIdle), LatestInstanceID: "worker-sample-1", Attempt: 1},
		"done-task": {TaskID: "done-task", AgentDefName: "unavailable-sample", State: string(SubAgentStateCompleted), Attempt: 1},
	}); err != nil {
		t.Fatal(err)
	}
	writePausedTaskMeta(t, path, subAgentMeta{InstanceID: "worker-sample-1", TaskID: "idle-task", WorkDir: a.ContentRoot()})
	if err := recovery.SaveSessionMeta(path, recovery.SessionMeta{WorktreePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.loadSessionState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ActiveRole != "main-sample" || loaded.Pause == nil || len(loaded.TaskRecords) != 2 {
		t.Fatal("available saved roles were not restored")
	}
}

func writePausedTaskMeta(t *testing.T, path string, meta subAgentMeta) {
	t.Helper()
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	metaPath := subAgentMetaPath(path, meta.InstanceID)
	if err := os.MkdirAll(filepath.Dir(metaPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPausedTaskRestoreRejectsUnavailableWorkingDirectoryBinding(t *testing.T) {
	for _, kind := range []string{"removed", "file", "missing_meta", "invalid_meta", "empty_directory", "wrong_task", "outside_repository"} {
		t.Run(kind, func(t *testing.T) {
			a := newReadyTestMainAgent(t)
			path, _ := createPausedTakeoverTarget(t, a, "sample-target")
			record := &DurableTaskRecord{TaskID: "task-1", AgentDefName: "worker-sample", State: string(SubAgentStateIdle), LatestInstanceID: "worker-sample-1", InstanceHistory: []string{"worker-sample-1"}}
			if err := persistDurableTaskRecords(path, map[string]*DurableTaskRecord{record.TaskID: record}); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(a.ContentRoot(), "sample-checkout")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			meta := subAgentMeta{InstanceID: record.LatestInstanceID, TaskID: record.TaskID, WorkDir: dir}
			switch kind {
			case "removed", "file":
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
				if kind == "file" {
					if err := os.WriteFile(dir, []byte("sample"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "empty_directory":
				meta.WorkDir = ""
			case "wrong_task":
				meta.TaskID = "task-2"
			case "outside_repository":
				meta.WorkDir = t.TempDir()
			}
			if kind != "missing_meta" {
				writePausedTaskMeta(t, path, meta)
			}
			if kind == "invalid_meta" {
				if err := os.WriteFile(subAgentMetaPath(path, meta.InstanceID), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Directory validation remains mandatory without a role catalog.
			if _, err := a.loadSessionState(path); err == nil || !strings.Contains(err.Error(), "restore_failed") {
				t.Fatalf("invalid task binding accepted: %v", err)
			}
		})
	}
}

func TestPausedTaskReactivationRechecksSavedWorkingDirectory(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.SetAgentConfigs(map[string]*config.AgentConfig{"worker-sample": {Name: "worker-sample", Mode: config.AgentModeSubAgent}})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	record := &DurableTaskRecord{TaskID: "task-1", AgentDefName: "worker-sample", State: string(SubAgentStateIdle), LatestInstanceID: "worker-sample-1", InstanceHistory: []string{"worker-sample-1"}}
	dir := filepath.Join(a.ContentRoot(), "sample-checkout")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writePausedTaskMeta(t, a.SessionDir(), subAgentMeta{InstanceID: record.LatestInstanceID, TaskID: record.TaskID, WorkDir: dir, WorkDirGeneration: 3})
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})
	loaded := &loadedSessionState{SessionPath: a.SessionDir(), Pause: &recovery.SessionPauseCheckpoint{Paused: true}, TaskRecords: map[string]*DurableTaskRecord{record.TaskID: record}}
	if err := a.validatePausedSessionRestore(loaded); err != nil {
		t.Fatal(err)
	}
	a.installPausedSession(loaded)
	a.finishPausedSessionResume(sessionPauseResult{checkpoint: loaded.Pause.Clone()})
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.getOrRehydrateTask(record); err == nil || !strings.Contains(err.Error(), "restore_failed") {
		t.Fatalf("reactivation accepted a removed directory: %v", err)
	}
	if sub := a.subAgentByTaskID(record.TaskID); sub != nil {
		t.Fatal("failed reactivation published a worker")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	sub, _, _, err := a.getOrRehydrateTask(record)
	if err != nil {
		t.Fatal(err)
	}
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if sub.effectiveToolBaseDir() != canonicalDir || sub.workDirState.load().Generation != 3 {
		t.Fatalf("reactivated task lost saved binding: %+v", sub.workDirState.load())
	}
}

func TestOrdinaryHistoryRestoreKeepsMetadataAndRoleBestEffort(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.agentConfigs = map[string]*config.AgentConfig{}
	loaded := &loadedSessionState{SessionPath: t.TempDir(), ActiveRole: "unavailable-sample"}
	if err := os.WriteFile(filepath.Join(loaded.SessionPath, "session-meta.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.validatePausedSessionRestore(loaded); err != nil {
		t.Fatal("ordinary history unexpectedly requires paused dependencies")
	}
}

func TestPausedSessionRestoreRejectsMissingInputOwner(t *testing.T) {
	for _, consumer := range []string{"load", "takeover"} {
		t.Run(consumer, func(t *testing.T) {
			a := newReadyTestMainAgent(t)
			installTakeoverTestLock(t, a)
			oldPath, oldLock, oldGate := a.SessionDir(), a.sessionLock, a.sessionExecutionGate()
			path, id := createPausedTakeoverTarget(t, a, "paused-target")
			manager := recovery.NewRecoveryManager(path)
			snapshot, err := manager.Recover()
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Pause.PendingInputs[0].OwnerTaskID = "missing-task"
			if err := manager.SaveSnapshot(snapshot); err != nil {
				t.Fatal(err)
			}
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
			if consumer == "load" {
				_, err = a.loadSessionState(path)
			} else {
				reply := make(chan error, 1)
				a.handleTakeoverTarget(sessionTakeoverTarget{ctx: t.Context(), sessionID: id.SessionID, identity: id, reply: reply})
				err = <-reply
			}
			if err == nil || !strings.Contains(err.Error(), "owner \"missing-task\" is unavailable") {
				t.Fatalf("missing owner restore error=%v", err)
			}
			if a.SessionDir() != oldPath || a.sessionLock != oldLock || oldGate.Snapshot().Paused {
				t.Fatal("invalid restore changed current session")
			}
		})
	}
}
