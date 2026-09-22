package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// The tests in this file drive the reduction layer across several requests
// instead of asserting one rule in isolation. Single-rule tests keep passing
// while the sequence they compose into loses information: a read that is
// correctly summarized when stale is still wrong if the same request also
// claims it is current, and an archived payload is only recoverable if the
// address survives the request that wrote it.
//
// Every flow below ends in assertRequestSurfaceInvariants, which holds for any
// surface this layer may produce.

// assertRequestSurfaceInvariants checks the properties no reduction may break:
// tool calls stay paired with their results, the newest user request stays
// verbatim, no stale read is presented as current, and every recovery address
// resolves to real bytes.
func assertRequestSurfaceInvariants(t *testing.T, prepared []message.Message, sessionDir string) {
	t.Helper()
	for i := range prepared {
		if prepared[i].Role == message.RoleTool && !toolResultSupportedByNearestAssistant(prepared, i) {
			t.Fatalf("tool result at %d lost its call: %q", i, compactTextSnippet(prepared[i].Content, 80))
		}
	}
	lastUser := -1
	for i := range prepared {
		if prepared[i].Role == message.RoleUser {
			lastUser = i
		}
	}
	if lastUser >= 0 && strings.Contains(prepared[lastUser].Content, "omitted from this request") {
		t.Fatalf("the newest user request was reduced: %q", prepared[lastUser].Content)
	}
	for i := range prepared {
		content := prepared[i].Content
		for _, ref := range tools.ExtractArtifactReferences(content) {
			assertArtifactAddressResolves(t, sessionDir, ref, content)
		}
		if address, ok := archivedOutputMarkerAddress(content); ok {
			if _, err := os.Stat(address); err != nil {
				t.Fatalf("archived output address does not resolve: %v", err)
			}
		}
	}
}

func stateMachineReadBody(start, end, total int, line string) string {
	var b strings.Builder
	b.WriteString(tools.FormatReadResultHeader(strconv.Itoa(start)+"-"+strconv.Itoa(end), total, "", "", ""))
	for i := start; i <= end; i++ {
		b.WriteString("\n")
		b.WriteString(line)
	}
	return b.String()
}

// TestStateMachineReadEditReread walks read -> edit -> reread. The first read
// observes a revision the edit then replaces; from that point the request must
// stop presenting it as the file's current content, while the reread that
// follows keeps full authority.
func TestStateMachineReadEditReread(t *testing.T) {
	sessionDir := t.TempDir()
	a := &MainAgent{parentCtx: context.Background(), sessionDir: sessionDir}
	a.newTurn()
	original := stateMachineReadBody(1, 400, 400, "alpha")

	msgs := []message.Message{
		{Role: message.RoleUser, Content: "update a.go"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "r1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r1", Content: original, ToolStatus: "success"},
	}

	// Request 1: the read is the only view of the file and stays complete.
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != original {
		t.Fatalf("a current read must not be reduced: %q", compactTextSnippet(prepared[2].Content, 120))
	}
	assertRequestSurfaceInvariants(t, prepared, sessionDir)

	// Request 2: an edit replaces the observed revision. The read is now a
	// historical observation and must say so.
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "e1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.go\n@@\n-alpha\n+beta\n*** End Patch"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "e1", Content: "Applied patch to a.go (+1 -1)", ToolStatus: "success"},
		message.Message{Role: message.RoleUser, Content: "now verify it"},
	)
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == original {
		t.Fatal("a read invalidated by a later edit must not stay presented as current content")
	}
	if !strings.Contains(prepared[2].Content, "truncated=") {
		t.Fatalf("invalidated read lost its validity note: %q", compactTextSnippet(prepared[2].Content, 160))
	}
	assertRequestSurfaceInvariants(t, prepared, sessionDir)

	// Request 3: the reread is the current view and takes full authority back,
	// while the superseded first read keeps its marker.
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "r2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "r2", Content: stateMachineReadBody(1, 400, 400, "beta"), ToolStatus: "success"},
		message.Message{Role: message.RoleUser, Content: "looks right?"},
	)
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[7].Content != msgs[7].Content {
		t.Fatalf("the newest read must keep full authority: %q", compactTextSnippet(prepared[7].Content, 120))
	}
	if prepared[2].Content == original {
		t.Fatal("the superseded read regained current-content presentation on a later request")
	}
	assertRequestSurfaceInvariants(t, prepared, sessionDir)
}

