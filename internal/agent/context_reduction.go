package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

type ContextReductionStats struct {
	Messages        int
	Bytes           int
	CurrentBytes    int
	CurrentMessages int
	TokensBefore    int
	TokensAfter     int
	TokensSaved     int
	Protected       bool
	ReusedStable    bool
	ProtectReason   string
	ReuseReason     string
	SavedDelta      int
	PreviousModel   string
	ModelChanged    bool
	ModelRunLength  int
	ByToolAndRule   map[string]ContextReductionBucket
	SkippedByReason map[string]int
	OverCompression map[string]int
	// OverCompressionByTool breaks the same events down by the tool that
	// produced the re-fetched output. It is a separate map so OverCompression
	// stays summable and bounded: tool names include dynamic MCP names, so
	// mixing the two would make any total double-count and give the key space
	// no ceiling.
	OverCompressionByTool map[string]int
	// ByRetentionLevel counts tool results per retention level, including the
	// ones kept complete. ByToolAndRule only records what was reduced, so it
	// cannot answer how much of the request is still full-fidelity evidence —
	// the question the valid-read retention policy has to be decided on.
	//
	// This field and the four below make up the retention ledger, which is only
	// filled in when the request debug log is enabled: they answer questions a
	// reader asks, not questions the request surface branches on.
	ByRetentionLevel map[string]int
	// ProtectedReadTokens is what current, non-superseded reads cost in this
	// request. Reclaiming them is the single largest change the retention
	// policy could make, so its price is measured before it is considered.
	ProtectedReadTokens int
	// UnrecoverableReductions counts lossy renderings that left neither key
	// fields nor an address behind. It must stay zero: it is the layer's
	// destructive failure mode, not a tuning knob.
	UnrecoverableReductions int
	// ArchiveReads / ArchiveReadFailures count how often the model actually
	// followed a recovery address, and how often that read failed. A recovery
	// route nobody can use is indistinguishable from a dropped payload until
	// these are measured.
	ArchiveReads              int
	ArchiveReadFailures       int
	EvidenceRebuildDurationUS int64
	EvidenceFiles             int
	EvidenceObservations      int
	EvidenceCurrent           int
	EvidenceStale             int
	EvidenceSuperseded        int
}

const (
	contextReductionWrapUpGraceRequests = 1

	contextProtectReasonWrapUpGrace = "wrap_up_grace"

	contextReuseReasonNone                = ""
	contextReuseReasonBelowIncrementalMin = "below_incremental_min"
	contextReuseReasonNoPreviousSavings   = "no_previous_savings"
)

type ContextReductionBucket struct {
	Messages    int
	Bytes       int
	TokensSaved int
}

const (
	contextReductionSkipRecentHighRisk = "recent_high_risk"
	contextReductionSkipLargeUnreduced = "large_but_unreduced"
	contextReductionSkipFrozenReduced  = "frozen_reduced"
	contextReductionSkipDeferredCache  = "deferred_for_cache"
	contextReductionSkipDeferredReview = "deferred_for_review"
	contextReductionSkipRecalledInput  = "recalled_input_protect"

	contextReductionOverCompressionReread                = "reread_after_reduction"
	contextReductionOverCompressionRereadSameRevision    = "reread_same_revision_after_reduction"
	contextReductionOverCompressionRereadChangedRevision = "reread_changed_revision_after_reduction"
	contextReductionOverCompressionResearch              = "research_after_reduction"
)

// Cache-aware flush model for boundary reductions. Rewriting an already-sent
// message at position p re-bills every token after p at input price instead of
// cache-read price (~10x cheaper), a one-time penalty of roughly
// cacheMissPenaltyRatio × tail tokens. The reduction saves its tokens on every
// later request, so deferred boundary rewrites are flushed once the pending
// savings would amortize that penalty within reductionFlushHorizonRequests
// future requests (a conservative fraction of observed session lengths).
const (
	reductionFlushHorizonRequests = 30
	cacheMissPenaltyRatio         = 9
)

type contextReductionPolicy struct {
	Disabled                bool
	ConfirmAgeTurns         int
	ErrorAgeTurns           int
	HighRiskProtectAgeTurns int
	DiffProtectAgeTurns     int
	ShellSuccessAgeTurns    int
	ShellReadOnlyAgeTurns   int
	ReadLikeAgeTurns        int
	StaleAgeTurns           int
	ShellSuccessBytes       int
	ReadLikeOutputBytes     int
	StaleOutputBytes        int
	WrapUpGraceRequests     int
	MinToolResultsPrune     int
	MinIncrementalTokens    int
}

func defaultContextReductionPolicy() contextReductionPolicy {
	return contextReductionPolicy{
		ConfirmAgeTurns:         compactConfirmAgeTurns,
		ErrorAgeTurns:           compactErrorAgeTurns,
		HighRiskProtectAgeTurns: compactHighRiskProtectAgeTurns,
		DiffProtectAgeTurns:     compactDiffProtectAgeTurns,
		ShellSuccessAgeTurns:    compactBashSuccessAgeTurns,
		ShellReadOnlyAgeTurns:   compactShellReadOnlyAgeTurns,
		ReadLikeAgeTurns:        compactReadLikeAgeTurns,
		StaleAgeTurns:           compactStaleAgeTurns,
		ShellSuccessBytes:       compactBashSuccessBytes,
		ReadLikeOutputBytes:     compactReadLikeOutputBytes,
		StaleOutputBytes:        compactStaleOutputBytes,
		WrapUpGraceRequests:     contextReductionWrapUpGraceRequests,
		MinToolResultsPrune:     compactMinToolResultsPrune,
		MinIncrementalTokens:    compactMinIncrementalTokens,
	}
}

func (a *MainAgent) contextReductionPolicy() contextReductionPolicy {
	policy := defaultContextReductionPolicy()
	if a == nil {
		return policy
	}
	for _, cfg := range []*config.Config{a.globalConfig, a.projectConfig} {
		if cfg == nil {
			continue
		}
		policy.applyConfig(cfg.Context.Reduction)
	}
	return policy
}

func (p *contextReductionPolicy) applyConfig(cfg config.ContextReductionConfig) {
	if enabled, explicit := cfg.ExplicitEnabledValue(); explicit {
		p.Disabled = !enabled
	}
	if cfg.ConfirmAgeTurns > 0 {
		p.ConfirmAgeTurns = cfg.ConfirmAgeTurns
	}
	if cfg.ErrorAgeTurns > 0 {
		p.ErrorAgeTurns = cfg.ErrorAgeTurns
	}
	if cfg.HighRiskProtectAgeTurns > 0 {
		p.HighRiskProtectAgeTurns = cfg.HighRiskProtectAgeTurns
	}
	if cfg.DiffProtectAgeTurns > 0 {
		p.DiffProtectAgeTurns = cfg.DiffProtectAgeTurns
	}
	if cfg.ShellSuccessAgeTurns > 0 {
		p.ShellSuccessAgeTurns = cfg.ShellSuccessAgeTurns
	}
	if cfg.ShellReadOnlyAgeTurns > 0 {
		p.ShellReadOnlyAgeTurns = cfg.ShellReadOnlyAgeTurns
	}
	if cfg.ReadLikeAgeTurns > 0 {
		p.ReadLikeAgeTurns = cfg.ReadLikeAgeTurns
	}
	if cfg.StaleAgeTurns > 0 {
		p.StaleAgeTurns = cfg.StaleAgeTurns
	}
	if cfg.ShellSuccessBytes > 0 {
		p.ShellSuccessBytes = cfg.ShellSuccessBytes
	}
	if cfg.ReadLikeOutputBytes > 0 {
		p.ReadLikeOutputBytes = cfg.ReadLikeOutputBytes
	}
	if cfg.StaleOutputBytes > 0 {
		p.StaleOutputBytes = cfg.StaleOutputBytes
	}
	if cfg.WrapUpGraceRequests > 0 {
		p.WrapUpGraceRequests = cfg.WrapUpGraceRequests
	}
	if cfg.MinToolResultsPrune > 0 {
		p.MinToolResultsPrune = cfg.MinToolResultsPrune
	}
	if cfg.MinIncrementalTokens > 0 {
		p.MinIncrementalTokens = cfg.MinIncrementalTokens
	}
}

func (p contextReductionPolicy) reuseStableReductionSurfaceReason(stats, previous ContextReductionStats) (string, int) {
	if p.MinIncrementalTokens <= 0 || stats.TokensSaved <= 0 || previous.TokensSaved <= 0 {
		return contextReuseReasonNoPreviousSavings, 0
	}
	delta := stats.TokensSaved - previous.TokensSaved
	if delta < p.MinIncrementalTokens {
		return contextReuseReasonBelowIncrementalMin, delta
	}
	return contextReuseReasonNone, delta
}

var (
	numberedSourceLineRe = regexp.MustCompile(`^\s*\d+\s+(?:\S|$)`)
	pathListLikelyFileRe = regexp.MustCompile(`(?:^|/)[^/\s]+\.[A-Za-z0-9_+.-]+$`)
	diffHunkHeaderLineRe = regexp.MustCompile(`^@@@?\s+-\d+(?:,\d+)?(?:\s+-\d+(?:,\d+)?)*\s+\+\d+(?:,\d+)?\s+@@@?`)
)

// diagnosticsSectionLabel matches the LSP diagnostics section header anywhere
// in a tool result. Producers emit it after a blank line, but the preceding
// output may already end with newlines, so detection stays deliberately loose.
const diagnosticsSectionLabel = "Diagnostics:"

func reduceDiagnosticsToolOutput(content string, superseded bool) (string, bool) {
	idx := strings.Index(content, lsp.DiagnosticsSectionMarker)
	sepLen := len(lsp.DiagnosticsSectionMarker)
	if idx < 0 {
		idx = strings.Index(content, "\nDiagnostics:\n")
		sepLen = len("\nDiagnostics:\n")
	}
	if idx < 0 {
		return content, false
	}
	prefix := strings.TrimRight(content[:idx], "\n")
	diagnostics := strings.TrimSpace(content[idx+sepLen:])
	if diagnostics == "" {
		return content, false
	}
	lines := strings.Split(diagnostics, "\n")
	if superseded {
		summary := preferredDiagnosticsSummaryLine(lines)
		if summary == "" {
			summary = "diagnostics were present"
		}
		return prefix + "\n\nDiagnostics summary:\n[Older diagnostics details omitted; a newer diagnostics output appears later in this conversation; prefer that tool result.]\n" + summary, true
	}
	return prefix + "\n\nDiagnostics summary:\n" + renderDiagnosticsSummaryLines(diagnostics), true
}

// renderDiagnosticsSummaryLines renders a bounded full location list from a
// diagnostics block, dropping only the explanatory status/change-summary prose
// around the locations. Diagnostics lines are short location records (severity,
// line:col, code, message)); the list cap is a safety net well above what the
// LSP/Ruff output configs can emit. Keeping every location (rather than one
// preferred line) preserves the edit-diagnostics iteration feedback signal.
func renderDiagnosticsSummaryLines(diagnostics string) string {
	const maxLines = 40
	lines := make([]string, 0)
	omitted := 0
	for line := range strings.SplitSeq(diagnostics, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isNoisyDiagnosticsSummaryLine(trimmed) {
			continue
		}
		if len(lines) >= maxLines {
			omitted++
			continue
		}
		lines = append(lines, trimmed)
	}
	if len(lines) == 0 {
		lines = append(lines, "no actionable diagnostics preserved")
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("- ... (%d more diagnostics omitted) ...", omitted))
	}
	return strings.Join(lines, "\n")
}

