//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/tools"
)

// The external worker reuses the host's confirm and question channels. These
// tests drive a real supervised external CLI through headless mode's actual
// event filter and command handler, so the envelope identity a gateway relies
// on (agent_id pointing at the worker instance) is exercised end to end.

const externalConfirmBody = `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"checking the fixture"},{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"README.md"}}]}}'
printf '%s\n' '{"type":"control_request","request_id":"perm-confirm","request":{"subtype":"can_use_tool","tool_name":"Read","input":{"file_path":"README.md"},"tool_use_id":"tool-1"}}'
IFS= read -r permission_response
case "$permission_response" in
  *'"behavior":"allow"'*) ;;
  *) exit 9 ;;
esac
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"fixture contents"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"external answer"}'
`

const externalQuestionBody = `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"asking the fixture"},{"type":"tool_use","id":"tool-q","name":"AskUserQuestion","input":{"questions":[{"question":"Which color?","header":"Color","options":[{"label":"Blue","description":"Blue"},{"label":"Green","description":"Green"}],"multiSelect":false}]}}]}}'
printf '%s\n' '{"type":"control_request","request_id":"perm-question","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which color?","header":"Color","options":[{"label":"Blue","description":"Blue"},{"label":"Green","description":"Green"}],"multiSelect":false}]},"tool_use_id":"tool-q"}}'
IFS= read -r permission_response
if ! printf '%s' "$permission_response" | grep -q '"answers"'; then exit 9; fi
if ! printf '%s' "$permission_response" | grep -q 'Which color?'; then exit 9; fi
if ! printf '%s' "$permission_response" | grep -q 'Blue'; then exit 9; fi
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-q","content":"Blue"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"external answer"}'
`

func writeHeadlessFakeClaude(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-claude")
	script := "#!/bin/sh\n" + `
IFS= read -r init
case "$init" in *'"subtype":"initialize"'*) ;; *) exit 8 ;; esac
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"chord-initialize","response":{}}}'
IFS= read -r prompt
` + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

func headlessTestPermissionNode(t *testing.T, src string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("parse permission: %v", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return *doc.Content[0]
	}
	return doc
}

type headlessEventRecorder struct {
	mu   sync.Mutex
	envs []*headlessEnvelope
}

func (r *headlessEventRecorder) add(env *headlessEnvelope) {
	if env == nil {
		return
	}
	r.mu.Lock()
	r.envs = append(r.envs, env)
	r.mu.Unlock()
}

func (r *headlessEventRecorder) snapshot() []*headlessEnvelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*headlessEnvelope(nil), r.envs...)
}

