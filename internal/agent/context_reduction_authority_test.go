package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/tools"
)

// authorityCase is one row of the authority matrix: an original tool result,
// the representation it is allowed to take in a request, and the properties a
// reader of that representation may rely on. The matrix exists so the four
// concepts that used to be implicit in the marker text — original fact vs
// summary, current state vs historical observation, recoverable vs
// semantically trustworthy, request surface vs durable transcript — are
// asserted per shape instead of argued per bug report.
type authorityCase struct {
	name string
	// authority names where the information's truth lives. A representation
	// may never claim more authority than this.
	authority string
	ctx       requestReductionContext

	wantClass    requestReductionClass
	wantLevel    retentionLevel
	wantRecovery retentionRecovery
	// wantReason is only checked for results kept complete, where the reason
	// names which protection fired.
	wantReason string
	// worstCase records what a misclassification of this row costs. It is
	// documentation with a test around it: every row must state the damage,
	// so a future relaxation of a rule has to argue against a written cost.
	worstCase string
}

func authorityMatrixCases(t *testing.T, sessionDir string) []authorityCase {
	t.Helper()
	policy := defaultContextReductionPolicy()
	readContent := "READ_RESULT lines=1-400 total=400\n" + strings.Repeat("source line\n", 400)
	shellSuccess := strings.Repeat("processed item ok\n", 300)
	readOnlyShell := strings.Repeat("internal/agent/main.go\n", 200)
	buildLog := strings.Repeat("go: downloading module cache entry\n", 40) +
		strings.Repeat("compile error: undefined symbol in package\n", 40)
	jsonBlob := `{"items":[` + strings.Repeat(`{"id":1,"name":"alpha"},`, 200) + `{"id":2,"name":"omega"}]}`
	diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,4 +1,4 @@\n" +
		strings.Repeat("-old line\n+new line\n", 300)
	diagnostics := "Edit applied.\n" + diagnosticsSectionLabel + "\n" +
		strings.Repeat("internal/agent/main.go:9:2: declared and not used\n", 120)
	// An edit result whose body only mentions the diagnostics label without the
	// section framing the renderer needs: the routing gate matches on the label
	// alone, so this reaches the diagnostics class and its renderer declines.
	labelMention := "Patch applied to internal/lsp/tool_output.go.\n" +
		strings.Repeat(`const DiagnosticsSectionMarker = "\n\n`+diagnosticsSectionLabel+`\n"`+"\n", 80)
	genericOutput := strings.Repeat("record without recognizable shape\n", 100)
	jobOutput := strings.Repeat("subagent transcript line\n", 100)
	errorOutput := strings.Repeat("build failed: cannot find package\n", 100)

	return []authorityCase{
		{
			name:      "current read stays the file's authoritative view",
			authority: "file system revision",
			ctx: requestReductionContext{
				ToolName: tools.NameRead, Meta: toolCallMeta{Name: tools.NameRead, Args: `{"path":"main.go"}`},
				Content: readContent, ToolStatus: "success", Age: policy.StaleAgeTurns + 10,
				Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionNone, wantLevel: retentionFull,
			wantRecovery: retentionRecoveryNone, wantReason: retentionReasonCurrentRead,
			worstCase: "the model answers about file content from a summary it cannot verify, or pays a re-read for bytes it already has",
		},
		{
			name:      "superseded read points at the newer read of the same range",
			authority: "file system revision",
			ctx: requestReductionContext{
				ToolName: tools.NameRead, Meta: toolCallMeta{Name: tools.NameRead, Args: `{"path":"main.go"}`},
				Content: readContent, Age: 1, Policy: policy, ReadSuperseded: true, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionReadLike, wantLevel: retentionStructured,
			wantRecovery: retentionRecoveryRereadFile,
			worstCase:    "two renderings of the same range disagree and the model edits against the older one",
		},
		{
			name:      "invalidated read whose bytes survive on disk is re-readable",
			authority: "file system revision",
			ctx: requestReductionContext{
				ToolName: tools.NameRead, Meta: toolCallMeta{Name: tools.NameRead, Args: `{"path":"main.go"}`},
				Content: readContent, Age: 0, Policy: policy, ReadInvalidated: true, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionReadLike, wantLevel: retentionStructured,
			wantRecovery: retentionRecoveryRereadFile,
			worstCase:    "stale file content keeps reading as the current file and the model edits from a revision that no longer exists",
		},
		{
			name:      "invalidated read whose prior content is lost gets an address",
			authority: "durable transcript (only surviving copy)",
			ctx: requestReductionContext{
				ToolName: tools.NameRead, Meta: toolCallMeta{Name: tools.NameRead, Args: `{"path":"main.go"}`},
				Content: readContent, Age: 0, Policy: policy,
				ReadInvalidated: true, ReadPriorContentLost: true, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionReadLike, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "the only record of the pre-mutation revision is destroyed with no way to get it back",
		},
		{
			name:      "repeated output defers to the identical later call",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell},
				Content: shellSuccess, Age: 2, Policy: policy, Repeated: true, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionRepeated, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "a marker claims a later identical copy that does not exist and the output is gone",
		},
		{
			name:      "aged shell success keeps its outcome lines",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"make build"}`},
				Content: shellSuccess, Age: policy.ShellSuccessAgeTurns, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionShellOK, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "a failure hidden in a nominally successful run is summarized away",
		},
		{
			name:      "read-only shell is protected like a read until its own age",
			authority: "file system (via command)",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"ls internal/agent"}`},
				Content: readOnlyShell, Age: policy.ShellReadOnlyAgeTurns - 1, Policy: policy,
				ShellReadOnly: true, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionNone, wantLevel: retentionFull,
			wantRecovery: retentionRecoveryNone, wantReason: retentionReasonReadOnlyShell,
			worstCase: "content the model fetched to work from is trimmed before it is used, forcing an immediate re-fetch",
		},
		{
			name:      "edit diagnostics stay complete while the model iterates",
			authority: "language server state",
			ctx: requestReductionContext{
				ToolName: tools.NameEdit, Meta: toolCallMeta{Name: tools.NameEdit},
				Content: diagnostics, Age: policy.ErrorAgeTurns - 1, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionNone, wantLevel: retentionFull,
			wantRecovery: retentionRecoveryNone, wantReason: retentionReasonRecentDiagnostics,
			worstCase: "the feedback the model is fixing against disappears mid-fix",
		},
		{
			name:      "aged diagnostics keep their structured body",
			authority: "language server state",
			ctx: requestReductionContext{
				ToolName: tools.NameEdit, Meta: toolCallMeta{Name: tools.NameEdit},
				Content: diagnostics, Age: policy.ErrorAgeTurns, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionDiagnostics, wantLevel: retentionStructured,
			wantRecovery: retentionRecoveryRerunTool,
			worstCase:    "a diagnostics block is misread as a build log and its per-file structure is lost",
		},
		{
			name:      "an output that only mentions the diagnostics label is archived, not dropped",
			authority: "durable transcript (only surviving copy)",
			ctx: requestReductionContext{
				ToolName: tools.NameApplyPatch, Meta: toolCallMeta{Name: tools.NameApplyPatch},
				Content: labelMention, Age: policy.HighRiskProtectAgeTurns, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionDiagnostics, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "the diagnostics exemption from the archive gate is granted to a rendering that produced no diagnostics body, so the whole tool output is dropped with neither excerpt nor address",
		},
		{
			name:      "recent diff is review evidence, not a log",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"git diff"}`},
				Content: diff, Age: policy.DiffProtectAgeTurns - 1, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionNone, wantLevel: retentionFull,
			wantRecovery: retentionRecoveryNone, wantReason: retentionReasonRecentDiff,
			worstCase: "the change under review is summarized to file names while the review is still happening",
		},
		{
			name:      "aged diff collapses to a per-file review summary",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"git diff"}`},
				Content: diff, Age: policy.DiffProtectAgeTurns, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionDiff, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "source identifiers such as \"error\" turn a patch into a misleading log summary",
		},
		{
			name:      "aged failure keeps its failure lines",
			authority: "tool status",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"make"}`},
				Content: errorOutput, ToolStatus: "error", Age: policy.HighRiskProtectAgeTurns, Policy: policy,
				ArchiveDir: sessionDir,
			},
			wantClass: requestReductionToolError, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "the model retries a fix against a failure whose message it can no longer see",
		},
		{
			name:      "recent failure is stronger evidence than any shape rule",
			authority: "tool status",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"make"}`},
				Content: errorOutput, ToolStatus: "error", Age: 0, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionNone, wantLevel: retentionFull,
			wantRecovery: retentionRecoveryNone, wantReason: retentionReasonRecentHighRisk,
			worstCase: "the exact failure is summarized away in the same turn the model is about to act on it",
		},
		{
			name:      "aged JSON keeps its skeleton",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"curl -s http://example/api"}`},
				Content: jsonBlob, Age: policy.StaleAgeTurns, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionJSON, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "values the model consumes over several requests are replaced by a key list with no way back",
		},
		{
			name:      "JSON waits for the stale age before losing its values",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"curl -s http://example/api"}`},
				Content: jsonBlob, Age: policy.StaleAgeTurns - 1, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionNone, wantLevel: retentionFull,
			wantRecovery: retentionRecoveryNone, wantReason: retentionReasonJSONAwaitsStale,
			worstCase: "the lossiest summary shape fires while the model is still reading values out of the document",
		},
		{
			// The reason has to come from the branch that actually returned.
			// This payload is JSON-shaped but below every byte gate, so no rule
			// reaches it at all; reporting the JSON retention rule here would
			// credit a protection that never ran and hide that the result is
			// simply too small to be worth reducing.
			name:      "a payload below every byte gate is reported as unmatched, not as awaiting an age",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: "custom_tool", Meta: toolCallMeta{Name: "custom_tool"},
				Content: `{"status":"ok","items":[{"id":1},{"id":2}],"total":2}`,
				Age:     policy.StaleAgeTurns - 1, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionNone, wantLevel: retentionFull,
			wantRecovery: retentionRecoveryNone, wantReason: retentionReasonNoRuleMatched,
			worstCase: "the retention ledger names a protection that never fired, so a reader tunes the rule that is not holding the result",
		},
		{
			name:      "aged build log keeps its signal lines",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"make"}`},
				Content: buildLog, Age: policy.HighRiskProtectAgeTurns, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionLongLog, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "the error lines that explain a failed build are dropped along with the noise",
		},
		{
			name:      "generic output keeps an excerpt and an address",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: "custom_tool", Meta: toolCallMeta{Name: "custom_tool"},
				Content: genericOutput, Age: policy.StaleAgeTurns, Policy: policy,
				ToolResults: policy.MinToolResultsPrune, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionGeneric, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "an unrecognized payload is replaced by a marker with neither content nor address",
		},
		{
			name:      "job output is archived because it cannot be replayed",
			authority: "durable transcript (one-shot)",
			ctx: requestReductionContext{
				ToolName: tools.NameJobOutput, Meta: toolCallMeta{Name: tools.NameJobOutput},
				Content: jobOutput, Age: policy.StaleAgeTurns, Policy: policy,
				ToolResults: policy.MinToolResultsPrune, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionGeneric, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "a subagent result with no command to re-run and no URL to re-fetch is lost outright",
		},
		{
			name:      "an existing artifact reference is carried forward",
			authority: "session artifact",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"make"}`},
				Content: shellSuccess + "\n" + tools.ArtifactReferencePrefix + sessionDir + "/tool-outputs/call_1.log. " + tools.ArtifactReadGuidance,
				Age:     policy.ShellSuccessAgeTurns, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionShellOK, wantLevel: retentionArchived,
			wantRecovery: retentionRecoveryReadArtifact,
			worstCase:    "the tool layer's own address is dropped by the reduction layer, orphaning the archived payload",
		},
	}
}

