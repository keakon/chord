package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
	"github.com/keakon/chord/internal/tools"
)

const takeoverProcessHelperEnv = "CHORD_TAKEOVER_TEST_PROCESS"
const takeoverProcessWorkspaceEnv = "CHORD_TAKEOVER_TEST_WORKSPACE"

func takeoverSampleAttachment(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

type takeoverProcessProvider struct {
	noopProvider
	workspace string
	target    bool
	started   chan struct{}
	calls     atomic.Int64
}

func (p *takeoverProcessProvider) CompleteStream(ctx context.Context, _, _, _ string, msgs []message.Message, _ []message.ToolDefinition, _ int, _ llm.RequestTuning, callback llm.StreamCallback) (*message.Response, error) {
	count := p.calls.Add(1)
	name := "source-calls"
	if p.target {
		name = "target-calls"
	}
	if err := os.WriteFile(filepath.Join(p.workspace, name), fmt.Appendf(nil, "%d", count), 0600); err != nil {
		return nil, err
	}
	if !p.target && count == 1 {
		close(p.started)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(filepath.Join(p.workspace, "release-request")); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
			}
		}
	}
	if p.target {
		data, err := json.Marshal(msgs)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(p.workspace, "target-request.json")
		if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
			return nil, err
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			return nil, err
		}
	}
	if callback != nil {
		callback(message.StreamDelta{Type: message.StreamDeltaText, Text: "Saved response."})
	}
	return &message.Response{Content: "Saved response.", StopReason: "stop"}, nil
}

func takeoverProcessApp(t *testing.T, workspace string, target bool) (*AppContext, *takeoverProcessProvider) {
	t.Helper()
	loc, err := locateTakeover(context.Background(), workspace, "sample-session")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(loc.sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := recovery.AcquireSessionLock(loc.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	provider := &takeoverProcessProvider{workspace: workspace, target: target, started: make(chan struct{})}
	cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}, Modalities: &config.ModelModalities{Input: []string{"text", "image"}}}}}, []string{"test-key"})
	client := llm.NewClient(cfg, provider, "test-model", 1024, "")
	manager := ctxmgr.NewManager(8192, 0)
	registry := tools.NewRegistry()
	main := agent.NewMainAgent(ctx, client, manager, registry, &hook.NoopEngine{}, loc.sessionDir, "test-model", workspace, workspace, &config.Config{}, nil, mcp.ClientInfo{Name: "sample", Version: "test"})
	main.SetSessionLock(lock)
	main.ReloadAgentsMD()
	main.MarkSkillsReady()
	main.SetPendingMCPDiscovery(nil, "")
	if target {
		if err := main.RestoreSessionAtStartup(); err != nil {
			t.Fatal(err)
		}
	}
	ac := &AppContext{Ctx: ctx, Cancel: cancel, MainAgent: main, SessionLock: lock, SessionDir: loc.sessionDir, ContentRoot: workspace, WorkDir: workspace, Cfg: &config.Config{}, GlobalCfg: &config.Config{}, Registry: registry, LLMClient: client, CtxMgr: manager, PathLocator: &config.PathLocator{StateDir: loc.stateDir}, ProjectLocator: &config.ProjectLocator{CanonicalRoot: loc.workspace, ProjectSessionsDir: filepath.Dir(loc.sessionDir)}}
	return ac, provider
}