func preferredDiagnosticsSummaryLine(lines []string) string {
	preferredPrefixes := []string{
		"Python diagnostics skipped:",
		"Ruff diagnostics failed:",
		"Ruff quick diagnostics failed:",
	}
	for _, prefix := range preferredPrefixes {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, prefix) {
				return trimmed
			}
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !isNoisyDiagnosticsSummaryLine(trimmed) {
			return trimmed
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type requestReductionClass string

const (
	requestReductionNone        requestReductionClass = ""
	requestReductionRepeated    requestReductionClass = "repeated_output"
	requestReductionToolError   requestReductionClass = "tool_error"
	requestReductionConfirm     requestReductionClass = "confirmation"
	requestReductionDiagnostics requestReductionClass = "diagnostics"
	requestReductionDiff        requestReductionClass = "diff"
	requestReductionReadLike    requestReductionClass = "read_like"
	requestReductionSearch      requestReductionClass = "search_result"
	requestReductionNumberedSrc requestReductionClass = "numbered_source"
	requestReductionJSON        requestReductionClass = "json_blob"
	requestReductionLongLog     requestReductionClass = "long_log"
	requestReductionShellOK     requestReductionClass = "shell_success"
	requestReductionListing     requestReductionClass = "record_listing"
	requestReductionGeneric     requestReductionClass = "generic_stale"
)

// Reduction rules name the rendering a class actually produced. Most are only
// reported in the per-tool/rule statistics, but these three are also read back
// as decisions — a class can fall back to another class's rendering, so the
// rule, not the class, says what the request surface ended up carrying.
const (
	// reductionRuleDiagnostics marks a structured diagnostics body: the
	// rendering the diagnostics class exists to produce.
	reductionRuleDiagnostics = "diagnostics"
	// reductionRuleArchived marks a rendering whose whole payload was written
	// to the session archive and replaced by its address.
	reductionRuleArchived = "archived"
	// reductionRuleStale marks the excerpt-bearing fallback summary used when a
	// class-specific renderer could not frame the payload.
	reductionRuleStale = "stale"
)

type requestReductionContext struct {
	ToolName   string
	ToolCallID string
	Meta       toolCallMeta
	// parseMemo carries the agent's per-call shell-parse memo so the
	// command-derived shape gates reuse the parse computed for this call on an
	// earlier request. A nil memo falls back to parsing directly.
	parseMemo   *reductionToolCallMemo
	Content     string
	ToolStatus  string
	FileState   *message.ToolFileState
	Age         int
	Policy      contextReductionPolicy
	Repeated    bool
	ToolResults int
	// ShellReadOnly marks a shell result whose command was classified read-only
	// (cat, ls, git log, ...). Such output is the shell-based analogue of a read
	// and gets a longer protection age: session data shows identical re-calls
	// arrive with a median gap of ~3 request batches.
	ShellReadOnly bool
	// ReadInvalidated / ReadSuperseded carry the conversation-level validity of
	// a read output (see analyzeReadValidity).
	ReadInvalidated bool
	ReadSuperseded  bool
	// ReadPriorContentLost marks an invalidated read whose bytes survive nowhere
	// else, so its summary must carry an archive address (see readValidity).
	ReadPriorContentLost bool
	// DiagnosticsSuperseded marks an edit-like result whose diagnostics section
	// was followed by a newer diagnostics-bearing result; the later output
	// carries the fresher LSP state, mirroring the read superseded semantics.
	DiagnosticsSuperseded bool
	// ArchiveDir is the session directory used to persist the full payload of
	// an output that cannot be reliably rebuilt or re-fetched, so the model can
	// read the archive back by its stable relative address instead of losing
	// the content to a generic marker.
	ArchiveDir string
}

// readRetentionProtects reports whether this successful read output must not
// be reduced. A read that is still the model's only current view of the file
// content — not invalidated by a later file mutation and not superseded by a
// newer read of the same range — is protected indefinitely regardless of age
// or size: trimming it forces the model either to re-read content it already
// paid for (extra rounds, broken prompt cache) or, worse, to answer from a
// summary it cannot verify. Capacity pressure is compaction's job, not
// reduction's.
func (ctx requestReductionContext) readRetentionProtects() bool {
	if ctx.ToolName != tools.NameRead || isToolResultUnsuccessfulStatus(ctx.ToolStatus) {
		return false
	}
	return !ctx.ReadInvalidated && !ctx.ReadSuperseded
}

// requestReductionVerdict is what one classification pass concluded about a
// tool result: the class that decides the rendering, plus the branch that
// decided it. Reason is only meaningful for a result kept complete — it names
// which protection fired — and it travels with the class so no second pass has
// to re-derive it from the same bytes. A hand-copied second pass is exactly how
// the reported reason and the branch that actually fired drift apart.
type requestReductionVerdict struct {
	Class  requestReductionClass
	Reason string
}

// protectedVerdict states that a named protection kept this result complete.
func protectedVerdict(reason string) requestReductionVerdict {
	return requestReductionVerdict{Class: requestReductionNone, Reason: reason}
}

// reducedVerdict states that a shape rule claimed this result.
func reducedVerdict(class requestReductionClass) requestReductionVerdict {
	return requestReductionVerdict{Class: class, Reason: string(class)}
}

// classifyRequestReductionToolOutput reports only the class. Callers that also
// report why a result survived use classifyRequestReduction.
func classifyRequestReductionToolOutput(ctx requestReductionContext) requestReductionClass {
	return classifyRequestReduction(ctx).Class
}

func classifyRequestReduction(ctx requestReductionContext) requestReductionVerdict {
	// An invalidated or superseded read must render its validity marker rather
	// than stay as full content — stale file content is misleading at any age
	// and at any size, and a superseded range is carried verbatim by the newer
	// read. It bypasses first-sight retention, the size gate and the protection
	// branches below (high-risk, diff); gating it here on size would make the
	// flat scan and the frozen incremental path render the same message
	// differently and alternately rewrite the cached prefix. It also takes
	// precedence over the repeated marker, whose "identical call appears later"
	// wording is weaker guidance than the validity note.
	if ctx.ToolName == tools.NameRead && (ctx.ReadInvalidated || ctx.ReadSuperseded) {
		return reducedVerdict(requestReductionReadLike)
	}
	if ctx.Repeated && ctx.Age >= 1 {
		return reducedVerdict(requestReductionRepeated)
	}
	if ctx.Age < ctx.Policy.HighRiskProtectAgeTurns && isHighRiskToolOutput(ctx) {
		return protectedVerdict(retentionReasonRecentHighRisk)
	}
	if ctx.readRetentionProtects() {
		return protectedVerdict(retentionReasonCurrentRead)
	}
	failed := isToolResultErrorStatus(ctx.ToolStatus) ||
		(strings.TrimSpace(ctx.ToolStatus) == "" && isToolErrorContent(ctx.Content))
	if failed && ctx.Age < ctx.Policy.ErrorAgeTurns {
		// An explicit failure is stronger evidence than any shape-based rule:
		// until it ages past the error threshold it must stay complete so the
		// model sees the exact failure while it is about to act on it. A failing
		// build's path:line:col lines must not be misread as a search result,
		// and a cancelled status must not behave like an error.
		return protectedVerdict(retentionReasonRecentError)
	}
	if ctx.Age >= ctx.Policy.ErrorAgeTurns && failed {
		return reducedVerdict(requestReductionToolError)
	}
	// Edit-like results carrying a diagnostics section stay complete until the
	// diagnostics summary age (ErrorAgeTurns): the LSP state is the feedback
	// the model iterates on while it is about to act, and the build-log /
	// read-like gates must not miscast a diagnostics block as a long log.
	if (ctx.ToolName == tools.NameEdit || ctx.ToolName == tools.NameApplyPatch || ctx.ToolName == tools.NameWrite) &&
		strings.Contains(ctx.Content, diagnosticsSectionLabel) && ctx.Age < ctx.Policy.ErrorAgeTurns {
		return protectedVerdict(retentionReasonRecentDiagnostics)
	}
	// Read-only shell output (cat, ls, git log, ...) is the shell-based analogue
	// of a read: unlike the read tool it has no validity tracking, so it relies
	// on a longer protection age before any size-based rule may reduce it.
	if ctx.ShellReadOnly && ctx.Age < ctx.Policy.ShellReadOnlyAgeTurns {
		return protectedVerdict(retentionReasonReadOnlyShell)
	}
	if ctx.Age < ctx.Policy.DiffProtectAgeTurns && looksLikeDiffOrPatch(ctx.Content) {
		return protectedVerdict(retentionReasonRecentDiff)
	}
	// Diffs are durable review evidence, not build logs. Keep them on a
	// dedicated path so source identifiers such as "error" and "failed" do
	// not turn a patch into a misleading log summary.
	if ctx.Age >= ctx.Policy.ShellSuccessAgeTurns &&
		len(ctx.Content) > max(ctx.Policy.ShellSuccessBytes, ctx.Policy.ReadLikeOutputBytes) &&
		looksLikeDiffOrPatch(ctx.Content) {
		return reducedVerdict(requestReductionDiff)
	}
	if ctx.Age >= ctx.Policy.ConfirmAgeTurns && isConfirmationOutput(ctx.Content) {
		return reducedVerdict(requestReductionConfirm)
	}
	if ctx.Age >= ctx.Policy.ErrorAgeTurns && (ctx.ToolName == tools.NameEdit || ctx.ToolName == tools.NameApplyPatch || ctx.ToolName == tools.NameWrite) && strings.Contains(ctx.Content, diagnosticsSectionLabel) {
		return reducedVerdict(requestReductionDiagnostics)
	}
	if ctx.Age >= ctx.Policy.ShellSuccessAgeTurns && len(ctx.Content) > ctx.Policy.ShellSuccessBytes && ctx.ToolName == tools.NameShell {
		// The command line is a first-hand statement of what this output is;
		// the heuristics below are second-hand inference from the bytes. Prefer
		// the former wherever it is conclusive.
		if shape, ok := commandDerivedShellShape(ctx); ok {
			return reducedVerdict(shape)
		}
		if looksLikeSearchResult(ctx) {
			return reducedVerdict(requestReductionSearch)
		}
		if looksLikeStructuredJSON(ctx.Content) {
			// The JSON skeleton (top-level keys / first items) is the lossiest
			// summary shape, and the model usually consumes values over several
			// requests, so a JSON document waits for the stale age instead of
			// the shell age. NDJSON log streams get no such retention.
			if !looksLikeJSONLinesLog(ctx.Content) && ctx.Age < ctx.Policy.StaleAgeTurns {
				return protectedVerdict(retentionReasonJSONAwaitsStale)
			}
			return reducedVerdict(requestReductionJSON)
		}
		if looksLikeNumberedSourceOutput(ctx.Content) {
			return reducedVerdict(requestReductionNumberedSrc)
		}
		if looksLikeBuildLikeLog(ctx) {
			return reducedVerdict(requestReductionLongLog)
		}
		return reducedVerdict(requestReductionShellOK)
	}
	// An invalidated or superseded read was already classified above; a web
	// fetch or other read-like output waits for the age gate here.
	if ctx.Age >= ctx.Policy.ReadLikeAgeTurns && len(ctx.Content) > ctx.Policy.ReadLikeOutputBytes {
		// Reached by a shell result only when the shell gate above is
		// configured looser than this one (higher ShellSuccessAgeTurns or
		// ShellSuccessBytes); at the default equal thresholds that gate already
		// returned. Kept so a custom policy does not silently fall back to
		// sniffing the bytes.
		if shape, ok := commandDerivedShellShape(ctx); ok {
			return reducedVerdict(shape)
		}
		switch {
		case looksLikeSearchResult(ctx):
			return reducedVerdict(requestReductionSearch)
		// Read-like tools take precedence over the JSON shape: a web fetch
		// of a JSON document must keep its URL rather than collapsing to a
		// key list.
		case contextReductionIsReadLike(ctx.ToolName):
			return reducedVerdict(requestReductionReadLike)
		case looksLikeStructuredJSON(ctx.Content):
			if !looksLikeJSONLinesLog(ctx.Content) && ctx.Age < ctx.Policy.StaleAgeTurns {
				return protectedVerdict(retentionReasonJSONAwaitsStale)
			}
			return reducedVerdict(requestReductionJSON)
		case looksLikeNumberedSourceOutput(ctx.Content):
			return reducedVerdict(requestReductionNumberedSrc)
		case looksLikeBuildLikeLog(ctx):
			return reducedVerdict(requestReductionLongLog)
		}
	}
	if ctx.ToolResults >= ctx.Policy.MinToolResultsPrune && ctx.Age >= ctx.Policy.StaleAgeTurns && len(ctx.Content) > ctx.Policy.StaleOutputBytes {
		// This is the widest gate (1500 bytes), so it is where a real
		// `git log --oneline -30` actually lands — the shell branch above never
		// sees it. The command signal has to be applied here too.
		if shape, ok := commandDerivedShellShape(ctx); ok {
			return reducedVerdict(shape)
		}
		if looksLikeSearchResult(ctx) {
			return reducedVerdict(requestReductionSearch)
		}
		if contextReductionIsReadLike(ctx.ToolName) {
			return reducedVerdict(requestReductionReadLike)
		}
		if looksLikeStructuredJSON(ctx.Content) {
			return reducedVerdict(requestReductionJSON)
		}
		if looksLikeNumberedSourceOutput(ctx.Content) {
			return reducedVerdict(requestReductionNumberedSrc)
		}
		if looksLikeBuildLikeLog(ctx) {
			return reducedVerdict(requestReductionLongLog)
		}
		return reducedVerdict(requestReductionGeneric)
	}
	return protectedVerdict(retentionReasonNoRuleMatched)
}

// nextContextReductionReviewAge returns the next request age at which an
// unchanged non-read tool result can cross an age-based reduction rule. Zero
// means no later age threshold can change its classification; other state
// changes (repeat detection and the generic tool-count gate) are handled by the
// caller before consulting this frontier.
func nextContextReductionReviewAge(ctx requestReductionContext) int {
	thresholds := []int{
		ctx.Policy.ErrorAgeTurns,
		ctx.Policy.ConfirmAgeTurns,
		ctx.Policy.ShellSuccessAgeTurns,
		ctx.Policy.ShellReadOnlyAgeTurns,
		ctx.Policy.ReadLikeAgeTurns,
		ctx.Policy.StaleAgeTurns,
		ctx.Policy.HighRiskProtectAgeTurns,
		ctx.Policy.DiffProtectAgeTurns,
	}
	next := 0
	for _, threshold := range thresholds {
		if threshold > ctx.Age && (next == 0 || threshold < next) {
			next = threshold
		}
	}
	return next
}

// Marker keyword sets shared by the context-reduction heuristics. They are kept
// as distinct package-level slices (rather than one merged list) because each
// caller scans for a deliberately different signal; centralizing them here keeps
// the variants visible side by side so a future edit is less likely to miss one.
var (
	// highRiskMarkers flag tool output that must be protected from reduction
	// while still recent (stack traces, auth failures, assertion mismatches).
	highRiskMarkers = []string{
		"traceback",
		"panic:",
		"exception",
		"segmentation fault",
		"permission denied",
		"access denied",
		"unauthorized",
		"forbidden",
		"expected:",
		"actual:",
		"assertion failed",
		"assert failed",
		// Go and Rust test runners report a failing case as "--- FAIL: Name",
		// which contains neither "failed" nor any other marker above.
		"--- fail",
		"npm err!",
		"fatal:",
	}
	// buildLogMarkers classify build/test/lint output as a long log worth
	// signal-based summarization rather than blanket omission.
	buildLogMarkers = []string{
		"error",
		"warning",
		// "fail" rather than "failed"/"failure": test runners print bare
		// "FAIL", "--- FAIL:" and "failing", none of which the longer forms
		// match, and a log whose only failure evidence is such a line would
		// otherwise summarize to "no preserved log lines".
		"fail",
		"panic:",
		"traceback",
		"exception",
		"diagnostics:",
	}
	// logLineMarkers select representative lines to preserve from a long log.
	logLineMarkers = []string{
		"error",
		"warning",
		"fail",
		"panic:",
		"traceback",
		"exception",
	}
	// importantLineMarkers select lines to preserve when summarizing an error
	// output, covering both failure and auth/timeout signals.
	importantLineMarkers = []string{
		"error",
		"warning",
		"fail",
		"panic:",
		"traceback",
		"exception",
		"fatal:",
		"expected",
		"actual",
		"assert",
		"permission",
		"denied",
		"unauthorized",
		"forbidden",
		"timeout",
	}
	// shellSuccessWeakLineMarkers select compact evidence from old successful
	// shell output. These weaker substring signals are considered after stronger
	// result markers so early progress lines don't crowd out final status.
	shellSuccessWeakLineMarkers = []string{
		"generated",
		"wrote",
		"written",
		"created",
		"updated",
		"skipped",
		"deprecated",
		"summary",
		"completed",
	}
)

func containsAnyMarker(lower string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func forEachLine(content string, visit func(string) bool) {
	for len(content) > 0 {
		line := content
		if idx := strings.IndexByte(content, '\n'); idx >= 0 {
			line = content[:idx]
			content = content[idx+1:]
		} else {
			content = ""
		}
		if !visit(line) {
			return
		}
	}
}

func isHighRiskToolOutput(ctx requestReductionContext) bool {
	if strings.TrimSpace(ctx.Content) == "" {
		return false
	}
	if looksLikeDiffOrPatch(ctx.Content) {
		return true
	}
	content := strings.ToLower(highRiskScanPrefix(ctx.Content))
	if containsAnyMarker(content, highRiskMarkers) {
		return true
	}
	if ctx.ToolName == tools.NameShell && strings.Contains(content, "failed") {
		return true
	}
	return false
}

func highRiskScanPrefix(content string) string {
	const maxHighRiskScanBytes = 64 * 1024
	if len(content) <= maxHighRiskScanBytes {
		return content
	}
	return content[:maxHighRiskScanBytes]
}

func looksLikeDiffOrPatch(content string) bool {
	seenHeader := false
	start := 0
	for range 80 {
		if start > len(content) {
			break
		}
		line := content[start:]
		if newline := strings.IndexByte(line, '\n'); newline >= 0 {
			line = line[:newline]
			start += newline + 1
		} else {
			start = len(content) + 1
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "diff --git "), strings.HasPrefix(trimmed, "diff --combined "), strings.HasPrefix(trimmed, "diff --cc "), strings.HasPrefix(trimmed, "--- "), strings.HasPrefix(trimmed, "+++ "):
			seenHeader = true
		case seenHeader && diffHunkHeaderLineRe.MatchString(trimmed):
			return true
		case seenHeader && (strings.HasPrefix(trimmed, "GIT binary patch") ||
			strings.HasPrefix(trimmed, "Binary files ") ||
			strings.HasPrefix(trimmed, "old mode ") ||
			strings.HasPrefix(trimmed, "new mode ") ||
			strings.HasPrefix(trimmed, "new file mode ") ||
			strings.HasPrefix(trimmed, "deleted file mode ") ||
			strings.HasPrefix(trimmed, "similarity index ") ||
			strings.HasPrefix(trimmed, "rename from ") ||
			strings.HasPrefix(trimmed, "rename to ") ||
			strings.HasPrefix(trimmed, "copy from ") ||
			strings.HasPrefix(trimmed, "copy to ")):
			return true
		case strings.HasPrefix(trimmed, "*** Begin Patch"), strings.HasPrefix(trimmed, "*** Update File:"), strings.HasPrefix(trimmed, "*** Add File:"), strings.HasPrefix(trimmed, "*** Delete File:"):
			return true
		}
	}
	return false
}

func reduceRequestToolOutput(class requestReductionClass, ctx requestReductionContext) (string, string, bool) {
	var reduced string
	var rule string
	switch class {
	case requestReductionRepeated:
		reduced, rule = fmt.Sprintf("[Repeated %s output omitted; an identical call appears later.]", toolNameOrUnknown(ctx.ToolName)), "repeated"
	case requestReductionToolError:
		reduced, rule = reduceToolErrorOutputSummary(ctx), "error"
	case requestReductionConfirm:
		reduced, rule = "[Confirmed]", "confirmation"
	case requestReductionDiagnostics:
		if compacted, ok := reduceDiagnosticsToolOutput(ctx.Content, ctx.DiagnosticsSuperseded); ok {
			reduced, rule = compacted, reductionRuleDiagnostics
			break
		}
		// The structured diagnostics body is this class's entire recovery
		// route, so a payload the renderer could not frame must not fall back
		// to a bare marker: the routing gate only looks for the section label
		// anywhere in the content while the renderer needs the exact framing,
		// and any output that mentions the label in passing lands here. Render
		// an excerpt instead and let the archive gate below add an address.
		reduced, rule = reduceGenericStaleOutputSummary(ctx), reductionRuleStale
	case requestReductionDiff:
		reduced, rule = reduceDiffOutputSummary(ctx.Content), "diff"
	case requestReductionReadLike:
		reduced, rule = reduceReadLikeOutputSummary(ctx), "read_like"
	case requestReductionSearch:
		reduced, rule = reduceSearchLikeOutputSummary(ctx), "search_result"
	case requestReductionNumberedSrc:
		reduced, rule = reduceNumberedSourceOutputSummary(ctx), "numbered_source"
	case requestReductionJSON:
		if compacted, ok := reduceJSONBlobSummary(ctx); ok && len(compacted) < len(ctx.Content) {
			reduced, rule = compacted, "json_blob"
			break
		}
		// A JSON document the skeleton renderer could not shrink still has to
		// keep an excerpt: the head/tail summary is the model's only in-request
		// evidence of what the document held while the archive address below is
		// being fetched.
		summary := reduceGenericStaleOutputSummary(ctx)
		if len(summary) < len(ctx.Content) {
			reduced, rule = summary, reductionRuleStale
			break
		}
		return "", "", false
	case requestReductionLongLog:
		reduced, rule = reduceLongLogOutputSummary(ctx), "long_log"
	case requestReductionShellOK:
		reduced, rule = reduceShellSuccessOutputSummary(ctx), "shell_success"
	case requestReductionListing:
		reduced, rule = reduceRecordListingOutputSummary(ctx), "record_listing"
	case requestReductionGeneric:
		// One-shot, non-rebuildable outputs are archived in full so the model
		// can read them back by stable address instead of losing the payload
		// to a generic marker; rebuildable outputs keep their ordinary summary.
		if irreducibleToolOutputRequiresArchive(ctx.ToolName) {
			if marker, ok := archiveIrreducibleToolOutput(ctx); ok {
				reduced, rule = marker, reductionRuleArchived
				break
			}
		}
		if reduced == "" {
			reduced, rule = reduceGenericStaleOutputSummary(ctx), reductionRuleStale
		}
	default:
		return "", "", false
	}
	reduced = appendPreservedArtifactReferences(reduced, ctx.Content)
	if address, ok := ensureReducedOutputRecoverable(class, rule, reduced, ctx); ok {
		reduced += "\n" + address
	}
	return reduced, rule, true
}

// minRecoverableArchiveBytes is the payload size above which a lossy summary
// must leave a recovery address behind. It tracks the widest byte gate any
// reduction rule uses, so there is no band that is large enough to be
// summarized yet too small to be archived.
const minRecoverableArchiveBytes = compactStaleOutputBytes

// archivedOutputMarkerFragment identifies a summary that already archived its
// own payload. Both the marker's format string and this probe must move
// together: if they drift apart the recoverability check silently starts
// reporting "not yet archived" (an extra copy, harmless) or "already archived"
// (a payload dropped with no address, the invariant this file exists to hold).
const archivedOutputMarkerFragment = " output archived at "

// ensureReducedOutputRecoverable upholds the layer's restorable invariant:
// never drop payload without leaving an address to get it back. The tool layer
// already archives anything over its inline budget and durable compaction
// exports history before rewriting it, but request-level reduction used to
// archive only five one-shot tools, so everything between the byte gates and
// the inline budget was lost outright. That is the band this layer works in,
// and it is why a misclassified summary could cost real work: the payload was
// gone, not merely summarized.
//
// Renderings that already carry their own recovery route are skipped: a
// confirmation has no payload, and a diagnostics summary keeps the structured
// body it was framed from. The diagnostics exemption is keyed on the rule the
// renderer actually applied, not on the class the router picked: the router
// only requires the section label to appear somewhere in the content while the
// renderer requires the exact framing, so a class that fell back to an excerpt
// must be archived like any other lossy summary.
// Archives are content-addressed, so identical copies (the repeated case) share
// one file rather than writing one each.
//
// A read is the delicate case. "Re-read the file" is a real recovery route only
// while the bytes the read observed are still reachable — either on disk (the
// range was superseded by a newer read, so nothing changed underneath) or in
// the arguments of whatever edited it (edit's old_string, apply_patch's body).
// When a whole-file write, a delete, a mutating shell command or an external
// process replaced the file, re-reading returns the new content and the observed
// revision is gone: that read output is its only record, so it must be archived
// like any other lossy summary. This was the layer's last outright-destructive
// path.
func ensureReducedOutputRecoverable(class requestReductionClass, rule, reduced string, ctx requestReductionContext) (string, bool) {
	if rule == reductionRuleDiagnostics {
		return "", false
	}
	switch class {
	case requestReductionConfirm:
		return "", false
	case requestReductionReadLike:
		if toolname.Normalize(ctx.ToolName) == tools.NameRead && !ctx.ReadPriorContentLost {
			return "", false
		}
	}
	if ctx.ArchiveDir == "" || len(ctx.Content) < minRecoverableArchiveBytes {
		return "", false
	}
	// Already recoverable: the tool layer's own reference survived into the
	// summary, or this class archived the payload itself. A read whose prior
	// content is gone never takes this exit: its summary quotes the head of the
	// payload, so a file that merely mentions an archive address would spoof
	// the check into dropping the only copy of what the read observed.
	if !ctx.ReadPriorContentLost &&
		(len(tools.ExtractArtifactReferences(reduced)) > 0 || strings.Contains(reduced, archivedOutputMarkerFragment)) {
		return "", false
	}
	path, ok := writeReducedOutputArchive(ctx)
	if !ok {
		return "", false
	}
	// Canonical reference form: the trailing period is what makes this parse
	// as an artifact reference, so later reductions of this marker carry the
	// address forward instead of dropping it.
	return tools.ArtifactReferencePrefix + path + ".", true
}

// writeReducedOutputArchive persists content under a content-addressed name so
// repeated writes of the same payload converge on one file.
func writeReducedOutputArchive(ctx requestReductionContext) (string, bool) {
	if strings.TrimSpace(ctx.Content) == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(ctx.Content))
	fileName := fmt.Sprintf("%s-%s.txt", toolNameOrUnknown(ctx.ToolName), hex.EncodeToString(sum[:6]))
	absPath := filepath.Join(ctx.ArchiveDir, "reduced-artifacts", fileName)
	// Content-addressed: a file of the right size under this name already holds
	// these bytes. Skipping the rewrite keeps a deferred proposal from paying
	// the write on every request, and keeps a concurrent reader from observing
	// the truncated window of a rewrite that could not change the contents.
	if info, err := privatefs.Stat(ctx.ArchiveDir, absPath); err == nil && info.Size() == int64(len(ctx.Content)) {
		return absPath, true
	}
	if err := privatefs.WriteFile(ctx.ArchiveDir, absPath, []byte(ctx.Content)); err != nil {
		return "", false
	}
	return absPath, true
}

func appendPreservedArtifactReferences(reduced, original string) string {
	refs := tools.ExtractArtifactReferences(original)
	if len(refs) == 0 {
		return reduced
	}
	// Compare against what the reference parser finds in the summary, not
	// against raw containment: an excerpt line can quote an address behind a
	// list bullet, where the parser no longer recognizes it. A summary of a
	// summary would then mention an address the model cannot follow.
	kept := tools.ExtractArtifactReferences(reduced)
	for _, ref := range refs {
		if slices.Contains(kept, ref) {
			continue
		}
		if reduced != "" {
			reduced += "\n"
		}
		reduced += ref
	}
	return reduced
}

// staleOutputOmittedMarker is the fallback rendering for an output whose
// class-specific summarizer produced nothing usable. Callers pass whichever
// name their class tracks: the reduction switch reads the recorded call meta,
// while the read-like path reads the resolved tool name.
func staleOutputOmittedMarker(toolName string) string {
	return fmt.Sprintf("[Older %s output omitted from this request to save context.]", toolNameOrUnknown(toolName))
}

func reduceToolErrorOutputSummary(ctx requestReductionContext) string {
	lines := summarizeImportantLines(ctx.Content, 6)
	if len(lines) == 0 {
		lines = summarizeHeadTailLines(ctx.Content, 4)
	}
	if len(lines) == 0 {
		lines = []string{"- (no preserved error details)"}
	}
	return fmt.Sprintf("[Older %s error summarized for this request to save context; bytes=%d lines=%d]\n%s", toolNameOrUnknown(ctx.ToolName), len(ctx.Content), countMeaningfulLines(ctx.Content), strings.Join(lines, "\n"))
}

// reduceGenericStaleOutputSummary routes a stale tool output to the best
// content-shaped summary before falling back to a head/tail excerpt. It is a
// defensive router keyed on content, not on the tool name, so the same arms
// stay valid no matter which caller reaches it. Some arms (e.g. numbered
// source) are not reachable from the current classify path because that path
// already screens the same shape, but they are kept so the router behaves
// correctly if a future caller routes here without that pre-screen.
func reduceGenericStaleOutputSummary(ctx requestReductionContext) string {
	toolName := toolNameOrUnknown(ctx.ToolName)
	switch {
	case looksLikeSearchResultContent(ctx.Content):
		lines := summarizeSearchResultLines(ctx.Content, 4)
		if len(lines) == 0 {
			break
		}
		return fmt.Sprintf("[Older %s output summarized as search-like content for this request; matches=%d]\n%s", toolName, countMeaningfulLines(ctx.Content), strings.Join(lines, "\n"))
	case looksLikeNumberedSourceOutput(ctx.Content):
		return reduceNumberedSourceOutputSummary(ctx)
	case looksLikePathListOutput(ctx.Content):
		lines := summarizeHeadTailLines(ctx.Content, 6)
		if len(lines) == 0 {
			break
		}
		return fmt.Sprintf("[Older %s path-list output summarized for this request; paths=%d bytes=%d]\n%s", toolName, countMeaningfulLines(ctx.Content), len(ctx.Content), strings.Join(lines, "\n"))
	}
	lines := summarizeHeadTailLines(ctx.Content, 4)
	if len(lines) == 0 {
		lines = []string{"- (no preserved excerpt)"}
	}
	return fmt.Sprintf("[Older %s output summarized for this request to save context; bytes=%d lines=%d]\n%s", toolName, len(ctx.Content), countMeaningfulLines(ctx.Content), strings.Join(lines, "\n"))
}

// irreducibleToolOutputRequiresArchive reports whether a tool's result cannot
// be reliably rebuilt or re-fetched after the fact — one-shot asynchronous or
// interactive outputs (spawned jobs, delegated results, user notifications)
// have no command to re-run and no URL to re-fetch, so their full payload must
// survive reduction as an archived artifact instead of a generic marker.
func irreducibleToolOutputRequiresArchive(toolName string) bool {
	switch tools.NormalizeName(toolName) {
	case tools.NameJobOutput, tools.NameDelegate, tools.NameNotify, tools.NameQuestion:
		return true
	}
	return false
}

// archiveIrreducibleToolOutput persists the full payload of a one-shot tool
// output to the session's reduced-artifacts directory and returns the stable
// address marker. It reports false when there is no archive dir or the write
// fails, letting the caller fall back to the ordinary summary instead of
// dropping the output to a marker with no recovery path.
func archiveIrreducibleToolOutput(ctx requestReductionContext) (string, bool) {
	if ctx.ArchiveDir == "" || strings.TrimSpace(ctx.Content) == "" {
		return "", false
	}
	absPath, ok := writeReducedOutputArchive(ctx)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("[Older %s%s%s; read it back to recover the full payload (bytes=%d).]",
		toolNameOrUnknown(ctx.ToolName), archivedOutputMarkerFragment, absPath, len(ctx.Content)), true
}

func reduceNumberedSourceOutputSummary(ctx requestReductionContext) string {
	lines := summarizeHeadTailLines(ctx.Content, 6)
	if len(lines) == 0 {
		lines = []string{"- (no preserved source excerpt)"}
	}
	meaningfulLines, rangeNote := numberedSourceStats(ctx.Content)
	return fmt.Sprintf("[Older %s output summarized as numbered source for this request; lines=%d bytes=%d%s]\n%s", toolNameOrUnknown(ctx.ToolName), meaningfulLines, len(ctx.Content), rangeNote, strings.Join(lines, "\n"))
}

// numberedSourceStats reports the meaningful line count and first→last source
// range in one pass. The range is empty when no line numbers could be parsed.
func numberedSourceStats(content string) (int, string) {
	meaningful := 0
	first, last := 0, 0
	found := false
	forEachLine(content, func(line string) bool {
		if strings.TrimSpace(line) == "" {
			return true
		}
		meaningful++
		num, ok := parseNumberedSourceLine(line)
		if !ok {
			return true
		}
		if !found {
			first = num
			found = true
		}
		last = num
		return true
	})
	if !found {
		return meaningful, ""
	}
	if first == last {
		return meaningful, fmt.Sprintf(" range=%d", first)
	}
	return meaningful, fmt.Sprintf(" range=%d-%d", first, last)
}

// parseNumberedSourceLine extracts the line number from "123 content".
// A number must be followed by whitespace so identifiers like "123abc" are
// not mistaken for source line numbers. Content after the separator may be
// empty because nl still numbers blank source lines.
func parseNumberedSourceLine(line string) (num int, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	digits := 0
	for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits == len(trimmed) {
		return 0, false
	}
	if trimmed[digits] != ' ' && trimmed[digits] != '\t' {
		return 0, false
	}
	n, err := strconv.Atoi(trimmed[:digits])
	if err != nil {
		return 0, false
	}
	return n, true
}

func looksLikeSearchResult(ctx requestReductionContext) bool {
	switch ctx.ToolName {
	case tools.NameGrep, tools.NameGlob:
		return true
	case tools.NameLsp:
		var parsed struct {
			Operation string `json:"operation"`
		}
		if err := json.Unmarshal([]byte(ctx.Meta.Args), &parsed); err != nil {
			return false
		}
		return strings.TrimSpace(parsed.Operation) == "references"
	default:
		return looksLikeSearchResultContent(ctx.Content)
	}
}

func looksLikeSearchResultContent(content string) bool {
	matches := 0
	checked := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		checked++
		if _, _, _, ok := parseSearchResultLine(trimmed); ok {
			matches++
		}
		if matches >= 2 {
			return false
		}
		if checked >= 24 {
			return false
		}
		return true
	})
	return matches >= 2
}