// TestContextReductionAuthorityMatrix pins the authority matrix: every row
// states what a request-surface rendering is allowed to claim about the tool
// result it replaces, and the invariants below hold for all of them at once.
func TestContextReductionAuthorityMatrix(t *testing.T) {
	sessionDir := t.TempDir()
	for _, test := range authorityMatrixCases(t, sessionDir) {
		t.Run(test.name, func(t *testing.T) {
			if test.worstCase == "" || test.authority == "" {
				t.Fatal("every matrix row must state its authority and the cost of misclassifying it")
			}
			verdict := classifyRequestReduction(test.ctx)
			class := verdict.Class
			if class != test.wantClass {
				t.Fatalf("class = %q, want %q", class, test.wantClass)
			}
			reduced, rule := "", ""
			if class != requestReductionNone {
				var ok bool
				reduced, rule, ok = reduceRequestToolOutput(class, test.ctx)
				if !ok {
					t.Fatalf("reduceRequestToolOutput(%q) declined to render", class)
				}
				if len(reduced) >= len(test.ctx.Content) {
					t.Fatalf("rendering is not smaller than the payload: %d >= %d", len(reduced), len(test.ctx.Content))
				}
			}
			decision := retentionDecisionFor(test.ctx, verdict, rule, reduced)
			if decision.Level != test.wantLevel {
				t.Fatalf("level = %q, want %q (rendering: %q)", decision.Level, test.wantLevel, firstLine(reduced))
			}
			if decision.Recovery != test.wantRecovery {
				t.Fatalf("recovery = %q, want %q", decision.Recovery, test.wantRecovery)
			}
			if test.wantReason != "" && decision.Reason != test.wantReason {
				t.Fatalf("reason = %q, want %q", decision.Reason, test.wantReason)
			}

			// Invariants shared by every row.
			if want := class == requestReductionNone; decision.Complete != want {
				t.Fatalf("complete = %v, want %v", decision.Complete, want)
			}
			if !decision.retentionDecisionRecoverable() {
				t.Fatalf("lossy rendering left no recovery route: %+v", decision)
			}
			if decision.Recovery == retentionRecoveryReadArtifact {
				assertArtifactAddressResolves(t, sessionDir, decision.ArtifactRef, reduced)
			}
			// The red line, asserted on the rendering rather than on a derived
			// label: a rendering that dropped payload must still carry either
			// lines of it or the address it was archived at. A single-line
			// marker with no address is the shape that destroys work.
			if class != requestReductionNone && class != requestReductionConfirm &&
				!strings.Contains(strings.TrimSpace(reduced), "\n") && decision.ArtifactRef == "" {
				t.Fatalf("lossy rendering kept neither an excerpt nor an address: %q", reduced)
			}
		})
	}
}

