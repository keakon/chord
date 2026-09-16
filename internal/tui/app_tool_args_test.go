package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestToolCardKeepsModelArgumentsWhenEffectiveArgumentsDiffer(t *testing.T) {
	m := NewModelWithSize(nil, 120, 16)
	modelArgs := `{"query":"cats"}`
	effectiveArgs := `{"query":"cats","apiKey":"runtime-secret"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: "mcp-call-1", Name: "mcp_exa_search", ArgsJSON: modelArgs,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID: "mcp-call-1", Name: "mcp_exa_search", ArgsJSON: effectiveArgs,
		State: agent.ToolCallExecutionStateRunning,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID: "mcp-call-1", Name: "mcp_exa_search", ArgsJSON: effectiveArgs,
		Audit:  &message.ToolArgsAudit{OriginalArgsJSON: modelArgs, EffectiveArgsJSON: effectiveArgs, UserModified: true},
		Result: "ok", Status: agent.ToolResultStatusSuccess,
	}})

	block, ok := m.viewport.FindBlockByToolID("mcp-call-1")
	if !ok {
		t.Fatal("expected MCP tool block")
	}
	if block.RawArgs != modelArgs {
		t.Fatalf("RawArgs = %q, want model args %q", block.RawArgs, modelArgs)
	}
	keys, vals := parseToolArgs(block.RawArgs)
	mainPart, grayPart, _ := genericToolHeaderParts(keys, vals)
	header := mainPart + " " + grayPart
	if strings.Contains(header, "runtime-secret") {
		t.Fatalf("tool card exposed effective argument: %q", header)
	}
	if !strings.Contains(header, "cats") {
		t.Fatalf("tool card omitted model argument: %q", header)
	}
}

func TestToolResultMarksIgnoredArgumentInToolCard(t *testing.T) {
	m := NewModelWithSize(nil, 120, 16)
	requested := `{"path":"sample.go","limit":40,"format":"json"}`
	effective := `{"limit":40,"path":"sample.go"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: "read-ignored", Name: tools.NameRead, ArgsJSON: requested,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID: "read-ignored", Name: tools.NameRead, ArgsJSON: effective,
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  requested,
			EffectiveArgsJSON: effective,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.format",
				ValueJSON: `"json"`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
		Result: "READ_RESULT lines=1-1 total=1\npackage sample", Status: agent.ToolResultStatusSuccess,
	}})

	block, ok := m.viewport.FindBlockByToolID("read-ignored")
	if !ok {
		t.Fatal("expected read tool block")
	}
	if strings.Contains(block.Content, "format") || !strings.Contains(block.Content, `"limit":40`) {
		t.Fatalf("block.Content = %q, want only effective arguments", block.Content)
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	if plain := stripANSI(rendered); !strings.Contains(plain, "format=json") {
		t.Fatalf("ignored argument missing from card:\n%s", plain)
	}
	if !strings.Contains(rendered, ";9m") {
		t.Fatalf("ignored argument is not struck through: %q", rendered)
	}
}