func parseSearchResultLine(line string) (path, lineNo, snippet string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.ContainsAny(trimmed[:1], ":\t\n\r ") {
		return "", "", "", false
	}
	for i := 1; i < len(trimmed)-2; i++ {
		if trimmed[i] != ':' || !isASCIIDigit(trimmed[i+1]) {
			continue
		}
		j := i + 2
		for j < len(trimmed) && isASCIIDigit(trimmed[j]) {
			j++
		}
		if j >= len(trimmed) || trimmed[j] != ':' {
			continue
		}
		path = strings.TrimSpace(trimmed[:i])
		lineNo = strings.TrimSpace(trimmed[i+1 : j])
		snippet = strings.TrimSpace(trimmed[j+1:])
		if !looksLikeSearchResultPath(path) {
			continue
		}
		return path, lineNo, snippet, path != "" && lineNo != "" && snippet != ""
	}
	return "", "", "", false
}

// looksLikeSearchResultPath rejects the `path` half of a `path:line:text`
// candidate when it cannot plausibly be a file path. Without it a timestamped
// log line — `12:34:56 INFO ...`, or `2026-09-06 12:34:56 ...` — parses as a
// hit at line 34 of a file named "12", two such lines make the whole output
// look like a search result, and the summary then asserts a match count and a
// preserved query for output that never was a search.
func looksLikeSearchResultPath(path string) bool {
	if path == "" || strings.ContainsAny(path, " \t") {
		return false
	}
	digitsOnly := true
	for i := 0; i < len(path); i++ {
		if !isASCIIDigit(path[i]) {
			digitsOnly = false
			break
		}
	}
	if digitsOnly {
		return false
	}
	// Real hits name a file: either a directory component or an extension.
	return strings.ContainsAny(path, "/\\.")
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func looksLikeNumberedSourceOutput(content string) bool {
	matches := 0
	checked := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) == "" {
			return true
		}
		checked++
		if numberedSourceLineRe.MatchString(trimmed) {
			matches++
		}
		if matches >= 4 {
			return false
		}
		if checked >= 16 {
			return false
		}
		return true
	})
	return matches >= 4
}