// TestAuthorityMatrixRenderingsAreStable covers the property the fallback
// path depends on. Re-admitting a request for a different target model rebuilds
// the surface from the durable transcript, so the same input must always render
// the same bytes; and if the rebuilt surface is ever reduced again, the second
// rendering must not be worse than the first — a summary of a summary that
// dropped the recovery address would strand the payload.
func TestAuthorityMatrixRenderingsAreStable(t *testing.T) {
	sessionDir := t.TempDir()
	for _, test := range authorityMatrixCases(t, sessionDir) {
		t.Run(test.name, func(t *testing.T) {
			verdict := classifyRequestReduction(test.ctx)
			class := verdict.Class
			if class == requestReductionNone {
				return
			}
			reduced, rule, ok := reduceRequestToolOutput(class, test.ctx)
			if !ok {
				t.Fatalf("reduceRequestToolOutput(%q) declined to render", class)
			}
			again, _, ok := reduceRequestToolOutput(class, test.ctx)
			if !ok || again != reduced {
				t.Fatalf("rendering is not deterministic:\n%q\nvs\n%q", firstLine(reduced), firstLine(again))
			}
			first := retentionDecisionFor(test.ctx, verdict, rule, reduced)

			// Now feed the marker back in, as a surface rebuild would if the
			// frozen prefix ever carried it into a fresh pass.
			next := test.ctx
			next.Content = reduced
			next.Age = test.ctx.Age + 1
			reVerdict := classifyRequestReduction(next)
			reClass := reVerdict.Class
			if reClass == requestReductionNone {
				return
			}
			reReduced, reRule, ok := reduceRequestToolOutput(reClass, next)
			if !ok {
				return
			}
			second := retentionDecisionFor(next, reVerdict, reRule, reReduced)
			if !second.retentionDecisionRecoverable() {
				t.Fatalf("re-reducing a marker left no recovery route: %+v", second)
			}
			if first.ArtifactRef != "" && second.ArtifactRef == "" {
				t.Fatalf("re-reducing a marker dropped its recovery address: %q", firstLine(reReduced))
			}
		})
	}
}

// assertArtifactAddressResolves checks the promise a recovery address makes:
// the file exists, holds bytes, and lives under the session directory that
// compaction exports and restores with the session.
func assertArtifactAddressResolves(t *testing.T, sessionDir, ref, reduced string) {
	t.Helper()
	if ref == "" {
		t.Fatalf("decision claims an artifact recovery route but carries no reference: %q", firstLine(reduced))
	}
	path := strings.TrimSuffix(strings.TrimPrefix(ref, tools.ArtifactReferencePrefix), ".")
	if idx := strings.Index(path, ". "); idx > 0 {
		path = path[:idx]
	}
	if !strings.HasPrefix(path, sessionDir) {
		t.Fatalf("artifact address escapes the session directory: %q", path)
	}
	info, err := privatefs.Stat(sessionDir, path)
	if err != nil {
		if os.IsNotExist(err) && strings.Contains(path, toolOutputDirName) {
			// The tool layer wrote this address before reduction ran; the
			// fixture only asserts it is carried forward verbatim.
			return
		}
		t.Fatalf("artifact address does not resolve: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("artifact address resolves to an empty file: %q", path)
	}
}

func firstLine(s string) string {
	head, _, _ := strings.Cut(s, "\n")
	return head
}