func TestToolCallUpdateEventArgsStreamingDoneMarksQueuedBeforeExecution(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	args := `{"todos":[{"id":"1","content":"a","status":"pending"}]}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-todo-queued-1",
		Name:     "todo_write",
		AgentID:  "",
		ArgsJSON: args,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:                "call-todo-queued-1",
		Name:              "todo_write",
		AgentID:           "",
		ArgsJSON:          args,
		ArgsStreamingDone: true,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-todo-queued-1")
	if !ok {
		t.Fatal("expected todo tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("ToolExecutionState = %q, want %q", block.ToolExecutionState, agent.ToolCallExecutionStateQueued)
	}
	if block.ToolQueuedByExecutionEvent {
		t.Fatalf("expected speculative queued state (not execution queued)")
	}
	joined := stripANSI(strings.Join(block.Render(96, "▖"), "\n"))
	if !strings.Contains(joined, "⧗ todo_write") {
		t.Fatalf("expected speculative queued TodoWrite to render a static pending indicator; got:\n%s", joined)
	}
	if strings.Contains(joined, "Queued") {
		t.Fatalf("did not expect queued header badge for speculative queued TodoWrite; got:\n%s", joined)
	}
	if strings.Contains(joined, "⏸") {
		t.Fatalf("did not expect execution-queued glyph for speculative queued TodoWrite; got:\n%s", joined)
	}
}

// Chat-completions providers emit no per-call args-end event, so a card is
// completed by inference when a later call starts streaming. Body text must be
// re-derived from the accumulated arguments then: streaming deltas are
// throttled, so the card can otherwise stay pinned to its first partial frame.
func TestInferredArgsCompleteRebuildsContentFromAccumulatedArgs(t *testing.T) {
	m := NewModelWithSize(nil, 100, 12)

	const first, full = `{"task":"invest`, `{"task":"investigate the delegate path"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-inferred-a",
		Name:     "delegate",
		AgentID:  "",
		ArgsJSON: first,
	}})
	// This delta lands inside the render cadence and is throttled away, but it
	// must still reach RawArgs.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-inferred-a",
		Name:     "delegate",
		AgentID:  "",
		ArgsJSON: full,
	}})
	// The next call's start is the only completion signal available.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-inferred-b",
		Name:     "delegate",
		AgentID:  "",
		ArgsJSON: `{"task":"`,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-inferred-a")
	if !ok {
		t.Fatal("expected first delegate tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("ToolExecutionState = %q, want %q", block.ToolExecutionState, agent.ToolCallExecutionStateQueued)
	}
	if block.ToolQueuedByExecutionEvent {
		t.Fatal("inferred completion must not earn the execution-queued badge")
	}
	if block.RawArgs != full {
		t.Fatalf("RawArgs = %q, want %q — throttled deltas must still update RawArgs", block.RawArgs, full)
	}
	if !strings.Contains(block.Content, "investigate the delegate path") {
		t.Fatalf("Content = %q, want the completed arguments rather than the throttled first frame", block.Content)
	}
	if block.ToolProgress != nil {
		t.Fatalf("ToolProgress = %+v, want nil once arguments are complete", block.ToolProgress)
	}
}

func TestToolArgRenderRefreshRespectsCadenceAndByteGrowth(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	now := time.Now()
	m.recordToolArgRender("call-1", `{"command":"echo"}`, now)

	if m.shouldRefreshToolArgRender("call-1", `{"command":"echo hi"}`, now.Add(100*time.Millisecond)) {
		t.Fatal("did not expect refresh before cadence delay elapses")
	}
	if m.shouldRefreshToolArgRender("call-1", `{"command":"echo"}`, now.Add(200*time.Millisecond)) {
		t.Fatal("did not expect refresh when byte length did not grow")
	}
	if !m.shouldRefreshToolArgRender("call-1", `{"command":"echo hi"}`, now.Add(200*time.Millisecond)) {
		t.Fatal("expected refresh after cadence delay and byte growth")
	}
}

