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

	wantClass      requestReductionClass
	wantLevel      retentionLevel
	wantValidity   retentionValidity
	wantRecovery   retentionRecovery
	wantConfidence retentionConfidence
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
	genericOutput := strings.Repeat("record without recognizable shape\n", 100)
	spawnOutput := strings.Repeat("subagent transcript line\n", 100)
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
			wantValidity: retentionValidityCurrent, wantRecovery: retentionRecoveryNone,
			wantConfidence: retentionConfidenceVerified, wantReason: retentionReasonCurrentRead,
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
			wantValidity: retentionValiditySuperseded, wantRecovery: retentionRecoveryRereadFile,
			wantConfidence: retentionConfidenceVerified,
			worstCase:      "two renderings of the same range disagree and the model edits against the older one",
		},
		{
			name:      "invalidated read whose bytes survive on disk is re-readable",
			authority: "file system revision",
			ctx: requestReductionContext{
				ToolName: tools.NameRead, Meta: toolCallMeta{Name: tools.NameRead, Args: `{"path":"main.go"}`},
				Content: readContent, Age: 0, Policy: policy, ReadInvalidated: true, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionReadLike, wantLevel: retentionStructured,
			wantValidity: retentionValidityStale, wantRecovery: retentionRecoveryRereadFile,
			wantConfidence: retentionConfidenceVerified,
			worstCase:      "stale file content keeps reading as the current file and the model edits from a revision that no longer exists",
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
			wantValidity: retentionValidityStale, wantRecovery: retentionRecoveryReadArtifact,
			wantConfidence: retentionConfidenceVerified,
			worstCase:      "the only record of the pre-mutation revision is destroyed with no way to get it back",
		},
		{
			name:      "repeated output defers to the identical later call",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell},
				Content: shellSuccess, Age: 2, Policy: policy, Repeated: true, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionRepeated, wantLevel: retentionArchived,
			wantValidity: retentionValidityHistorical, wantRecovery: retentionRecoveryReadArtifact,
			wantConfidence: retentionConfidenceVerified,
			worstCase:      "a marker claims a later identical copy that does not exist and the output is gone",
		},
		{
			name:      "aged shell success keeps its outcome lines",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"make build"}`},
				Content: shellSuccess, Age: policy.ShellSuccessAgeTurns, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionShellOK, wantLevel: retentionArchived,
			wantValidity: retentionValidityHistorical, wantRecovery: retentionRecoveryReadArtifact,
			wantConfidence: retentionConfidenceInferred,
			worstCase:      "a failure hidden in a nominally successful run is summarized away",
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
			wantValidity: retentionValidityCurrent, wantRecovery: retentionRecoveryNone,
			wantConfidence: retentionConfidenceVerified, wantReason: retentionReasonReadOnlyShell,
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
			wantValidity: retentionValidityCurrent, wantRecovery: retentionRecoveryNone,
			wantConfidence: retentionConfidenceInferred, wantReason: retentionReasonRecentDiagnostics,
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
			wantValidity: retentionValidityHistorical, wantRecovery: retentionRecoveryRerunTool,
			wantConfidence: retentionConfidenceInferred,
			worstCase:      "a diagnostics block is misread as a build log and its per-file structure is lost",
		},
		{
			name:      "recent diff is review evidence, not a log",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"git diff"}`},
				Content: diff, Age: policy.DiffProtectAgeTurns - 1, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionNone, wantLevel: retentionFull,
			wantValidity: retentionValidityCurrent, wantRecovery: retentionRecoveryNone,
			wantConfidence: retentionConfidenceInferred, wantReason: retentionReasonRecentDiff,
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
			wantValidity: retentionValidityHistorical, wantRecovery: retentionRecoveryReadArtifact,
			wantConfidence: retentionConfidenceInferred,
			worstCase:      "source identifiers such as \"error\" turn a patch into a misleading log summary",
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
			wantValidity: retentionValidityHistorical, wantRecovery: retentionRecoveryReadArtifact,
			wantConfidence: retentionConfidenceVerified,
			worstCase:      "the model retries a fix against a failure whose message it can no longer see",
		},
		{
			name:      "recent failure is stronger evidence than any shape rule",
			authority: "tool status",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"make"}`},
				Content: errorOutput, ToolStatus: "error", Age: 0, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionNone, wantLevel: retentionFull,
			wantValidity: retentionValidityCurrent, wantRecovery: retentionRecoveryNone,
			wantConfidence: retentionConfidenceVerified, wantReason: retentionReasonRecentHighRisk,
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
			wantValidity: retentionValidityHistorical, wantRecovery: retentionRecoveryReadArtifact,
			wantConfidence: retentionConfidenceInferred,
			worstCase:      "values the model consumes over several requests are replaced by a key list with no way back",
		},
		{
			name:      "JSON waits for the stale age before losing its values",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"curl -s http://example/api"}`},
				Content: jsonBlob, Age: policy.StaleAgeTurns - 1, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionNone, wantLevel: retentionFull,
			wantValidity: retentionValidityCurrent, wantRecovery: retentionRecoveryNone,
			wantConfidence: retentionConfidenceInferred, wantReason: retentionReasonJSONAwaitsStale,
			worstCase: "the lossiest summary shape fires while the model is still reading values out of the document",
		},
		{
			name:      "aged build log keeps its signal lines",
			authority: "durable transcript",
			ctx: requestReductionContext{
				ToolName: tools.NameShell, Meta: toolCallMeta{Name: tools.NameShell, Args: `{"command":"make"}`},
				Content: buildLog, Age: policy.HighRiskProtectAgeTurns, Policy: policy, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionLongLog, wantLevel: retentionArchived,
			wantValidity: retentionValidityHistorical, wantRecovery: retentionRecoveryReadArtifact,
			wantConfidence: retentionConfidenceInferred,
			worstCase:      "the error lines that explain a failed build are dropped along with the noise",
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
			wantValidity: retentionValidityHistorical, wantRecovery: retentionRecoveryReadArtifact,
			wantConfidence: retentionConfidenceInferred,
			worstCase:      "an unrecognized payload is replaced by a marker with neither content nor address",
		},
		{
			name:      "spawn output is archived because it cannot be replayed",
			authority: "durable transcript (one-shot)",
			ctx: requestReductionContext{
				ToolName: tools.NameSpawn, Meta: toolCallMeta{Name: tools.NameSpawn},
				Content: spawnOutput, Age: policy.StaleAgeTurns, Policy: policy,
				ToolResults: policy.MinToolResultsPrune, ArchiveDir: sessionDir,
			},
			wantClass: requestReductionGeneric, wantLevel: retentionArchived,
			wantValidity: retentionValidityHistorical, wantRecovery: retentionRecoveryReadArtifact,
			wantConfidence: retentionConfidenceInferred,
			worstCase:      "a subagent result with no command to re-run and no URL to re-fetch is lost outright",
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
			wantValidity: retentionValidityHistorical, wantRecovery: retentionRecoveryReadArtifact,
			wantConfidence: retentionConfidenceInferred,
			worstCase:      "the tool layer's own address is dropped by the reduction layer, orphaning the archived payload",
		},
	}
}