func runTakeoverProcessHelper(t *testing.T, mode string) {
	workspace := os.Getenv(takeoverProcessWorkspaceEnv)
	target := strings.HasPrefix(mode, "target-")
	kind := strings.TrimPrefix(strings.TrimPrefix(mode, "target-"), "source-")
	ac, provider := takeoverProcessApp(t, workspace, target)
	seedInput := func() {
		<-provider.started
		parts := []message.ContentPart{{Type: message.ContentPartText, Text: "Queued request."}}
		if kind == takeover.OwnerTUI {
			path := filepath.Join(workspace, "temporary.png")
			if err := os.WriteFile(path, takeoverSampleAttachment(t), 0600); err != nil {
				t.Error(err)
				return
			}
			parts = append(parts, message.ContentPart{Type: message.ContentPartImage, MimeType: "image/png", ImagePath: path})
		}
		if !ac.MainAgent.QueuePendingUserDraft("sample-draft", parts) {
			t.Error("pending input rejected")
			return
		}
		if err := os.WriteFile(filepath.Join(workspace, "queued-input"), []byte("ready"), 0600); err != nil {
			t.Error(err)
		}
	}
	if !target {
		go seedInput()
	}
	if kind == takeover.OwnerGateway {
		flagGatewayManaged = true
		flagGatewayInstance = "binding-sample"
		if target {
			flagGatewayTransferID = "transfer-sample"
		}
		err := runHeadlessWithDeps(headlessRunDeps{initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil }, createRuntime: func(ac *AppContext) (headlessRuntime, error) {
			go ac.MainAgent.Run(ac.Ctx)
			return runtimeHeadlessAdapter{rt: &Runtime{Agent: ac.MainAgent}}, nil
		}, stdin: os.Stdin, stdout: os.Stdout})
		if cliExitCode(err) == takeoverExitCode {
			os.Exit(takeoverExitCode)
		}
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	go ac.MainAgent.Run(ac.Ctx)
	var endpoint *runtimeTakeover
	if !target {
		var err error
		endpoint, err = startRuntimeTakeover(ac, takeover.OwnerTUI)
		if err != nil {
			t.Fatal(err)
		}
		ac.MainAgent.SendUserMessage("Start the request.")
	}
	if err := json.NewEncoder(os.Stdout).Encode(headlessEnvelope{Type: "ready", Payload: map[string]any{"session_id": "sample-session"}}); err != nil {
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var cmd headlessCommand
			if json.Unmarshal(scanner.Bytes(), &cmd) == nil && cmd.Type == "send" {
				ac.MainAgent.SendUserMessage(cmd.Content)
			}
		}
		if err := scanner.Err(); err != nil {
			t.Error(err)
		}
	}()
	for event := range ac.MainAgent.Events() {
		if e, ok := event.(agent.ErrorEvent); ok {
			t.Fatal(e.Err)
		}
		if e, ok := event.(agent.SessionTransferEvent); ok && e.Status.State == agent.SessionPauseSaved {
			if err := ac.MainAgent.ReleasePausedSession(e.Status.TransferID); err != nil {
				t.Fatal(err)
			}
			endpoint.close()
			ac.Close()
			os.Exit(takeoverExitCode)
		}
		if _, ok := event.(agent.AssistantMessageEvent); ok && target {
			if err := json.NewEncoder(os.Stdout).Encode(headlessEnvelope{Type: "completed", Payload: map[string]any{"calls": provider.calls.Load()}}); err != nil {
				t.Fatal(err)
			}
			ac.Close()
			os.Exit(0)
		}
	}
	t.Fatal("helper ended without transfer")
}

type takeoverChild struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	lines   <-chan []byte
	scanErr chan error
	stderr  bytes.Buffer
}

func startTakeoverChild(t *testing.T, ctx context.Context, workspace, mode string) *takeoverChild {
	t.Helper()
	child := &takeoverChild{cmd: exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTakeoverProcessRoundTrip$", "-test.timeout=45s"), scanErr: make(chan error, 1)}
	child.cmd.Env = append(os.Environ(), takeoverProcessHelperEnv+"="+mode, takeoverProcessWorkspaceEnv+"="+workspace)
	var err error
	child.stdin, err = child.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.cmd.Stderr = &child.stderr
	lines := make(chan []byte, 256)
	child.lines = lines
	if err := child.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- append([]byte(nil), scanner.Bytes()...)
		}
		if err := scanner.Err(); err != nil {
			child.scanErr <- err
		}
	}()
	t.Cleanup(func() {
		_ = child.stdin.Close()
		if child.cmd.ProcessState == nil {
			_ = child.cmd.Process.Kill()
			_ = child.cmd.Wait()
		}
	})
	return child
}

