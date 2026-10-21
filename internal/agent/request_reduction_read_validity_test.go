package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestPrepareMessagesForLLM_ReducedReadBecomesStaleAfterTailEdit(t *testing.T) {
	a := newReductionTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		MinToolResultsPrune:  1,
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  40,
		ShellSuccessAgeTurns: 9,
		ShellSuccessBytes:    1 << 20,
		MinIncrementalTokens: 1 << 20,
	}}}
	a.newTurn("")
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", ToolStatus: "success", Content: "READ_RESULT lines=1-100 total=100\n" + strings.Repeat("source line\n", 100)},
	}
	setTestRequestBatch(a, msgs, 2)
	first := a.prepareMessagesForLLM(msgs)
	if first[2].Content != msgs[2].Content {
		t.Fatalf("still-valid read should stay full before invalidation, got %q", first[2].Content)
	}
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "edit", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"y"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "edit", ToolStatus: "success", Content: "edited"},
	)
	setTestRequestBatch(a, msgs, 3)
	second := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(second[2].Content, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("reduced read marker was not invalidated after edit: %q", second[2].Content)
	}
}

func TestPrepareMessagesForLLM_ReducedReadBecomesStaleAfterMutatingShell(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "a.go")
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile initial: %v", err)
	}
	a := newReductionTestMainAgent(t, projectRoot)
	a.tools.Register(tools.NewShellTool("bash"))
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		MinToolResultsPrune:  1,
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  40,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1 << 20,
		MinIncrementalTokens: 1 << 20,
	}}}
	a.newTurn("")
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", ToolStatus: "success", Content: "READ_RESULT lines=1-100 total=100\n" + strings.Repeat("source line\n", 100), FileState: readFileStateForTest(path)},
	}
	setTestRequestBatch(a, msgs, 2)
	first := a.prepareMessagesForLLM(msgs)
	if first[2].Content != msgs[2].Content {
		t.Fatalf("still-valid read should stay full before invalidation, got %q", first[2].Content)
	}
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile changed: %v", err)
	}
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "shell", Name: tools.NameShell, Args: json.RawMessage(`{"command":"sed -i '' 's/1/2/' a.go"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "shell", ToolStatus: "success", Content: "ok"},
	)
	setTestRequestBatch(a, msgs, 3)
	second := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(second[2].Content, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("read marker after mutating shell = %q, want stale", second[2].Content)
	}
}

func TestPrepareMessagesForLLM_ShellInvalidationVerdictMemoizedPerShell(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "a.go")
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile initial: %v", err)
	}
	a := newReductionTestMainAgent(t, projectRoot)
	a.tools.Register(tools.NewShellTool("bash"))
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		MinToolResultsPrune:  1,
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  40,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1 << 20,
		MinIncrementalTokens: 1 << 20,
	}}}
	a.newTurn("")
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", ToolStatus: "success", Content: "READ_RESULT lines=1-100 total=100\n" + strings.Repeat("source line\n", 100), FileState: readFileStateForTest(path)},
		{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "shell1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"touch other"}`)}}},
		{Role: message.RoleTool, ToolCallID: "shell1", ToolStatus: "success", Content: "ok"},
	}
	setTestRequestBatch(a, msgs, 3)
	first := a.prepareMessagesForLLM(msgs)
	if first[2].Content != msgs[2].Content {
		t.Fatalf("unchanged read after harmless shell should stay full, got %q", first[2].Content)
	}
	// F2 lazy validation: an external edit without a new mutating shell is
	// still detected — the read is stat-checked each request and re-hashed
	// when the mtime/size changed, so the model stops trusting stale content.
	// The edit keeps the byte length identical, so only a stat change can
	// reveal it; bump mtime explicitly so detection does not depend on the
	// filesystem's timestamp granularity.
	beforeEdit, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile changed: %v", err)
	}
	editedAt := beforeEdit.ModTime().Add(time.Second)
	if err := os.Chtimes(path, editedAt, editedAt); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	second := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(second[2].Content, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("external edit without a new mutating shell must be detected by lazy validation, got %q", second[2].Content)
	}
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, RequestBatch: 3, ToolCalls: []message.ToolCall{{ID: "shell2", Name: tools.NameShell, Args: json.RawMessage(`{"command":"sed -i '' 's/1/2/' a.go"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "shell2", ToolStatus: "success", Content: "ok"},
	)
	setTestRequestBatch(a, msgs, 4)
	third := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(third[2].Content, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("new mutating shell must re-verify, read marker = %q, want stale", third[2].Content)
	}
}

func TestMutatingShellInvalidationSkipsReadOnlyShells(t *testing.T) {
	// Only a shell the classifier rejects may trigger the
	// re-verify earlier reads against disk. An expanded-table query (rg)
	// must not, even when the earlier read is stale on disk.
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "a.go")
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile initial: %v", err)
	}
	readState := readFileStateForTest(path)
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile changed: %v", err)
	}
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	a := &MainAgent{tools: registry, contentRoot: projectRoot}
	messagesFor := func(callID, shellCommand string) []message.Message {
		return []message.Message{
			{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
			{Role: message.RoleTool, ToolCallID: "read", ToolStatus: message.ToolStatusSuccess, FileState: readState},
			{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: callID, Name: tools.NameShell, Args: json.RawMessage(`{"command":` + strconv.Quote(shellCommand) + `}`)}}},
			{Role: message.RoleTool, ToolCallID: callID, ToolStatus: message.ToolStatusSuccess},
		}
	}
	// NOTE: each case needs its own shell ToolCallID: the verdict memo keys
	// on call ID (production IDs are unique per call), so reusing one would
	// return the first case's verdict for the second.
	msgs := messagesFor("shell-rg", "rg --no-config -n value a.go")
	if got := a.externallyInvalidatedReadsAfterMutatingShell(msgs, requesttrim.NewScan(msgs, nil)); len(got) != 0 {
		t.Fatalf("read-only rg shell invalidated reads %v, want none", got)
	}
	msgs = messagesFor("shell-touch", "touch other")
	if got := a.externallyInvalidatedReadsAfterMutatingShell(msgs, requesttrim.NewScan(msgs, nil)); !got[1] {
		t.Fatalf("mutating shell invalidated reads %v, want index 1", got)
	}
}

func TestPrepareMessagesForLLM_RestoredReadBecomesStaleAfterMutatingShell(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "a.go")
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile initial: %v", err)
	}
	readState := readFileStateForTest(path)
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile changed: %v", err)
	}
	a := newReductionTestMainAgent(t, projectRoot)
	a.tools.Register(tools.NewShellTool("bash"))
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		MinToolResultsPrune:  1,
		ReadLikeAgeTurns:     99,
		ReadLikeOutputBytes:  40,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1 << 20,
		MinIncrementalTokens: 1,
	}}}
	a.newTurn("")
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", ToolStatus: "success", Content: "READ_RESULT lines=1-100 total=100\n" + strings.Repeat("source line\n", 100), FileState: readState},
		{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "shell", Name: tools.NameShell, Args: json.RawMessage(`{"command":"sed -i '' 's/1/2/' a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "shell", ToolStatus: "success", Content: "ok"},
	}
	setTestRequestBatch(a, msgs, 3)
	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("restored read marker after mutating shell = %q, want stale", prepared[2].Content)
	}
}

func TestPrepareMessagesForLLM_RestoredShellOnlyInvalidatesEarlierReads(t *testing.T) {
	projectRoot := t.TempDir()
	pathA := filepath.Join(projectRoot, "a.go")
	pathB := filepath.Join(projectRoot, "b.go")
	for _, path := range []string{pathA, pathB} {
		if err := os.WriteFile(path, []byte("package main\n\nconst value = 1\n"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", path, err)
		}
	}
	stateA := readFileStateForTest(pathA)
	if err := os.WriteFile(pathA, []byte("package main\n\nconst value = 2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile changed A: %v", err)
	}
	stateB := readFileStateForTest(pathB)
	a := newReductionTestMainAgent(t, projectRoot)
	a.tools.Register(tools.NewShellTool("bash"))
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		MinToolResultsPrune:  1,
		ReadLikeAgeTurns:     99,
		ReadLikeOutputBytes:  40,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1 << 20,
		MinIncrementalTokens: 1,
	}}}
	a.newTurn("")
	content := "READ_RESULT lines=1-100 total=100\n" + strings.Repeat("source line\n", 100)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "read-a", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read-a", ToolStatus: "success", Content: content, FileState: stateA},
		{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "shell", Name: tools.NameShell, Args: json.RawMessage(`{"command":"sed -i '' 's/1/2/' a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "shell", ToolStatus: "success", Content: "ok"},
		{Role: message.RoleAssistant, RequestBatch: 3, ToolCalls: []message.ToolCall{{ID: "read-b", Name: tools.NameRead, Args: json.RawMessage(`{"path":"b.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read-b", ToolStatus: "success", Content: content, FileState: stateB},
	}
	setTestRequestBatch(a, msgs, 4)
	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("read before shell = %q, want stale", prepared[2].Content)
	}
	if strings.Contains(prepared[6].Content, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("read after shell = %q, must remain current", prepared[6].Content)
	}
}