// TestStateMachineReadExternalMutationArchivesPriorContent walks
// read -> external mutation -> reduction. A mutating shell command leaves no
// copy of what the read observed: the transcript's read output is the only
// record, so its summary must carry an address rather than a marker.
func TestStateMachineReadExternalMutationArchivesPriorContent(t *testing.T) {
	sessionDir := t.TempDir()
	projectRoot := t.TempDir()
	filePath := filepath.Join(projectRoot, "a.go")
	if err := os.WriteFile(filePath, []byte("alpha\n"), 0o600); err != nil {
		t.Fatalf("seeding the file: %v", err)
	}
	observed := sha256.Sum256([]byte("alpha\n"))

	reg := tools.NewRegistry()
	reg.Register(tools.ShellTool{})
	a := &MainAgent{parentCtx: context.Background(), sessionDir: sessionDir, contentRoot: projectRoot, tools: reg}
	a.newTurn()
	original := stateMachineReadBody(1, 400, 400, "alpha")

	msgs := []message.Message{
		{Role: message.RoleUser, Content: "regenerate a.go"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "r1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{
			Role: message.RoleTool, ToolCallID: "r1", Content: original, ToolStatus: "success",
			FileState: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: "a.go", SHA256: hex.EncodeToString(observed[:]), Exists: true}}},
		},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "s1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"./codegen.sh > a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "s1", Content: "generated 1 file", ToolStatus: "success"},
		{Role: message.RoleUser, Content: "what changed?"},
	}

	// The command rewrote the file: nothing in the transcript holds the bytes
	// the read observed.
	if err := os.WriteFile(filePath, []byte("beta\n"), 0o600); err != nil {
		t.Fatalf("rewriting the file: %v", err)
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == original {
		t.Fatal("a read invalidated by a mutating shell command must not stay presented as current content")
	}
	decisionRef := ""
	if refs := tools.ExtractArtifactReferences(prepared[2].Content); len(refs) > 0 {
		decisionRef = refs[0]
	}
	if decisionRef == "" {
		t.Fatalf("the only surviving copy of the observed revision was summarized without an address: %q", compactTextSnippet(prepared[2].Content, 200))
	}
	assertArtifactAddressResolves(t, sessionDir, decisionRef, prepared[2].Content)

	// The archived bytes must be the read output itself, not the summary.
	path := strings.TrimSuffix(strings.TrimPrefix(decisionRef, tools.ArtifactReferencePrefix), ".")
	if idx := strings.Index(path, ". "); idx > 0 {
		path = path[:idx]
	}
	archived, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the archived payload: %v", err)
	}
	if string(archived) != original {
		t.Fatalf("archived payload is not the original read output (%d vs %d bytes)", len(archived), len(original))
	}
	assertRequestSurfaceInvariants(t, prepared, sessionDir)

	// The address must survive later requests: a recovery route that only
	// exists on the request that created it is not a recovery route.
	msgs = append(msgs, message.Message{Role: message.RoleUser, Content: "and now?"})
	prepared = a.prepareMessagesForLLM(msgs)
	if len(tools.ExtractArtifactReferences(prepared[2].Content)) == 0 {
		t.Fatalf("recovery address was dropped on a later request: %q", compactTextSnippet(prepared[2].Content, 200))
	}
	assertRequestSurfaceInvariants(t, prepared, sessionDir)
}