func TestAnyToolShowsReceivedCharCountFromStreamingArgs(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	startArgs := `{"command":"ec"}`
	updateArgs := `{"command":"echo hello world"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-shell-progress-1",
		Name:     "shell",
		AgentID:  "",
		ArgsJSON: startArgs,
	}})
	m.toolArgRenderState["call-shell-progress-1"] = toolArgRenderState{
		lastBytes: len(startArgs),
		lastAt:    time.Now().Add(-200 * time.Millisecond),
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-shell-progress-1",
		Name:     "shell",
		AgentID:  "",
		ArgsJSON: updateArgs,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-shell-progress-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "30 chars received") {
		t.Fatalf("expected generic arg char count progress; got:\n%s", joined)
	}
}

func TestWriteStreamingCardShowsPathBeforeContentCompletes(t *testing.T) {
	m := NewModelWithSize(nil, 100, 12)
	partial := `{"path":"src/demo.go","content":"package main`
	complete := `{"path":"src/demo.go","content":"package main\n"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: "call-write-streaming-path-1", Name: tools.NameWrite, AgentID: "", ArgsJSON: partial,
	}})
	block, ok := m.viewport.FindBlockByToolID("call-write-streaming-path-1")
	if !ok {
		t.Fatal("expected write tool block")
	}
	partialPlain := stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if !strings.Contains(partialPlain, "write src/demo.go") {
		t.Fatalf("expected partial Write card to show path, got:\n%s", partialPlain)
	}
	if !strings.Contains(partialPlain, "chars received") {
		t.Fatalf("expected partial Write card to show received char count, got:\n%s", partialPlain)
	}
	if strings.Contains(partialPlain, "package main") {
		t.Fatalf("did not expect incomplete Write content preview, got:\n%s", partialPlain)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: "call-write-streaming-path-1", Name: tools.NameWrite, AgentID: "", ArgsJSON: complete, ArgsStreamingDone: true,
	}})
	block.Collapsed = false
	completePlain := stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if !strings.Contains(completePlain, "package main") {
		t.Fatalf("expected completed Write card to show content preview, got:\n%s", completePlain)
	}
}

func TestFileMutationDisplaySurvivesExecutionAndResultEvents(t *testing.T) {
	tests := []struct {
		name            string
		toolName        string
		partialArgs     string
		completeArgs    string
		result          string
		diff            string
		wantContent     []string
		dontWantContent []string
		wantRendered    string
	}{
		{
			name:         "write content preview",
			toolName:     tools.NameWrite,
			partialArgs:  `{"path":"src/demo.go","content":"package main`,
			completeArgs: `{"path":"src/demo.go","content":"package main\n"}`,
			result:       "Successfully wrote 1 line, 13 bytes",
			wantContent:  []string{`"path":"src/demo.go"`, `"content":"package main\n"`},
			wantRendered: "package main",
		},
		{
			name:            "edit diff",
			toolName:        tools.NameEdit,
			partialArgs:     `{"path":"src/demo.go","old_string":"old`,
			completeArgs:    `{"path":"src/demo.go","old_string":"old","new_string":"new"}`,
			result:          "Replaced 1 occurrence",
			diff:            "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n",
			wantContent:     []string{"src/demo.go"},
			dontWantContent: []string{"old_string", "new_string"},
			wantRendered:    "+new",
		},
		{
			name:            "patch diff",
			toolName:        tools.NameApplyPatch,
			partialArgs:     `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old`,
			completeArgs:    `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`,
			result:          "Applied patch to src/demo.go (+1 -1)",
			diff:            "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n",
			wantContent:     []string{"src/demo.go"},
			dontWantContent: []string{`"patch"`},
			wantRendered:    "+new",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModelWithSize(nil, 100, 12)
			callID := "call-file-mutation-result-" + tt.toolName

			_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
				ID: callID, Name: tt.toolName, AgentID: "", ArgsJSON: tt.partialArgs,
			}})
			_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
				ID: callID, Name: tt.toolName, AgentID: "", ArgsJSON: tt.completeArgs, ArgsStreamingDone: true,
			}})
			_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
				ID: callID, Name: tt.toolName, AgentID: "", ArgsJSON: tt.completeArgs, State: agent.ToolCallExecutionStateRunning,
			}})
			_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
				CallID: callID, Name: tt.toolName, ArgsJSON: tt.completeArgs, Result: tt.result, Status: agent.ToolResultStatusSuccess, Diff: tt.diff,
			}})

			block, ok := m.viewport.FindBlockByToolID(callID)
			if !ok {
				t.Fatalf("expected %s tool block", tt.toolName)
			}
			if block.RawArgs != tt.completeArgs {
				t.Fatalf("RawArgs = %q, want complete args %q", block.RawArgs, tt.completeArgs)
			}
			for _, want := range tt.wantContent {
				if !strings.Contains(block.Content, want) {
					t.Fatalf("Content = %q, want it to contain %q", block.Content, want)
				}
			}
			for _, notWant := range tt.dontWantContent {
				if strings.Contains(block.Content, notWant) {
					t.Fatalf("Content = %q, do not want it to contain %q", block.Content, notWant)
				}
			}
			plain := stripANSI(strings.Join(block.Render(100, "●"), "\n"))
			if !strings.Contains(plain, tt.wantRendered) {
				t.Fatalf("expected completed %s display to survive execution and result events, got:\n%s", tt.toolName, plain)
			}
		})
	}
}