// TestContextReductionAuthorityMatrix is the plan's P0-A matrix. Every row
// states what a request-surface rendering is allowed to claim; the invariants
// below hold for all of them at once.
func TestContextReductionAuthorityMatrix(t *testing.T) {
	sessionDir := t.TempDir()
	for _, test := range authorityMatrixCases(t, sessionDir) {
		t.Run(test.name, func(t *testing.T) {
			if test.worstCase == "" || test.authority == "" {
				t.Fatal("every matrix row must state its authority and the cost of misclassifying it")
			}
			class := classifyRequestReductionToolOutput(test.ctx)
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
			decision := retentionDecisionFor(test.ctx, class, rule, reduced)
			if decision.Level != test.wantLevel {
				t.Fatalf("level = %q, want %q (rendering: %q)", decision.Level, test.wantLevel, firstLine(reduced))
			}
			if decision.Validity != test.wantValidity {
				t.Fatalf("validity = %q, want %q", decision.Validity, test.wantValidity)
			}
			if decision.Recovery != test.wantRecovery {
				t.Fatalf("recovery = %q, want %q", decision.Recovery, test.wantRecovery)
			}
			if decision.Confidence != test.wantConfidence {
				t.Fatalf("confidence = %q, want %q", decision.Confidence, test.wantConfidence)
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
			if decision.Level == retentionHidden {
				t.Fatalf("request-level reduction must never hide a payload outright: %q", firstLine(reduced))
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
			class := classifyRequestReductionToolOutput(test.ctx)
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
			first := retentionDecisionFor(test.ctx, class, rule, reduced)

			// Now feed the marker back in, as a surface rebuild would if the
			// frozen prefix ever carried it into a fresh pass.
			next := test.ctx
			next.Content = reduced
			next.Age = test.ctx.Age + 1
			reClass := classifyRequestReductionToolOutput(next)
			if reClass == requestReductionNone {
				return
			}
			reReduced, reRule, ok := reduceRequestToolOutput(reClass, next)
			if !ok {
				return
			}
			second := retentionDecisionFor(next, reClass, reRule, reReduced)
			if second.Level == retentionHidden {
				t.Fatalf("re-reducing a marker hid the payload: %q", firstLine(reReduced))
			}
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
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}