func looksLikeStructuredJSON(content string) bool {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) < 2 {
		return false
	}
	return (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) || (strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))
}

// looksLikeJSONLinesLog reports NDJSON-shaped output (each early line its own
// JSON object), e.g. `go test -json` or structured build logs. That is log
// streaming, not a JSON document the model reads values from over several
// requests, so it earns no extended JSON retention.
func looksLikeJSONLinesLog(content string) bool {
	lines := 0
	jsonLines := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		lines++
		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			jsonLines++
		}
		return lines < 3
	})
	return lines >= 2 && jsonLines == lines
}

func looksLikeBuildLikeLog(ctx requestReductionContext) bool {
	if ctx.ToolName != tools.NameShell && ctx.ToolName != tools.NameEdit && ctx.ToolName != tools.NameApplyPatch && ctx.ToolName != tools.NameWrite {
		return false
	}
	content := strings.TrimSpace(ctx.Content)
	if content == "" {
		return false
	}
	// A single incidental marker must not classify an ordinary listing as a
	// build log. `git log --oneline` prints one commit per line, and a subject
	// such as "fix(tools): improve match-failure recovery" contains "fail";
	// treating that listing as a build log routes it through the signal-based
	// log summary, which keeps only the marker lines and drops every other
	// commit. Require either a line that leads with a failure verdict (a real
	// runner always emits one) or at least two marker lines.
	matches := 0
	strong := false
	forEachLine(content, func(line string) bool {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			return true
		}
		if strings.HasPrefix(lower, "fail ") ||
			strings.HasPrefix(lower, "fail\t") ||
			strings.HasPrefix(lower, "--- fail") ||
			strings.HasPrefix(lower, "build failed") ||
			strings.HasPrefix(lower, "lint failed") ||
			strings.HasPrefix(lower, "panic:") ||
			strings.HasPrefix(lower, "traceback") ||
			strings.HasPrefix(lower, "diagnostics:") {
			strong = true
			return false
		}
		if containsAnyMarker(lower, buildLogMarkers) {
			matches++
			if matches >= 2 {
				return false
			}
		}
		return true
	})
	return strong || matches >= 2
}