func TestToolCallUpdateDoesNotMutateContentBeforeArgRenderCadence(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	startArgs := `{"todos":[{"content":"one"}]}`
	updateArgs := `{"todos":[{"content":"one"},{"content":"two"}]}`
	now := time.Now()

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-todo-throttle-1",
		Name:     "todo_write",
		AgentID:  "",
		ArgsJSON: startArgs,
	}})
	block, ok := m.viewport.FindBlockByToolID("call-todo-throttle-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	beforeContent := block.Content
	beforeVersion := m.viewport.RenderVersion()
	m.toolArgRenderState["call-todo-throttle-1"] = toolArgRenderState{
		lastBytes: len(startArgs),
		lastAt:    now,
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-todo-throttle-1",
		Name:     "todo_write",
		AgentID:  "",
		ArgsJSON: updateArgs,
	}})

	if block.Content != beforeContent {
		t.Fatalf("block.Content changed before render cadence:\nbefore=%q\nafter=%q", beforeContent, block.Content)
	}
	if got := m.viewport.RenderVersion(); got != beforeVersion {
		t.Fatalf("viewport render version = %d, want unchanged %d", got, beforeVersion)
	}

	m.toolArgRenderState["call-todo-throttle-1"] = toolArgRenderState{
		lastBytes: len(startArgs),
		lastAt:    now.Add(-foregroundCadence.visualAnimDelay),
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-todo-throttle-1",
		Name:     "todo_write",
		AgentID:  "",
		ArgsJSON: updateArgs,
	}})

	if block.Content == beforeContent {
		t.Fatal("block.Content should update once render cadence allows it")
	}
	if got := m.viewport.RenderVersion(); got == beforeVersion {
		t.Fatal("viewport render version should bump when throttled args are rendered")
	}
}

func TestToolCharCountProgressUsesExactDigits(t *testing.T) {
	largeJSON := `{"command":"` + strings.Repeat("x", 2190) + `"}`
	progress := inferToolArgProgress("shell", largeJSON)
	if progress == nil {
		t.Fatal("expected inferred arg progress")
	}
	if progress.Text != "2204 chars received" {
		t.Fatalf("progress.Text = %q, want exact digits", progress.Text)
	}
}

func TestApplyPatchToolHasNoInferredArgCharCount(t *testing.T) {
	for _, name := range []string{tools.NameApplyPatch, "patch"} {
		if progress := inferToolArgProgress(name, `{"patch":"*** Begin Patch"}`); progress != nil {
			t.Fatalf("inferToolArgProgress(%q) = %+v, want nil (patch text preview replaces char count)", name, progress)
		}
	}
	// Other tools keep the generic char count.
	if progress := inferToolArgProgress("shell", `{"command":"echo hi"}`); progress == nil {
		t.Fatal("expected generic char count for shell")
	}
}

func TestToolArgStreamingDoneClearsReceivedCharCountImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	argsJSON := `{"command":"echo hello world"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-shell-progress-done-1",
		Name:     "shell",
		AgentID:  "",
		ArgsJSON: argsJSON,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-shell-progress-done-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	if block.ToolProgress == nil {
		t.Fatal("expected transient char count before streaming completes")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:                "call-shell-progress-done-1",
		Name:              "shell",
		AgentID:           "",
		ArgsJSON:          argsJSON,
		ArgsStreamingDone: true,
	}})

	if block.ToolProgress != nil {
		t.Fatalf("expected temp char count cleared on tool arg completion, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("expected tool to hide temp char count when arg streaming completes; got:\n%s", joined)
	}
}

func TestDoneToolArgStreamingDoneClearsReceivedCharCountImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	argsJSON := `{"report":"## Completion status\nDone\n\n## Verification\nChecked"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-done-progress-done-1",
		Name:     "done",
		AgentID:  "",
		ArgsJSON: argsJSON,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-done-progress-done-1")
	if !ok {
		t.Fatal("expected Done tool block")
	}
	if block.ToolProgress == nil {
		t.Fatal("expected transient char count before Done arg streaming completes")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:                "call-done-progress-done-1",
		Name:              "done",
		AgentID:           "",
		ArgsJSON:          argsJSON,
		ArgsStreamingDone: true,
	}})

	if block.ToolProgress != nil {
		t.Fatalf("expected Done temp char count cleared on arg completion, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("expected Done to hide temp char count when arg streaming completes; got:\n%s", joined)
	}
}