func (r *headlessEventRecorder) wait(t *testing.T, typ string) *headlessEnvelope {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		for _, env := range r.snapshot() {
			if env.Type == typ {
				return env
			}
		}
		if time.Now().After(deadline) {
			var got []string
			for _, env := range r.snapshot() {
				got = append(got, env.Type)
			}
			t.Fatalf("timed out waiting for %s; received %v", typ, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type externalHeadlessRig struct {
	t      *testing.T
	agent  *agent.MainAgent
	state  *headlessState
	out    *testOut
	events *headlessEventRecorder
}

func newExternalHeadlessRig(t *testing.T, command, permissionYAML string) *externalHeadlessRig {
	t.Helper()
	projectRoot := t.TempDir()
	sessionDir := filepath.Join(projectRoot, ".chord", "sessions", "test")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	paths := &config.PathLocator{
		ConfigHome:   filepath.Join(projectRoot, ".test-chord", "config"),
		StateDir:     filepath.Join(projectRoot, ".test-chord", "state"),
		CacheDir:     filepath.Join(projectRoot, ".test-chord", "cache"),
		SessionsRoot: filepath.Join(projectRoot, ".test-chord", "sessions"),
		LogsDir:      filepath.Join(projectRoot, ".test-chord", "logs"),
		ExportsDir:   filepath.Join(projectRoot, ".test-chord", "state", "exports"),
	}
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	ctx, cancel := context.WithCancel(context.Background())
	mainAgent := agent.NewMainAgent(
		ctx,
		llm.NewClient(providerCfg, noopProvider{}, "test-model", 1024, ""),
		ctxmgr.NewManager(8192, 0),
		tools.NewRegistry(),
		&hook.NoopEngine{},
		sessionDir,
		"test-model",
		projectRoot,
		projectRoot,
		&config.Config{},
		nil,
		mcp.ClientInfo{Name: "chord-test", Version: "test"},
		paths,
	)
	mainAgent.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Delegation: config.DelegationConfig{MaxChildren: 10, MaxDepth: 2}},
		"external-worker": {
			Name:        "external-worker",
			Description: "external worker",
			Mode:        config.AgentModeSubAgent,
			Backend:     &config.BackendConfig{Kind: config.BackendKindClaudeCode, Command: command},
			Permission:  headlessTestPermissionNode(t, permissionYAML),
		},
	})
	registry := tools.NewRegistry()
	wireMainAgentRuntime(ctx, mainAgent, registry, 30*time.Second, false, false)
	t.Cleanup(func() {
		cancel()
		if err := mainAgent.Shutdown(5 * time.Second); err != nil {
			t.Logf("shutdown test main agent: %v", err)
		}
	})

	state := &headlessState{subscriptions: map[string]bool{
		"confirm_request":   true,
		"confirm_resolved":  true,
		"question_request":  true,
		"question_resolved": true,
		"error":             true,
	}}
	events := &headlessEventRecorder{}
	go func() {
		for ev := range mainAgent.Events() {
			for _, env := range filterHeadlessEvent(ev, state, mainAgent) {
				events.add(env)
			}
		}
	}()
	return &externalHeadlessRig{
		t:      t,
		agent:  mainAgent,
		state:  state,
		out:    newTestOut(t),
		events: events,
	}
}

func (r *externalHeadlessRig) createWorker() tools.TaskHandle {
	r.t.Helper()
	handle, err := r.agent.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "exercise the external protocol",
		AgentType:   "external-worker",
	})
	if err != nil {
		r.t.Fatalf("create external worker: %v", err)
	}
	if handle.Status != "started" {
		r.t.Fatalf("external worker status = %q, want started", handle.Status)
	}
	return handle
}