func reduceSearchLikeOutputSummary(ctx requestReductionContext) string {
	toolName := toolNameOrUnknown(ctx.ToolName)
	snippetLines := summarizeSearchResultLocations(ctx.Content, searchSummaryByteBudget)
	if len(snippetLines) == 0 {
		snippetLines = summarizeSearchResultLines(ctx.Content, 6)
	}
	if len(snippetLines) == 0 {
		snippetLines = []string{"- (no preserved matches)"}
	}
	scope := reduceSearchScope(ctx)
	return fmt.Sprintf("[Older %s results summarized for this request to save context; %s; matches=%d]\n%s", toolName, scope, countMeaningfulLines(ctx.Content), strings.Join(snippetLines, "\n"))
}

const (
	// searchSummaryByteBudget bounds the location listing of a reduced search
	// result. Within it every matched file keeps its line numbers: the
	// path:line list — not the snippets — is what a multi-site task acts on,
	// and session data shows >3000B search outputs carry ~75 matches at the
	// median, which fits comfortably as locations but not as snippet groups.
	searchSummaryByteBudget      = 1400
	searchSummaryMaxLinesPerFile = 8
	searchSummarySnippetGroups   = 3
)

type searchLocationGroup struct {
	path    string
	lines   []string
	matches int
	snippet string
}

// summarizeSearchResultLocations renders search output as a location-first
// listing: every matched path with its line numbers (capped per file), the
// first few groups keeping one snippet as a content reminder, all within
// byteBudget. Outputs without path:line matches (glob, rg -l) become a plain
// budget-bounded line list. Returns nil when nothing was parseable.
func summarizeSearchResultLocations(content string, byteBudget int) []string {
	if byteBudget <= 0 {
		return nil
	}
	groups := make([]searchLocationGroup, 0)
	groupByPath := make(map[string]int)
	var plain []string
	plainTotal := 0
	totalMatches := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		path, lineNo, snippet, ok := parseSearchResultLine(trimmed)
		if !ok {
			plainTotal++
			if len(plain) < 64 {
				plain = append(plain, compactTextSnippet(trimmed, summaryLineSnippetChars))
			}
			return true
		}
		totalMatches++
		idx, seen := groupByPath[path]
		if !seen {
			idx = len(groups)
			groupByPath[path] = idx
			g := searchLocationGroup{path: path}
			// Only the leading snippet groups ever render their snippet;
			// building one per path is wasted allocation on large outputs.
			if idx < searchSummarySnippetGroups {
				g.snippet = compactTextSnippet(snippet, 100)
			}
			groups = append(groups, g)
		}
		g := &groups[idx]
		g.matches++
		if len(g.lines) < searchSummaryMaxLinesPerFile && (len(g.lines) == 0 || g.lines[len(g.lines)-1] != lineNo) {
			g.lines = append(g.lines, lineNo)
		}
		return true
	})
	if len(groups) == 0 {
		if len(plain) == 0 {
			return nil
		}
		out := make([]string, 0, len(plain))
		used := 0
		for _, line := range plain {
			cost := len(line) + 3
			if used+cost > byteBudget {
				break
			}
			out = append(out, "- "+strings.ReplaceAll(line, "\n", " "))
			used += cost
		}
		for omitted := plainTotal - len(out); omitted > 0; omitted++ {
			marker := fmt.Sprintf("- ... (+%d lines omitted) ...", omitted)
			if used+len(marker)+1 <= byteBudget {
				out = append(out, marker)
				break
			}
			if len(out) == 0 {
				break
			}
			last := out[len(out)-1]
			out = out[:len(out)-1]
			used -= len(last) + 1
		}
		return out
	}
	out := make([]string, 0, min(len(groups), 32))
	used := 0
	renderedMatches := 0
	for i := range groups {
		g := &groups[i]
		line := "- " + g.path + ": " + strings.Join(g.lines, ", ")
		if extra := g.matches - len(g.lines); extra > 0 {
			line += fmt.Sprintf(" (+%d more)", extra)
		}
		if len(out) < searchSummarySnippetGroups && g.snippet != "" {
			line += " — " + strings.ReplaceAll(g.snippet, "\n", " ")
		}
		cost := len(line) + 1
		if used+cost > byteBudget {
			break
		}
		out = append(out, line)
		used += cost
		renderedMatches += g.matches
	}
	// Non-match lines mixed into a path:line output (tool truncation notes,
	// invalid-regex warnings) are not rendered; account for them in the tail
	// so the reader cannot mistake the listed matches for the complete output.
	otherLinesNote := ""
	if plainTotal > 0 {
		otherLinesNote = fmt.Sprintf(", %d other lines", plainTotal)
	}
	for {
		omittedFiles := len(groups) - len(out)
		if omittedFiles == 0 && plainTotal == 0 {
			return out
		}
		var marker string
		if omittedFiles > 0 {
			marker = fmt.Sprintf("- ... (+%d files, %d matches%s omitted) ...", omittedFiles, totalMatches-renderedMatches, otherLinesNote)
		} else {
			marker = fmt.Sprintf("- ... (+%d other lines omitted) ...", plainTotal)
		}
		if used+len(marker)+1 <= byteBudget {
			out = append(out, marker)
			return out
		}
		if len(out) == 0 {
			return out
		}
		last := out[len(out)-1]
		out = out[:len(out)-1]
		used -= len(last) + 1
		renderedMatches -= groups[len(out)].matches
	}
}

func reduceSearchScope(ctx requestReductionContext) string {
	switch ctx.ToolName {
	case tools.NameGrep:
		var parsed struct {
			Pattern  string   `json:"pattern"`
			Paths    []string `json:"paths"`
			Includes []string `json:"includes"`
		}
		_ = json.Unmarshal([]byte(ctx.Meta.Args), &parsed)
		paths := reduceSearchList(parsed.Paths, ".")
		includes := reduceSearchList(parsed.Includes, "")
		return fmt.Sprintf("pattern=%q paths=%q includes=%q", strings.TrimSpace(parsed.Pattern), paths, includes)
	case tools.NameGlob:
		var parsed struct {
			Patterns []string `json:"patterns"`
			Path     string   `json:"path"`
		}
		_ = json.Unmarshal([]byte(ctx.Meta.Args), &parsed)
		return fmt.Sprintf("patterns=%q path=%q", reduceSearchList(parsed.Patterns, ""), blankToDefault(strings.TrimSpace(parsed.Path), "."))
	case tools.NameLsp:
		var parsed struct {
			Operation string `json:"operation"`
			Path      string `json:"path"`
			Line      int    `json:"line"`
		}
		_ = json.Unmarshal([]byte(ctx.Meta.Args), &parsed)
		return fmt.Sprintf("operation=%q path=%q line=%d", strings.TrimSpace(parsed.Operation), strings.TrimSpace(parsed.Path), parsed.Line)
	default:
		return "query preserved"
	}
}

func reduceSearchList(values []string, fallback string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return fallback
	}
	return strings.Join(parts, ",")
}

type searchSummaryGroup struct {
	Path    string
	Matches []string
}

func summarizeSearchResultLines(content string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	groups := make([]searchSummaryGroup, 0)
	groupByPath := make(map[string]int)
	fallback := make([]string, 0, limit)
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		path, lineNo, snippet, ok := parseSearchResultLine(trimmed)
		if !ok {
			if len(fallback) < limit {
				fallback = append(fallback, renderSummaryLine(trimmed))
			}
			return true
		}
		idx, ok := groupByPath[path]
		if !ok {
			idx = len(groups)
			groupByPath[path] = idx
			groups = append(groups, searchSummaryGroup{Path: path})
		}
		if len(groups[idx].Matches) < 2 {
			groups[idx].Matches = append(groups[idx].Matches, fmt.Sprintf("%s: %s", lineNo, compactTextSnippet(snippet, 140)))
		}
		return true
	})
	if len(groups) == 0 {
		return fallback
	}
	out := make([]string, 0, limit)
	for _, group := range groups {
		if len(group.Matches) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("- %s: %s", group.Path, strings.Join(group.Matches, "; ")))
		if len(out) >= limit {
			break
		}
	}
	if omitted := len(groups) - len(out); omitted > 0 && len(out) > 0 {
		out[len(out)-1] += fmt.Sprintf("; ... (+%d files)", omitted)
	}
	return out
}

func reduceJSONBlobSummary(ctx requestReductionContext) (string, bool) {
	var decoded any
	dec := json.NewDecoder(strings.NewReader(ctx.Content))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return "", false
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", false
	}
	switch v := decoded.(type) {
	case map[string]any:
		lines := summarizeJSONObjectEntries(v)
		if len(lines) == 0 {
			lines = []string{"- (no preserved entries)"}
		}
		return fmt.Sprintf("[Older %s JSON object summarized to save context; keys=%d]\n%s", toolNameOrUnknown(ctx.ToolName), len(v), strings.Join(lines, "\n")), true
	case []any:
		items := summarizeJSONArrayItems(v, 3)
		if len(items) == 0 {
			items = []string{"- (no preserved items)"}
		}
		return fmt.Sprintf("[Older %s JSON array summarized to save context; items=%d]\n%s", toolNameOrUnknown(ctx.ToolName), len(v), strings.Join(items, "\n")), true
	default:
		return "", false
	}
}

// summarizeJSONObjectEntries keeps scalar values before nested containers — the
// exact fields a model most often re-reads across requests — while rendering
// containers as shape markers.
func summarizeJSONObjectEntries(v map[string]any) []string {
	scalarKeys := make([]string, 0, len(v))
	containerKeys := make([]string, 0, len(v))
	for key, value := range v {
		switch value.(type) {
		case []any, map[string]any:
			containerKeys = append(containerKeys, key)
		default:
			scalarKeys = append(scalarKeys, key)
		}
	}
	slices.Sort(scalarKeys)
	slices.Sort(containerKeys)
	const maxEntries = 8
	scalarCount := min(len(scalarKeys), maxEntries)
	containerCount := min(len(containerKeys), maxEntries-scalarCount)
	omitted := len(v) - scalarCount - containerCount
	out := make([]string, 0, scalarCount+containerCount+min(omitted, 1))
	for _, key := range scalarKeys[:scalarCount] {
		out = append(out, "- "+summarizeJSONKey(key)+": "+summarizeJSONValue(v[key]))
	}
	for _, key := range containerKeys[:containerCount] {
		out = append(out, "- "+summarizeJSONKey(key)+": "+summarizeJSONValue(v[key]))
	}
	if omitted > 0 {
		out = append(out, fmt.Sprintf("- ... (+%d more keys omitted) ...", omitted))
	}
	return out
}

func summarizeJSONKey(key string) string {
	return strconv.Quote(compactTextSnippet(key, summaryLineSnippetChars))
}