func TestProseControlToolsShowAndClearReceivedCharCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
	}{
		{name: tools.NameComplete, args: `{"summary":"A sufficiently long completion summary"}`},
		{name: tools.NameEscalate, args: `{"reason":"A sufficiently long escalation reason"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 12)
			callID := "call-" + tc.name + "-progress"

			_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
				ID: callID, Name: tc.name, AgentID: "agent-1", ArgsJSON: tc.args,
			}})
			block, ok := m.viewport.FindBlockByToolID(callID)
			if !ok {
				t.Fatal("expected prose control tool block")
			}
			if block.ToolProgress == nil || !strings.Contains(block.ToolProgress.Text, "chars received") {
				t.Fatalf("expected transient char count, got %+v", block.ToolProgress)
			}

			_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
				ID: callID, Name: tc.name, AgentID: "agent-1", ArgsJSON: tc.args, ArgsStreamingDone: true,
			}})
			if block.ToolProgress != nil {
				t.Fatalf("expected char count cleared after arg streaming, got %+v", block.ToolProgress)
			}
			if joined := stripANSI(strings.Join(block.Render(96, "●"), "\n")); strings.Contains(joined, "chars received") {
				t.Fatalf("expected completed args to hide char count, got:\n%s", joined)
			}
		})
	}
}

func TestToolDoesNotShowCharCountWhenNoArgsReceived(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-zero-progress-1",
		Name:     "shell",
		AgentID:  "",
		ArgsJSON: ``,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-zero-progress-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("did not expect char count when no args have been received; got:\n%s", joined)
	}
}

func TestWriteToolHidesReceivedCharCountWhenExecutionStarts(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-write-progress-hide-1",
		Name:     "write",
		AgentID:  "",
		ArgsJSON: `{"path":"demo.txt","content":"hello world"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-write-progress-hide-1",
		Name:     "write",
		AgentID:  "",
		ArgsJSON: `{"path":"demo.txt","content":"hello world"}`,
		State:    agent.ToolCallExecutionStateRunning,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-write-progress-hide-1")
	if !ok {
		t.Fatal("expected write tool block")
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected write temp char count to be cleared, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("expected write tool to hide temp char count after execution starts; got:\n%s", joined)
	}
}

func TestToolUpdateAfterExecutionStartsDoesNotRestoreReceivedCharCount(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	initialArgs := `{"command":"echo hello"}`
	finalArgs := `{"command":"echo hello world"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-shell-progress-after-running-1",
		Name:     "shell",
		AgentID:  "",
		ArgsJSON: initialArgs,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-shell-progress-after-running-1",
		Name:     "shell",
		AgentID:  "",
		ArgsJSON: initialArgs,
		State:    agent.ToolCallExecutionStateRunning,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-shell-progress-after-running-1",
		Name:     "shell",
		AgentID:  "",
		ArgsJSON: finalArgs,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-shell-progress-after-running-1")
	if !ok {
		t.Fatal("expected shell tool block")
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected post-running arg update not to restore temp char count, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("expected post-running arg update to keep temp char count hidden; got:\n%s", joined)
	}
	if !strings.Contains(block.Content, "echo hello world") {
		t.Fatalf("expected post-running arg update to refresh content, got %q", block.Content)
	}
}

func TestEditToolHidesReceivedCharCountWhenExecutionStarts(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-patch-progress-hide-1",
		Name:     tools.NameEdit,
		AgentID:  "",
		ArgsJSON: `{"path":"demo.txt","patch":"@@\n-old\n+abcdef\n"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-patch-progress-hide-1",
		Name:     tools.NameEdit,
		AgentID:  "",
		ArgsJSON: `{"path":"demo.txt","patch":"@@\n-old\n+abcdef\n"}`,
		State:    agent.ToolCallExecutionStateRunning,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-patch-progress-hide-1")
	if !ok {
		t.Fatal("expected Edit tool block")
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected Edit temp char count to be cleared, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("expected Edit tool to hide temp char count after execution starts; got:\n%s", joined)
	}
}