func (r *externalHeadlessRig) waitTaskCompleted(taskID string) {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		for _, rec := range r.agent.OrchestrationTaskDiagnostics() {
			if rec.TaskID != taskID {
				continue
			}
			switch rec.State {
			case string(agent.SubAgentStateCompleted):
				return
			case string(agent.SubAgentStateFailed), string(agent.SubAgentStateCancelled):
				r.t.Fatalf("external task %s reached %s (closed_reason=%q), want completed", taskID, rec.State, rec.ClosedReason)
			}
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for external task %s to complete", taskID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHeadlessExternalPermissionConfirmRoundTrip(t *testing.T) {
	rig := newExternalHeadlessRig(t, writeHeadlessFakeClaude(t, externalConfirmBody), "\"*\": allow\nread: ask")
	handle := rig.createWorker()

	request := rig.events.wait(t, "confirm_request")
	payload, ok := request.Payload.(map[string]any)
	if !ok {
		t.Fatalf("confirm_request payload = %T, want map[string]any", request.Payload)
	}
	if payload["agent_id"] != handle.AgentID {
		t.Fatalf("confirm_request agent_id = %#v, want the worker instance %q", payload["agent_id"], handle.AgentID)
	}
	if payload["tool_name"] != tools.NameRead {
		t.Fatalf("confirm_request tool_name = %#v, want %q", payload["tool_name"], tools.NameRead)
	}
	requestID, _ := payload["request_id"].(string)
	if requestID == "" {
		t.Fatalf("confirm_request has no request id: %#v", payload)
	}
	rig.state.mu.Lock()
	pending := rig.state.pendingConfirm
	rig.state.mu.Unlock()
	if pending == nil || pending.RequestID != requestID || pending.AgentID != handle.AgentID {
		t.Fatalf("pendingConfirm = %+v, want the worker request %s", pending, requestID)
	}

	handleHeadlessCommand(headlessCommand{
		Type:      "confirm",
		Action:    "allow",
		RequestID: requestID,
	}, rig.agent, rig.state, rig.out.writer())

	resolved := rig.events.wait(t, "confirm_resolved")
	resolvedPayload, ok := resolved.Payload.(map[string]any)
	if !ok {
		t.Fatalf("confirm_resolved payload = %T, want map[string]any", resolved.Payload)
	}
	if resolvedPayload["request_id"] != requestID {
		t.Fatalf("confirm_resolved request_id = %#v, want %s", resolvedPayload["request_id"], requestID)
	}
	if resolvedPayload["agent_id"] != handle.AgentID {
		t.Fatalf("confirm_resolved agent_id = %#v, want %q", resolvedPayload["agent_id"], handle.AgentID)
	}
	if resolvedPayload["outcome"] != agent.ConfirmOutcomeApproved {
		t.Fatalf("confirm_resolved outcome = %#v, want %q", resolvedPayload["outcome"], agent.ConfirmOutcomeApproved)
	}
	rig.state.mu.Lock()
	pending = rig.state.pendingConfirm
	rig.state.mu.Unlock()
	if pending != nil {
		t.Fatalf("pendingConfirm = %+v, want nil after the resolved event", pending)
	}

	rig.waitTaskCompleted(handle.TaskID)
}

func TestHeadlessExternalAskUserQuestionRoundTrip(t *testing.T) {
	rig := newExternalHeadlessRig(t, writeHeadlessFakeClaude(t, externalQuestionBody), "\"*\": allow")
	handle := rig.createWorker()

	request := rig.events.wait(t, "question_request")
	payload, ok := request.Payload.(*headlessQuestionPayload)
	if !ok {
		t.Fatalf("question_request payload = %T, want *headlessQuestionPayload", request.Payload)
	}
	if payload.AgentID != handle.AgentID {
		t.Fatalf("question_request agent_id = %q, want the worker instance %q", payload.AgentID, handle.AgentID)
	}
	if payload.ToolName != tools.NameQuestion || payload.Question != "Which color?" {
		t.Fatalf("question_request = %+v, want the external question", payload)
	}
	if len(payload.Options) != 2 || payload.Options[0] != "Blue" {
		t.Fatalf("question_request options = %v, want the CLI-provided labels", payload.Options)
	}
	if len(payload.OptionIDs) != 2 || payload.OptionIDs[0] != "0" {
		t.Fatalf("question_request option ids = %v, want the CLI-order ids", payload.OptionIDs)
	}

	handleHeadlessCommand(headlessCommand{
		Type:        "question",
		Action:      agent.QuestionOpAnswer,
		OperationID: "answer-1",
		RequestID:   payload.RequestID,
		Answers:     []string{payload.OptionIDs[0]},
	}, rig.agent, rig.state, rig.out.writer())

	resolved := rig.events.wait(t, "question_resolved")
	resolvedPayload, ok := resolved.Payload.(*headlessQuestionPayload)
	if !ok {
		t.Fatalf("question_resolved payload = %T, want *headlessQuestionPayload", resolved.Payload)
	}
	if resolvedPayload.RequestID != payload.RequestID || resolvedPayload.AgentID != handle.AgentID {
		t.Fatalf("question_resolved = %+v, want the answered worker question", resolvedPayload)
	}
	if resolvedPayload.Outcome != tools.QuestionOutcomeAnswered {
		t.Fatalf("question_resolved outcome = %q, want %q", resolvedPayload.Outcome, tools.QuestionOutcomeAnswered)
	}
	if len(resolvedPayload.Selected) != 1 || resolvedPayload.Selected[0] != "Blue" {
		t.Fatalf("question_resolved selected = %v, want [Blue]", resolvedPayload.Selected)
	}

	rig.waitTaskCompleted(handle.TaskID)
}