// summarizeJSONValue renders a decoded JSON value as a compact summary.
// Scalar values keep their content (truncated); nested containers collapse to
// a shape marker so the summary stays small while still identifying the value
// kind.
func summarizeJSONValue(value any) string {
	switch t := value.(type) {
	case nil:
		return "null"
	case string:
		return strconv.Quote(compactTextSnippet(t, summaryLineSnippetChars))
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return strings.ReplaceAll(compactTextSnippet(t.String(), summaryLineSnippetChars), "\n", " ")
	case []any:
		return fmt.Sprintf("[%d items]", len(t))
	case map[string]any:
		return summarizeJSONNestedKeys(t)
	default:
		if raw, err := json.Marshal(value); err == nil {
			return strings.ReplaceAll(compactTextSnippet(string(raw), summaryLineSnippetChars), "\n", " ")
		}
		return ""
	}
}

// summarizeJSONNestedKeys renders a nested object's key set as a compact shape
// marker; its values are deliberately omitted (the top-level pass already kept
// the scalar values the model reads directly).
func summarizeJSONNestedKeys(v map[string]any) string {
	if len(v) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(v))
	for key := range v {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	const maxNested = 5
	shown := min(len(keys), maxNested)
	for i := range shown {
		keys[i] = summarizeJSONKey(keys[i])
	}
	s := "{" + strings.Join(keys[:shown], ", ")
	if len(keys) > maxNested {
		s += fmt.Sprintf(", ... (+%d more)", len(keys)-maxNested)
	}
	return s + "}"
}

func summarizeJSONArrayItems(items []any, limit int) []string {
	if limit <= 0 || len(items) == 0 {
		return nil
	}
	if limit > len(items) {
		limit = len(items)
	}
	// Sample first/middle/last rather than a naive head slice: arrays are often
	// time-ordered or sorted, but the representative item can sit anywhere.
	// Keeping both ends plus an interior item covers more shapes than three
	// head items, which systematically miss the tail.
	out := make([]string, 0, limit)
	for i := range limit {
		idx := 0
		if limit > 1 {
			idx = (len(items) - 1) * i / (limit - 1)
		}
		rendered, _ := json.Marshal(items[idx])
		out = append(out, fmt.Sprintf("- [%d] %s", idx, strings.ReplaceAll(compactTextSnippet(string(rendered), summaryLineSnippetChars), "\n", " ")))
	}
	return out
}

func reduceLongLogOutputSummary(ctx requestReductionContext) string {
	counts := summarizeLogSignalCounts(ctx.Content)
	lines := summarizeRepresentativeLogLines(ctx.Content, 6)
	preserved := len(lines)
	if preserved == 0 {
		lines = []string{"- (no preserved log lines)"}
	}
	// State how much was dropped. This summary keeps only marker lines, so
	// without a count the model cannot tell "one matching line" from "the
	// output only had one line" and may read the summary as the whole result.
	total := countMeaningfulLines(ctx.Content)
	if omitted := total - preserved; omitted > 0 {
		noun := "lines"
		if omitted == 1 {
			noun = "line"
		}
		lines = append(lines, fmt.Sprintf("- ... (%d more %s omitted) ...", omitted, noun))
	}
	return fmt.Sprintf("[Older %s log summarized for this request to save context; lines=%d errors=%d warnings=%d failed=%d]\n%s", toolNameOrUnknown(ctx.ToolName), total, counts.Errors, counts.Warnings, counts.Failures, strings.Join(lines, "\n"))
}

// compactListingHeadLines bounds how many leading records a listing summary
// keeps.
const compactListingHeadLines = 8

// reduceRecordListingOutputSummary summarizes output that the command line
// identified as one record per line. Marker-based selection is wrong for this
// shape: the records are peers, so "salient" is not a property any single line
// has, and the substrings that would be scored (`updated`, `created`,
// `completed`) are ordinary words in commit subjects and branch names. Position
// carries the meaning instead — these listings print newest or current first —
// so the head is kept and the drop is stated as a count rather than inferred
// from what survived.
func reduceRecordListingOutputSummary(ctx requestReductionContext) string {
	lines := make([]string, 0, compactListingHeadLines)
	total := 0
	forEachLine(ctx.Content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		total++
		if len(lines) < compactListingHeadLines {
			lines = append(lines, "- "+compactTextSnippet(trimmed, summaryLineSnippetChars))
		}
		return true
	})
	if len(lines) == 0 {
		lines = append(lines, "- (no records preserved)")
	}
	if omitted := total - len(lines); omitted > 0 {
		noun := "records"
		if omitted == 1 {
			noun = "record"
		}
		lines = append(lines, fmt.Sprintf("- ... (%d later %s omitted) ...", omitted, noun))
	}
	return fmt.Sprintf("[Older %s listing summarized for this request to save context; bytes=%d records=%d]\n%s",
		toolNameOrUnknown(ctx.ToolName), len(ctx.Content), total, strings.Join(lines, "\n"))
}

func reduceShellSuccessOutputSummary(ctx requestReductionContext) string {
	if summary, ok := reduceGoTestSuccessOutputSummary(ctx); ok {
		return summary
	}
	summary := summarizeShellSuccess(ctx.Content, 4)
	if len(summary.Lines) == 0 {
		summary.Lines = []string{"- (no salient output lines preserved)"}
	}
	return fmt.Sprintf("[Older %s success summarized for this request to save context; bytes=%d lines=%d]\n%s", tools.NameShell, len(ctx.Content), summary.MeaningfulLines, strings.Join(summary.Lines, "\n"))
}

// reduceDiffOutputSummary keeps the structural parts of a patch instead of
// routing it through log heuristics. A review can recover the exact patch with
// the original git command, while the summary still identifies changed files,
// hunks, and representative additions/removals.
func reduceDiffOutputSummary(content string) string {
	const maxFiles = 12
	const maxChangesPerHunk = 3
	const maxChangesTotal = 36

	type diffFile struct {
		name           string
		hunks          int
		changes        int
		kind           string
		shownChanges   []string
		omittedChanges int
	}
	files := make([]diffFile, 0)
	current := -1
	currentKind := ""
	hunkChanges := 0
	shownChanges := 0
	pendingUnifiedPath := ""
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "*** Update File:") || strings.HasPrefix(trimmed, "*** Add File:") || strings.HasPrefix(trimmed, "*** Delete File:"):
			name := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(trimmed, "*** Update File:"), "*** Add File:"), "*** Delete File:"))
			name = compactTextSnippet(name, summaryLineSnippetChars)
			if len(files) < maxFiles {
				files = append(files, diffFile{name: name})
				current = len(files) - 1
			} else {
				current = -1
			}
			currentKind = "patch"
			hunkChanges = 0
		case strings.HasPrefix(trimmed, "diff --git ") || strings.HasPrefix(trimmed, "diff --combined ") || strings.HasPrefix(trimmed, "diff --cc "):
			name := compactTextSnippet(diffSummaryGitHeaderPath(trimmed), summaryLineSnippetChars)
			if len(files) < maxFiles {
				files = append(files, diffFile{name: name})
				current = len(files) - 1
			} else {
				current = -1
			}
			currentKind = "git"
			hunkChanges = 0
		case currentKind != "patch" && strings.HasPrefix(trimmed, "--- "):
			pendingUnifiedPath = strings.TrimPrefix(trimmed, "--- ")
		case currentKind != "patch" && strings.HasPrefix(trimmed, "+++ ") && pendingUnifiedPath != "":
			name := diffSummaryPath(strings.TrimPrefix(trimmed, "+++ "))
			if name == "/dev/null" {
				name = diffSummaryPath(pendingUnifiedPath)
			}
			if currentKind == "git" && current >= 0 {
				files[current].name = compactTextSnippet(name, summaryLineSnippetChars)
			} else {
				name = compactTextSnippet(name, summaryLineSnippetChars)
				if len(files) < maxFiles {
					files = append(files, diffFile{name: name})
					current = len(files) - 1
				} else {
					current = -1
				}
			}
			currentKind = "unified"
			pendingUnifiedPath = ""
		case current >= 0 && (strings.HasPrefix(trimmed, "GIT binary patch") || strings.HasPrefix(trimmed, "Binary files ")):
			files[current].kind = "binary"
		case current >= 0 && (strings.HasPrefix(trimmed, "old mode ") || strings.HasPrefix(trimmed, "new mode ") || strings.HasPrefix(trimmed, "new file mode ") || strings.HasPrefix(trimmed, "deleted file mode ")):
			files[current].kind = "mode"
		case current >= 0 && (strings.HasPrefix(trimmed, "similarity index ") || strings.HasPrefix(trimmed, "rename from ") || strings.HasPrefix(trimmed, "rename to ")):
			files[current].kind = "rename"
		case current >= 0 && (strings.HasPrefix(trimmed, "copy from ") || strings.HasPrefix(trimmed, "copy to ")):
			files[current].kind = "copy"
		case strings.HasPrefix(trimmed, "@@"):
			if current >= 0 {
				files[current].hunks++
			}
			hunkChanges = 0
		case current >= 0 && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) &&
			!strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---"):
			files[current].changes++
			if hunkChanges < maxChangesPerHunk && shownChanges < maxChangesTotal {
				files[current].shownChanges = append(files[current].shownChanges, compactTextSnippet(trimmed, summaryLineSnippetChars))
				shownChanges++
			} else {
				files[current].omittedChanges++
			}
			hunkChanges++
		}
		return true
	})

	lines := make([]string, 0, len(files)*2+2)
	for _, file := range files {
		line := fmt.Sprintf("- %s: hunks=%d changes=%d", file.name, file.hunks, file.changes)
		if file.kind != "" {
			line += "; kind=" + file.kind
		}
		if len(file.shownChanges) > 0 {
			line += "; shown: " + strings.Join(file.shownChanges, " | ")
		}
		if file.omittedChanges > 0 {
			line += fmt.Sprintf("; %d changes omitted", file.omittedChanges)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, "- (no diff file headers preserved)")
	}
	if omitted := countDiffFileHeaders(content) - len(files); omitted > 0 {
		lines = append(lines, fmt.Sprintf("- ... (%d additional files omitted) ...", omitted))
	}
	return fmt.Sprintf("[Older git diff summarized for this request; bytes=%d lines=%d]\n%s", len(content), countMeaningfulLines(content), strings.Join(lines, "\n"))
}

func diffSummaryPath(path string) string {
	path = strings.TrimSpace(path)
	if fields := strings.SplitN(path, "\t", 2); len(fields) > 0 {
		path = fields[0]
	}
	path = strings.Trim(path, "\"")
	if path == "/dev/null" {
		return path
	}
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return path
}

func diffSummaryGitHeaderPath(header string) string {
	header = strings.TrimSpace(header)
	for _, prefix := range []string{"diff --git ", "diff --combined ", "diff --cc "} {
		if !strings.HasPrefix(header, prefix) {
			continue
		}
		paths := strings.TrimSpace(strings.TrimPrefix(header, prefix))
		if idx := strings.LastIndex(paths, " b/"); idx >= 0 {
			return diffSummaryPath(paths[idx+1:])
		}
		if idx := strings.LastIndex(paths, `"b/`); idx >= 0 {
			return diffSummaryPath(paths[idx+1:])
		}
		fields := strings.Fields(paths)
		if len(fields) > 0 {
			return diffSummaryPath(fields[len(fields)-1])
		}
	}
	return header
}

func countDiffFileHeaders(content string) int {
	count := 0
	pendingUnified := false
	explicitKind := ""
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "diff --git ") || strings.HasPrefix(trimmed, "diff --combined ") || strings.HasPrefix(trimmed, "diff --cc "):
			count++
			pendingUnified = false
			explicitKind = "git"
		case strings.HasPrefix(trimmed, "*** Update File:") || strings.HasPrefix(trimmed, "*** Add File:") || strings.HasPrefix(trimmed, "*** Delete File:"):
			count++
			pendingUnified = false
			explicitKind = "patch"
		case explicitKind != "patch" && strings.HasPrefix(trimmed, "--- "):
			pendingUnified = true
		case explicitKind != "patch" && strings.HasPrefix(trimmed, "+++ ") && pendingUnified:
			if explicitKind != "git" {
				count++
			}
			explicitKind = "unified"
			pendingUnified = false
		}
		return true
	})
	return count
}

type goTestSuccessSummary struct {
	PackagesOK  int
	Cached      int
	NoTestFiles int
	Coverage    int
	Failed      bool
	Lines       []string
}