// TestStateMachineFailedBuildFixRebuild walks failed build -> fix -> rebuild.
// The failure must stay complete while the model is acting on it, keep its
// failure lines once it ages, and never be resurrected as current state by the
// successful rebuild that follows.
func TestStateMachineFailedBuildFixRebuild(t *testing.T) {
	sessionDir := t.TempDir()
	reg := tools.NewRegistry()
	reg.Register(tools.ShellTool{})
	a := &MainAgent{parentCtx: context.Background(), sessionDir: sessionDir, tools: reg}
	a.newTurn()
	failure := strings.Repeat("internal/agent/main.go: undefined: helperFn\n", 120)

	msgs := []message.Message{
		{Role: message.RoleUser, Content: "make the build pass"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "b1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go build ./..."}`)}}},
		{Role: message.RoleTool, ToolCallID: "b1", Content: failure, ToolStatus: "error"},
	}
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != failure {
		t.Fatalf("a fresh failure must stay complete: %q", compactTextSnippet(prepared[2].Content, 120))
	}
	assertRequestSurfaceInvariants(t, prepared, sessionDir)

	// The fix, then the rebuild, then enough turns for the failure to age past
	// both the high-risk protection and the error threshold.
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "e1", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"internal/agent/main.go"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "e1", Content: "Edit applied", ToolStatus: "success"},
		message.Message{Role: message.RoleUser, Content: "rebuild"},
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "b2", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go build ./..."}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "b2", Content: "ok\n", ToolStatus: "success"},
		message.Message{Role: message.RoleUser, Content: "u3"},
		message.Message{Role: message.RoleUser, Content: "u4"},
		message.Message{Role: message.RoleUser, Content: "u5"},
		message.Message{Role: message.RoleUser, Content: "u6"},
	)
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == failure {
		t.Fatal("an aged failure should have been summarized once the fix landed")
	}
	if !strings.Contains(prepared[2].Content, "undefined: helperFn") {
		t.Fatalf("the summary dropped the failure evidence it exists to preserve: %q", compactTextSnippet(prepared[2].Content, 200))
	}
	if prepared[7].Content != "ok\n" {
		t.Fatalf("the successful rebuild must stay verbatim: %q", prepared[7].Content)
	}
	assertRequestSurfaceInvariants(t, prepared, sessionDir)
}

// TestStateMachineReferencedResultKeepsItsEvidence covers the dependency rule:
// when later assistant reasoning cites a result's specifics, the request must
// still contain those specifics — the identifier the reasoning names, or an
// address the model can read it back from. Otherwise the claim stays in the
// request while the evidence for it left.
func TestStateMachineReferencedResultKeepsItsEvidence(t *testing.T) {
	sessionDir := t.TempDir()
	a := &MainAgent{parentCtx: context.Background(), sessionDir: sessionDir}
	a.newTurn()
	output := "{\n" + strings.Repeat(`  "internal/agent/context_reduction.go": "ok",`+"\n", 100) + `  "internal/agent/last_entry.go": "ok"` + "\n}"

	msgs := []message.Message{
		{Role: message.RoleUser, Content: "audit the report"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "t1", Name: "custom_report", Args: json.RawMessage(`{"scope":"all"}`)}}},
		{Role: message.RoleTool, ToolCallID: "t1", Content: output, ToolStatus: "success"},
		{Role: message.RoleAssistant, Content: "The report flags internal/agent/context_reduction.go, so that file is the one to change."},
	}
	for i := range 6 {
		msgs = append(msgs,
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "n" + strconv.Itoa(i), Name: tools.NameShell, Args: json.RawMessage(`{"command":"echo ` + strconv.Itoa(i) + `"}`)}}},
			message.Message{Role: message.RoleTool, ToolCallID: "n" + strconv.Itoa(i), Content: strconv.Itoa(i) + "\n", ToolStatus: "success"},
			message.Message{Role: message.RoleUser, Content: "u" + strconv.Itoa(i)},
		)
	}

	const citedIdentifier = "internal/agent/context_reduction.go"
	prepared := a.prepareMessagesForLLM(msgs)
	rendering := prepared[2].Content
	if rendering == output {
		// The fixture ages the result past every gate on purpose. Returning
		// early here once let a policy change turn this test into a no-op.
		t.Fatal("the fixture no longer reduces the cited result; the rule it pins is untested")
	}
	if !strings.Contains(rendering, citedIdentifier) && !strings.Contains(rendering, reducedArtifactDirName) {
		t.Fatalf("the reasoning still cites %q but the request keeps neither the identifier nor an address for it: %q",
			citedIdentifier, compactTextSnippet(rendering, 300))
	}
	assertRequestSurfaceInvariants(t, prepared, sessionDir)
}