func (c *takeoverChild) waitEnvelope(t *testing.T, ctx context.Context, want string) headlessEnvelope {
	t.Helper()
	for {
		select {
		case data, ok := <-c.lines:
			if !ok {
				select {
				case err := <-c.scanErr:
					t.Fatalf("read child output: %v", err)
				default:
				}
				t.Fatalf("child output ended awaiting %s: %s", want, c.stderr.String())
			}
			var env headlessEnvelope
			if err := json.Unmarshal(data, &env); err != nil {
				var output strings.Builder
				output.Write(data)
				for rest := range c.lines {
					output.WriteByte('\n')
					output.Write(rest)
				}
				t.Fatalf("non-JSON child stdout %q: %v", output.String(), err)
			}
			if env.Type == want {
				return env
			}
		case <-ctx.Done():
			t.Fatalf("timeout awaiting %s: %s", want, c.stderr.String())
		}
	}
}

func waitTakeoverMarker(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			entries, _ := os.ReadDir(filepath.Dir(path))
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Fatalf("timeout awaiting %s; workspace files=%v", filepath.Base(path), names)
		}
	}
}

func TestTakeoverProcessRoundTrip(t *testing.T) {
	if mode := os.Getenv(takeoverProcessHelperEnv); mode != "" {
		runTakeoverProcessHelper(t, mode)
		return
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("local session takeover requires macOS or Linux")
	}
	withTestStateDir(t)
	configText := "providers:\n  sample:\n    type: messages\n    models:\n      test-model: {}\nmodel_pools:\n  default: [sample/test-model]\n"
	if err := os.WriteFile(filepath.Join(flagConfigHome, "config.yaml"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	// Unix socket names have a small platform limit. Keep the endpoint root
	// short even when the Go test runner uses a deeply nested temp directory.
	endpointRoot, err := os.MkdirTemp("/tmp", "chord-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(endpointRoot) })
	flagStateDir = filepath.Join(endpointRoot, "state")
	flagSessionsDir = filepath.Join(flagStateDir, "sessions")
	t.Setenv("CHORD_STATE_DIR", flagStateDir)
	t.Setenv("CHORD_SESSIONS_DIR", flagSessionsDir)
	if err := os.MkdirAll(flagStateDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, sourceKind := range []string{takeover.OwnerTUI, takeover.OwnerGateway} {
		t.Run(sourceKind, func(t *testing.T) {
			workspace, err := os.MkdirTemp("", "chord-takeover-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(workspace) })
			workspace, err = filepath.EvalSymlinks(workspace)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
			defer cancel()
			source := startTakeoverChild(t, ctx, workspace, "source-"+sourceKind)
			source.waitEnvelope(t, ctx, "ready")
			if sourceKind == takeover.OwnerGateway {
				if _, err := io.WriteString(source.stdin, "{\"type\":\"send\",\"content\":\"Start the request.\",\"request_id\":\"request-sample\"}\n"); err != nil {
					t.Fatal(err)
				}
			}
			waitTakeoverMarker(t, ctx, filepath.Join(workspace, "queued-input"))
			loc, err := locateTakeover(ctx, workspace, "sample-session")
			if err != nil {
				t.Fatal(err)
			}
			targetKind := takeover.OwnerGateway
			if sourceKind == takeover.OwnerGateway {
				targetKind = takeover.OwnerTUI
			}
			result := make(chan struct {
				status agent.SessionPauseStatus
				err    error
			}, 1)
			go func() {
				timeout := time.Duration(0)
				status, err := startTakeover(ctx, loc, "sample-session", "transfer-sample", targetKind, "binding-sample", &timeout)
				result <- struct {
					status agent.SessionPauseStatus
					err    error
				}{status, err}
			}()
			if sourceKind == takeover.OwnerGateway {
				env := source.waitEnvelope(t, ctx, headlessTransferPreparing)
				data, _ := json.Marshal(env.Payload)
				var status takeover.Status
				if err := json.Unmarshal(data, &status); err != nil {
					t.Fatal(err)
				}
				ack := transferPreparedAck{Type: headlessTransferPrepared, TransferIdentity: status.TransferIdentity, BindingInstanceID: "binding-sample", Accepted: true}
				if err := json.NewEncoder(source.stdin).Encode(ack); err != nil {
					t.Fatal(err)
				}
			}
			var status agent.SessionPauseStatus
			select {
			case got := <-result:
				if got.err != nil {
					t.Fatal(got.err)
				}
				status = got.status
			case <-ctx.Done():
				t.Fatal("start coordinator timed out")
			}
			if status.State != agent.SessionPauseDraining {
				t.Fatalf("accepted state=%s", status.State)
			}
			if lock, err := recovery.AcquireSessionLock(loc.sessionDir); err == nil {
				_ = lock.Release()
				t.Fatal("source released lock while its request was still running")
			}
			if data, err := os.ReadFile(filepath.Join(workspace, "source-calls")); err != nil || string(data) != "1" {
				t.Fatalf("source requests=%s err=%v", data, err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "release-request"), []byte("ready"), 0600); err != nil {
				t.Fatal(err)
			}
			if sourceKind == takeover.OwnerGateway {
				source.waitEnvelope(t, ctx, headlessTransferReleased)
			}
			if err := source.cmd.Wait(); err == nil {
				t.Fatal("source did not use the transfer exit code")
			} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != takeoverExitCode {
				t.Fatalf("source exit=%v stderr=%s", err, source.stderr.String())
			}
			status, err = queryTakeover(ctx, loc, "sample-session", "transfer-sample")
			if err != nil || status.State != agent.SessionPauseReleased {
				t.Fatalf("durable status=%#v err=%v", status, err)
			}
			manager := recovery.NewRecoveryManager(loc.sessionDir)
			snapshot, err := manager.Recover()
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Pause == nil || !snapshot.Pause.Paused || !snapshot.Pause.CheckpointComplete || len(snapshot.Pause.PendingInputs) != 1 {
				t.Fatalf("pending checkpoint=%#v", snapshot.Pause)
			}
			transcript, err := manager.LoadMessages(identity.MainAgentID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, msg := range transcript {
				found = found || msg.Content == "Saved response."
			}
			if !found {
				t.Fatal("source response was not persisted")
			}
			if sourceKind == takeover.OwnerTUI {
				if err := os.Remove(filepath.Join(workspace, "temporary.png")); err != nil {
					t.Fatal(err)
				}
			}
			_ = manager.Close()
			target := startTakeoverChild(t, ctx, workspace, "target-"+targetKind)
			target.waitEnvelope(t, ctx, "ready")
			if _, err := os.Stat(filepath.Join(workspace, "target-calls")); !os.IsNotExist(err) {
				t.Fatalf("paused target sent a model request: %v", err)
			}
			if lock, err := recovery.AcquireSessionLock(loc.sessionDir); err == nil {
				_ = lock.Release()
				t.Fatal("target did not acquire the unique session lock")
			}
			if _, err := io.WriteString(target.stdin, "{\"type\":\"send\",\"content\":\"Continue this conversation.\",\"request_id\":\"request-target\"}\n"); err != nil {
				t.Fatal(err)
			}
			waitTakeoverMarker(t, ctx, filepath.Join(workspace, "target-request.json"))
			data, err := os.ReadFile(filepath.Join(workspace, "target-request.json"))
			if err != nil {
				t.Fatal(err)
			}
			var msgs []message.Message
			if err := json.Unmarshal(data, &msgs); err != nil {
				t.Fatal(err)
			}
			queued, attachments := 0, 0
			for _, msg := range msgs {
				if msg.Content == "Queued request." {
					queued++
				}
				for _, part := range msg.Parts {
					if part.Type == message.ContentPartImage {
						attachments++
						payload := part.Data
						if len(payload) == 0 {
							payload, err = os.ReadFile(part.ImagePath)
						}
						if err != nil || !bytes.Equal(payload, takeoverSampleAttachment(t)) {
							t.Fatalf("restored attachment=%q err=%v", payload, err)
						}
					}
				}
			}
			if queued != 1 {
				t.Fatalf("queued input count=%d", queued)
			}
			if sourceKind == takeover.OwnerTUI && attachments != 1 {
				t.Fatalf("restored attachment count=%d", attachments)
			}
			if targetKind == takeover.OwnerGateway {
				target.stdin.Close()
			} else {
				target.waitEnvelope(t, ctx, "completed")
			}
			if err := target.cmd.Wait(); err != nil {
				t.Fatalf("target exit=%v stderr=%s", err, target.stderr.String())
			}
		})
	}
}