func reduceGoTestSuccessOutputSummary(ctx requestReductionContext) (string, bool) {
	if isToolResultUnsuccessfulStatus(ctx.ToolStatus) || !isDirectGoTestCommandMemo(ctx.parseMemo, ctx.ToolCallID, ctx.Meta.Args) {
		return "", false
	}
	summary := summarizeGoTestSuccess(ctx.Content, 4)
	if summary.PackagesOK == 0 || summary.Failed {
		return "", false
	}
	if len(summary.Lines) == 0 {
		summary.Lines = []string{"- (no package result lines preserved)"}
	}
	return fmt.Sprintf(
		"[Older %s go test success summarized for this request to save context; bytes=%d lines=%d packages_ok=%d cached=%d no_test_files=%d coverage_reports=%d]\n%s",
		tools.NameShell,
		len(ctx.Content),
		countMeaningfulLines(ctx.Content),
		summary.PackagesOK,
		summary.Cached,
		summary.NoTestFiles,
		summary.Coverage,
		strings.Join(summary.Lines, "\n"),
	), true
}

// commandDerivedShellShape is the gate-agnostic wrapper each shape branch
// consults. It is deliberately not hoisted above the branches: the three of
// them apply different age, size and tool-count gates, and the command may only
// decide *which* summary an output gets, never *whether* it is summarized at
// all. Hoisting it would silently widen the reduction surface.
func commandDerivedShellShape(ctx requestReductionContext) (requestReductionClass, bool) {
	if ctx.ToolName != tools.NameShell {
		return requestReductionNone, false
	}
	return shellOutputShapeFromCommandMemo(ctx.parseMemo, ctx.ToolCallID, ctx.Meta.Args)
}

// shellOutputShapeFromCommand returns the reduction class a shell result takes
// when the command that produced it settles the shape, and false when it does
// not. Reading the command beats sniffing the bytes it printed: the reduction
// layer holds the exact command line, while the shape rules below it inspect
// the output, where a payload can imitate a shape it does not have. `git log
// --oneline` is the standing example — a couple of commit subjects containing
// "fail" are all looksLikeBuildLikeLog needs to route a whole commit listing
// through the failure-signal log summary and keep one line of it. A command
// name cannot be imitated that way.
//
// The table stays deliberately small: only commands whose output shape is
// unambiguous, and only when the command is a single invocation, so a pipeline
// that reshapes the output (`... | jq`) keeps the heuristics. Everything
// unmapped falls through unchanged, which makes this a subtraction from the
// sniffing surface rather than a replacement for it.
func shellOutputShapeFromCommand(argsJSON string) (requestReductionClass, bool) {
	return shellOutputShapeFromCommandMemo(nil, "", argsJSON)
}

func shellOutputShapeFromCommandMemo(memo *reductionToolCallMemo, toolCallID, argsJSON string) (requestReductionClass, bool) {
	literal, ok := memo.shellInvocationLiteralArgs(toolCallID, argsJSON, "git")
	if !ok || literal[0] != "git" {
		return requestReductionNone, false
	}
	sub, ok := gitSubcommand(literal)
	if !ok {
		return requestReductionNone, false
	}
	switch sub {
	// Listing and status subcommands print one record per line: durable
	// evidence the model reads as a whole, and never a build log, a search
	// result or a JSON document. `git log -p` and `git show` do emit a diff,
	// but the diff rules run ahead of the shell branch and claim them first.
	case "log", "status", "branch", "tag", "remote", "rev-parse", "stash":
		return requestReductionListing, true
	}
	return requestReductionNone, false
}

// gitSubcommand returns the subcommand of a git invocation, skipping the global
// options that may precede it (`git -C dir log`, `git --no-pager log`). An
// unrecognised option reports false rather than guessing: mistaking an option
// for the subcommand would reintroduce exactly the misreading the command table
// exists to remove.
func gitSubcommand(literal []string) (string, bool) {
	for i := 1; i < len(literal); {
		arg := literal[i]
		if !strings.HasPrefix(arg, "-") {
			return arg, true
		}
		switch {
		case arg == "-C" || arg == "-c" || arg == "--git-dir" || arg == "--work-tree" ||
			arg == "--namespace" || arg == "--exec-path":
			i += 2 // option and its separate value
		case arg == "--no-pager" || arg == "-p" || arg == "--paginate" || arg == "--bare" ||
			arg == "--no-replace-objects" || arg == "--literal-pathspecs" ||
			arg == "--no-optional-locks" || strings.HasPrefix(arg, "--git-dir=") ||
			strings.HasPrefix(arg, "--work-tree=") || strings.HasPrefix(arg, "--namespace=") ||
			strings.HasPrefix(arg, "--exec-path="):
			i++
		default:
			return "", false
		}
	}
	return "", false
}

// singleShellInvocationLiteralArgs returns the literal argv of a shell tool call
// that is exactly one invocation, so a pipeline or a substitution that reshapes
// the output never reaches a command-derived rule. program is the name the
// caller is looking for: it is checked as a plain substring first, which is a
// necessary condition and lets a command line that never mentions it skip the
// AST parse — that parse runs for every shell result on every request.
func singleShellInvocationLiteralArgs(argsJSON, program string) ([]string, bool) {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(argsJSON), &args) != nil {
		return nil, false
	}
	if !strings.Contains(args.Command, program) {
		return nil, false
	}
	analysis, err := tools.AnalyzeShellCommand(args.Command)
	if err != nil || len(analysis.Subcommands) != 1 {
		return nil, false
	}
	literal := analysis.Subcommands[0].LiteralArgs
	if len(literal) < 2 {
		return nil, false
	}
	return literal, true
}

func isDirectGoTestCommandMemo(memo *reductionToolCallMemo, toolCallID, argsJSON string) bool {
	literal, ok := memo.shellInvocationLiteralArgs(toolCallID, argsJSON, "go")
	if !ok || literal[0] != "go" || literal[1] != "test" {
		return false
	}
	for _, arg := range literal[2:] {
		if arg == "-json" || strings.HasPrefix(arg, "-json=") {
			return false
		}
	}
	return true
}

func summarizeGoTestSuccess(content string, limit int) goTestSuccessSummary {
	var summary goTestSuccessSummary
	if limit <= 0 {
		return summary
	}
	resultLines := make([]string, 0, limit)
	appendResult := func(line string) {
		line = compactTextSnippet(line, summaryLineSnippetChars)
		if slices.Contains(resultLines, line) {
			return
		}
		if len(resultLines) < limit {
			resultLines = append(resultLines, line)
			return
		}
		copy(resultLines, resultLines[1:])
		resultLines[len(resultLines)-1] = line
	}
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if trimmed == "FAIL" || strings.HasPrefix(trimmed, "FAIL\t") || strings.HasPrefix(trimmed, "--- FAIL:") || strings.HasPrefix(trimmed, "panic:") {
			summary.Failed = true
			return true
		}
		if len(fields) >= 2 && fields[0] == "ok" {
			summary.PackagesOK++
			if strings.Contains(trimmed, "(cached)") {
				summary.Cached++
			}
			if strings.Contains(strings.ToLower(trimmed), "coverage:") {
				summary.Coverage++
			}
			appendResult(trimmed)
			return true
		}
		if len(fields) >= 3 && fields[0] == "?" && strings.Contains(trimmed, "[no test files]") {
			summary.NoTestFiles++
			return true
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "coverage:") {
			summary.Coverage++
			appendResult(trimmed)
		}
		return true
	})
	for _, line := range resultLines {
		summary.Lines = append(summary.Lines, "- "+strings.ReplaceAll(line, "\n", " "))
	}
	return summary
}

type logSignalCounts struct {
	Errors   int
	Warnings int
	Failures int
}

func summarizeLogSignalCounts(content string) logSignalCounts {
	var out logSignalCounts
	forEachLine(content, func(line string) bool {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			return true
		}
		if strings.Contains(lower, "error") || strings.Contains(lower, "panic:") || strings.Contains(lower, "exception") || strings.Contains(lower, "traceback") {
			out.Errors++
		}
		if strings.Contains(lower, "warning") || strings.Contains(lower, "warn") {
			out.Warnings++
		}
		if strings.Contains(lower, "failed") || strings.Contains(lower, "failure") {
			out.Failures++
		}
		return true
	})
	return out
}

// summarizeLinesMatching collects up to limit de-duplicated lines for which
// keep reports true, each rendered as a single-line bullet. It backs the
// log-line and error-line summaries, which differ only in their predicate.
func summarizeLinesMatching(content string, limit int, keep func(trimmed string) bool) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	seen := make(map[string]struct{})
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !keep(trimmed) {
			return true
		}
		lineKey := compactTextSnippet(trimmed, summaryLineSnippetChars)
		if _, ok := seen[lineKey]; ok {
			return true
		}
		seen[lineKey] = struct{}{}
		out = append(out, "- "+strings.ReplaceAll(lineKey, "\n", " "))
		return len(out) < limit
	})
	return out
}

func summarizeRepresentativeLogLines(content string, limit int) []string {
	return summarizeLinesMatching(content, limit, func(trimmed string) bool {
		return containsAnyMarker(strings.ToLower(trimmed), logLineMarkers)
	})
}

func summarizeImportantLines(content string, limit int) []string {
	return summarizeLinesMatching(content, limit, isImportantSummaryLine)
}

type shellSuccessSummary struct {
	Lines           []string
	MeaningfulLines int
}

func summarizeShellSuccess(content string, limit int) shellSuccessSummary {
	if limit <= 0 {
		return shellSuccessSummary{}
	}
	weakLimit := max(1, limit-1)
	tailLimit := min(3, limit)
	strong := make([]string, 0, limit)
	weak := make([]string, 0, weakLimit)
	tail := make([]string, 0, tailLimit)
	summary := shellSuccessSummary{}

	appendSignal := func(lines []string, line string, lineLimit int) []string {
		if slices.Contains(lines, line) {
			return lines
		}
		if len(lines) < lineLimit {
			return append(lines, line)
		}
		copy(lines, lines[1:])
		lines[lineLimit-1] = line
		return lines
	}
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		summary.MeaningfulLines++
		lineKey := compactTextSnippet(trimmed, summaryLineSnippetChars)
		if isStrongShellSuccessSummaryLine(trimmed) {
			strong = appendSignal(strong, lineKey, limit)
		}
		if isWeakShellSuccessSummaryLine(trimmed) {
			weak = appendSignal(weak, lineKey, weakLimit)
		}
		if len(tail) < tailLimit {
			tail = append(tail, lineKey)
		} else {
			copy(tail, tail[1:])
			tail[tailLimit-1] = lineKey
		}
		return true
	})

	renderLines := func(lines []string) []string {
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			out = append(out, "- "+strings.ReplaceAll(line, "\n", " "))
		}
		return out
	}
	appendTailFallback := func(lines []string) []string {
		if len(lines) >= limit || len(tail) == 0 {
			return lines
		}
		lastTail := "- " + strings.ReplaceAll(tail[len(tail)-1], "\n", " ")
		if slices.Contains(lines, lastTail) {
			return lines
		}
		return append(lines, lastTail)
	}

	if len(strong) > 0 {
		summary.Lines = appendTailFallback(renderLines(strong))
		return summary
	}
	if len(weak) > 0 {
		summary.Lines = appendTailFallback(renderLines(weak))
		return summary
	}
	if omitted := summary.MeaningfulLines - len(tail); omitted > 0 {
		noun := "lines"
		if omitted == 1 {
			noun = "line"
		}
		summary.Lines = append(summary.Lines, fmt.Sprintf("- ... (%d %s omitted) ...", omitted, noun))
	}
	summary.Lines = append(summary.Lines, renderLines(tail)...)
	return summary
}

func isStrongShellSuccessSummaryLine(trimmed string) bool {
	lower := strings.ToLower(strings.TrimSpace(trimmed))
	if strings.Contains(lower, "coverage") || strings.Contains(lower, "benchmark") {
		return true
	}
	return strings.HasPrefix(lower, "pass ") ||
		strings.HasPrefix(lower, "pass:") ||
		strings.HasPrefix(lower, "ok ") ||
		strings.HasPrefix(lower, "done") ||
		strings.HasPrefix(lower, "success ") ||
		strings.HasPrefix(lower, "success:") ||
		strings.HasPrefix(lower, "successfully ")
}

func isWeakShellSuccessSummaryLine(trimmed string) bool {
	lower := strings.ToLower(strings.TrimSpace(trimmed))
	return containsAnyMarker(lower, shellSuccessWeakLineMarkers)
}

func isImportantSummaryLine(line string) bool {
	if containsAnyMarker(strings.ToLower(strings.TrimSpace(line)), importantLineMarkers) {
		return true
	}
	_, _, _, ok := parseSearchResultLine(line)
	return ok
}

func summarizeHeadTailLines(content string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	head := limit / 2
	tail := limit - head
	headLines := make([]string, 0, head)
	tailLines := make([]string, 0, tail)
	meaningful := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		meaningful++
		if len(headLines) < head {
			headLines = append(headLines, trimmed)
			return true
		}
		if tail > 0 {
			if len(tailLines) < tail {
				tailLines = append(tailLines, trimmed)
			} else {
				copy(tailLines, tailLines[1:])
				tailLines[tail-1] = trimmed
			}
		}
		return true
	})
	if meaningful == 0 {
		return nil
	}
	if meaningful <= limit {
		out := make([]string, 0, meaningful)
		for _, line := range headLines {
			out = append(out, renderSummaryLine(line))
		}
		for _, line := range tailLines {
			out = append(out, renderSummaryLine(line))
		}
		return out
	}
	out := make([]string, 0, limit+1)
	for _, line := range headLines {
		out = append(out, renderSummaryLine(line))
	}
	omitted := meaningful - limit
	noun := "lines"
	if omitted == 1 {
		noun = "line"
	}
	out = append(out, fmt.Sprintf("- ... (%d %s omitted) ...", omitted, noun))
	for _, line := range tailLines {
		out = append(out, renderSummaryLine(line))
	}
	return out
}

// renderSummaryLine renders one preserved summary line. Artifact reference
// lines ("Full output saved to <path>") stay verbatim: mid-line truncation
// would clip the artifact path and leave the model an unreadable path.
func renderSummaryLine(line string) string {
	if refs := tools.ExtractArtifactReferences(line); len(refs) > 0 {
		return "- " + refs[0]
	}
	return "- " + strings.ReplaceAll(compactTextSnippet(line, summaryLineSnippetChars), "\n", " ")
}

func looksLikePathListOutput(content string) bool {
	matches := 0
	checked := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		checked++
		if pathListLikelyFileRe.MatchString(trimmed) {
			matches++
		}
		if matches >= 5 {
			return false
		}
		if checked >= 24 {
			return false
		}
		return true
	})
	return matches >= 5
}

func countMeaningfulLines(content string) int {
	count := 0
	forEachLine(content, func(line string) bool {
		if strings.TrimSpace(line) != "" {
			count++
		}
		return true
	})
	return count
}

func isNoisyDiagnosticsSummaryLine(line string) bool {
	return strings.HasPrefix(line, "Diagnostics status:") ||
		strings.HasPrefix(line, "Used LSP diagnostics") ||
		strings.HasPrefix(line, "Used Ruff quick diagnostics") ||
		strings.HasPrefix(line, "Full Python semantic diagnostics")
}

func contextReductionIsReadLike(name string) bool {
	return tools.IsReadLike(name)
}

type readRequestSummary struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func parseReadRequestSummary(argsJSON string) readRequestSummary {
	if strings.TrimSpace(argsJSON) == "" {
		return readRequestSummary{}
	}
	var parsed readRequestSummary
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
		return readRequestSummary{}
	}
	parsed.Path = strings.TrimSpace(parsed.Path)
	return parsed
}

type displayedReadRange struct {
	Start int
	End   int
	Total int
	OK    bool
}

func parseDisplayedReadRange(content string) displayedReadRange {
	firstLine, rest, hasMore := strings.Cut(content, "\n")
	firstLine = strings.TrimSpace(firstLine)
	if standard := parseReadResultHeaderRange(firstLine); standard.OK {
		return standard
	}
	if strings.HasPrefix(firstLine, "(showing ") {
		if legacy := parseLegacyDisplayedReadRange(firstLine); legacy.OK {
			return legacy
		}
	}
	if !hasMore || (!strings.Contains(rest, "READ_RESULT") && !strings.Contains(rest, "(showing ")) {
		return displayedReadRange{}
	}

	var found displayedReadRange
	forEachLine(rest, func(line string) bool {
		line = strings.TrimSpace(line)
		if standard := parseReadResultHeaderRange(line); standard.OK {
			found = standard
			return false
		}
		if !strings.HasPrefix(line, "(showing ") {
			return true
		}
		if legacy := parseLegacyDisplayedReadRange(line); legacy.OK {
			found = legacy
			return false
		}
		return true
	})
	return found
}

func readResultShowsWholeFile(content string) bool {
	displayed := parseDisplayedReadRange(content)
	if displayed.OK {
		return displayed.Start == 1 && displayed.End == displayed.Total && !strings.Contains(content, "truncated="+tools.ReadTruncatedBudget)
	}
	return strings.Contains(content, "READ_RESULT lines=none total=0")
}

// parseReadResultHeaderRange parses the standard read metadata line without a
// regexp or field-slice allocation. A full reduction scan calls this for every
// read result, so the common path should remain proportional to header length.
func parseReadResultHeaderRange(line string) displayedReadRange {
	const prefix = "READ_RESULT"
	if !strings.HasPrefix(line, prefix) || (len(line) > len(prefix) && line[len(prefix)] != ' ' && line[len(prefix)] != '\t') {
		return displayedReadRange{}
	}
	var out displayedReadRange
	haveRange := false
	haveTotal := false
	for rest := line[len(prefix):]; len(rest) > 0; {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		field := rest
		if end := strings.IndexAny(rest, " \t"); end >= 0 {
			field = rest[:end]
			rest = rest[end:]
		} else {
			rest = ""
		}
		switch {
		case strings.HasPrefix(field, "lines="):
			rangeText := field[len("lines="):]
			dash := strings.IndexByte(rangeText, '-')
			if dash <= 0 || dash == len(rangeText)-1 {
				continue
			}
			start, startErr := strconv.Atoi(rangeText[:dash])
			end, endErr := strconv.Atoi(rangeText[dash+1:])
			if startErr == nil && endErr == nil && start >= 1 && end >= start {
				out.Start, out.End = start, end
				haveRange = true
			}
		case strings.HasPrefix(field, "total="):
			total, err := strconv.Atoi(field[len("total="):])
			if err == nil && total >= 0 {
				out.Total = total
				haveTotal = true
			}
		}
	}
	out.OK = haveRange && haveTotal
	return out
}

func parseLegacyDisplayedReadRange(line string) displayedReadRange {
	var out displayedReadRange
	if n, _ := fmt.Sscanf(line, "(showing lines %d-%d of %d total", &out.Start, &out.End, &out.Total); n == 3 {
		out.OK = true
		return out
	}
	if n, _ := fmt.Sscanf(line, "(showing line %d of %d total", &out.Start, &out.Total); n == 2 {
		out.End = out.Start
		out.OK = true
		return out
	}
	return displayedReadRange{}
}

// readReductionTruncatedKind picks the READ_RESULT truncation marker and a
// trailing note for a trimmed read output based on its conversation-level
// validity. Reads reach reduction only when invalidated or superseded; the
// distinction matters to the model: stale content must not be trusted, while
// superseded content exists fresher later in context. When the prior content is
// gone, "re-read the file" is not a recovery route at all — re-reading returns
// the replacement — so that case does not offer it.
func readReductionTruncatedKind(ctx requestReductionContext) (kind, note string) {
	if ctx.ReadSuperseded && !ctx.ReadInvalidated {
		return tools.ReadTruncatedSuperseded, "[A newer read of this range appears later in this conversation; prefer that output.]"
	}
	if ctx.ReadPriorContentLost {
		return tools.ReadTruncatedStale, "[File replaced after this read; re-reading returns the current content, not the version above.]"
	}
	return tools.ReadTruncatedStale, "[File modified after this read; the content above may be outdated. Re-read before relying on it.]"
}

func reduceReadOutputSummary(ctx requestReductionContext) string {
	content := ctx.Content
	displayed := parseDisplayedReadRange(content)
	body := stripReadResultHeaderLine(content)
	kind, note := readReductionTruncatedKind(ctx)

	// Preferred path: a READ_RESULT range is present, so rebuild the same header
	// the read tool emits, marked with the validity-specific truncation kind and
	// keeping only leading lines.
	if displayed.OK && strings.TrimSpace(body) != "" {
		headLines, headEnd := reduceReadHeadLines(body, displayed.Start, displayed.End, reduceSnippetChars)
		if len(headLines) > 0 {
			linesField := fmt.Sprintf("%d-%d", displayed.Start, headEnd)
			header := tools.FormatReadResultHeader(linesField, displayed.Total, kind, "", "")
			out := header
			out += "\n" + strings.Join(headLines, "\n")
			if note != "" {
				out += "\n" + note
			}
			return out
		}
	}

	// Fallback for read output without a parseable range (e.g. legacy sessions
	// or non-paged content): keep the path hint and a short excerpt.
	request := ctx.Meta.parsedReadRequest()
	snippet := strings.TrimSpace(compactTextSnippet(content, reduceSnippetChars))
	if snippet == "" {
		snippet = "(no preserved excerpt)"
	}
	if note != "" {
		snippet += "\n" + note
	}
	requestedRange := ""
	if request.Offset > 0 || request.Limit > 0 {
		requestedRange = fmt.Sprintf("; requested range: offset=%d limit=%d", request.Offset, request.Limit)
	}
	if request.Path == "" {
		return "[Older " + tools.NameRead + " output truncated for this request to save context" + requestedRange + "]\n" + snippet
	}
	return fmt.Sprintf("[Older %s output truncated for this request to save context; path=%q%s]\n%s", tools.NameRead, request.Path, requestedRange, snippet)
}

// stripReadResultHeaderLine returns content without its leading READ_RESULT
// metadata line, leaving only the raw source body.
func stripReadResultHeaderLine(content string) string {
	if before, after, ok := strings.Cut(content, "\n"); ok {
		if parseReadResultHeaderRange(strings.TrimSpace(before)).OK {
			return after
		}
	}
	return content
}

// reduceReadHeadLines keeps whole leading lines of a read body within a rough
// character budget, never splitting a line. startLine is the 1-based source
// line of the first body line; endLine bounds the head so it never claims more
// than the original range. It returns the surviving head lines and the source
// line number of the last kept line.
func reduceReadHeadLines(body string, startLine, endLine, budget int) ([]string, int) {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) == 0 {
		return nil, startLine
	}
	maxLines := endLine - startLine + 1
	if maxLines > 0 && maxLines < len(lines) {
		lines = lines[:maxLines]
	}

	kept := 0
	used := 0
	for kept < len(lines) {
		cost := len(lines[kept]) + 1
		if kept > 0 && used+cost > budget {
			break
		}
		used += cost
		kept++
	}
	return lines[:kept], startLine + kept - 1
}

func reduceReadLikeOutputSummary(ctx requestReductionContext) string {
	switch strings.TrimSpace(ctx.ToolName) {
	case tools.NameRead:
		return reduceReadOutputSummary(ctx)
	case tools.NameWebFetch:
		return reduceWebFetchOutputSummary(ctx.Meta.Args, ctx.Content)
	default:
		return staleOutputOmittedMarker(ctx.ToolName)
	}
}

func reduceWebFetchOutputSummary(argsJSON, content string) string {
	var parsed struct {
		URL       string `json:"url"`
		Raw       bool   `json:"raw"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &parsed)
	snippetSource := stripWebFetchResultHeaders(content)
	snippet := strings.TrimSpace(compactTextSnippet(snippetSource, reduceSnippetChars))
	if snippet == "" {
		snippet = "(no preserved excerpt)"
	}
	h := contentFingerprint(content)
	return fmt.Sprintf(
		"[Older %s output truncated for this request to save context; url=%q raw=%t timeout_ms=%d content_fnv1a64=%016x]\n%s",
		tools.NameWebFetch,
		strings.TrimSpace(parsed.URL),
		parsed.Raw,
		parsed.TimeoutMs,
		h,
		snippet,
	)
}

// contentFingerprint returns a compact non-cryptographic fingerprint of an
// output, used only to detect whether a re-fetch returns changed content.
// It runs on the request hot path: fnv1a-64 over the raw bytes, formatted
// inline by the caller so no intermediate string is allocated.
func contentFingerprint(content string) uint64 {
	const (
		fnvOffset64 = 14695981039346656037
		fnvPrime64  = 1099511628211
	)
	h := uint64(fnvOffset64)
	for i := 0; i < len(content); i++ {
		h ^= uint64(content[i])
		h *= fnvPrime64
	}
	return h
}

func stripWebFetchResultHeaders(content string) string {
	if _, after, ok := strings.Cut(content, "\n\n"); ok {
		return after
	}
	return content
}
