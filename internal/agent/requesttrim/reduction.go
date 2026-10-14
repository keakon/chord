package requesttrim

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const (
	contextReductionWrapUpGraceRequests = 1
)

// Policy controls request-local reduction. It never changes the durable transcript.
type Policy struct {
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

// DefaultPolicy returns the compiled request-reduction thresholds.
func DefaultPolicy() Policy {
	return Policy{
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

// ApplyConfig applies explicit configuration values over the current policy.
func (p *Policy) ApplyConfig(cfg config.ContextReductionConfig) {
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

// Class identifies the selected rendering rule for a tool result.
type Class string

const (
	None                        Class = ""
	requestReductionRepeated    Class = "repeated_output"
	requestReductionToolError   Class = "tool_error"
	requestReductionConfirm     Class = "confirmation"
	Diagnostics                 Class = "diagnostics"
	requestReductionDiff        Class = "diff"
	ReadLike                    Class = "read_like"
	requestReductionSearch      Class = "search_result"
	requestReductionNumberedSrc Class = "numbered_source"
	requestReductionJSON        Class = "json_blob"
	requestReductionLongLog     Class = "long_log"
	requestReductionShellOK     Class = "shell_success"
	requestReductionListing     Class = "record_listing"
	requestReductionSkill       Class = "skill_instruction"
	requestReductionGeneric     Class = "generic_stale"
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

// Context borrows the immutable facts needed to classify and render one tool result.
// Memo may retain derived parses; ArchiveDir explicitly authorizes Render to save a
// recoverable full output there. FileState and input messages are never mutated.
type Context struct {
	ToolName   string
	ToolCallID string
	Meta       ToolCall
	// Memo carries the agent's per-call shell-parse memo so the
	// command-derived shape gates reuse the parse computed for this call on an
	// earlier request. A nil memo falls back to parsing directly.
	Memo        *Memo
	Content     string
	ToolStatus  string
	FileState   *message.ToolFileState
	Age         int
	Policy      Policy
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
	// else, so its summary must carry an archive address (see ReadValidity).
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

// ReadRetentionProtects reports whether this successful read output must not
// be reduced. A read that is still the model's only current view of the file
// content — not invalidated by a later file mutation and not superseded by a
// newer read of the same range — is protected indefinitely regardless of age
// or size: trimming it forces the model either to re-read content it already
// paid for (extra rounds, broken prompt cache) or, worse, to answer from a
// summary it cannot verify. Capacity pressure is compaction's job, not
// reduction's.
func (ctx Context) ReadRetentionProtects() bool {
	if ctx.ToolName != tools.NameRead || message.IsToolResultUnsuccessfulStatus(ctx.ToolStatus) {
		return false
	}
	return !ctx.ReadInvalidated && !ctx.ReadSuperseded
}

// Verdict is what one classification pass concluded about a
// tool result: the class that decides the rendering, plus the branch that
// decided it. Reason is only meaningful for a result kept complete — it names
// which protection fired — and it travels with the class so no second pass has
// to re-derive it from the same bytes. A hand-copied second pass is exactly how
// the reported reason and the branch that actually fired drift apart.
type Verdict struct {
	Class  Class
	Reason string
}

// protectedVerdict states that a named protection kept this result complete.
func protectedVerdict(reason string) Verdict {
	return Verdict{Class: None, Reason: reason}
}

// ReducedVerdict states that a shape rule claimed this result.
func ReducedVerdict(class Class) Verdict {
	return Verdict{Class: class, Reason: string(class)}
}

// Classify returns the verdict for one tool result: the class a
// shape rule claimed, or the reason the result survived reduction.
func Classify(ctx Context) Verdict {
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
		return ReducedVerdict(ReadLike)
	}
	if ctx.Repeated && ctx.Age >= 1 {
		return ReducedVerdict(requestReductionRepeated)
	}
	if ctx.Age < ctx.Policy.HighRiskProtectAgeTurns && isHighRiskToolOutput(ctx) {
		return protectedVerdict(RetentionReasonRecentHighRisk)
	}
	if ctx.ReadRetentionProtects() {
		return protectedVerdict(retentionReasonCurrentRead)
	}
	failed := message.IsToolResultErrorStatus(ctx.ToolStatus) ||
		(strings.TrimSpace(ctx.ToolStatus) == "" && message.IsToolErrorContent(ctx.Content))
	if failed && ctx.Age < ctx.Policy.ErrorAgeTurns {
		// An explicit failure is stronger evidence than any shape-based rule:
		// until it ages past the error threshold it must stay complete so the
		// model sees the exact failure while it is about to act on it. A failing
		// build's path:line:col lines must not be misread as a search result,
		// and a cancelled status must not behave like an error.
		return protectedVerdict(retentionReasonRecentError)
	}
	if ctx.Age >= ctx.Policy.ErrorAgeTurns && failed {
		return ReducedVerdict(requestReductionToolError)
	}
	// Edit-like results carrying a diagnostics section stay complete until the
	// diagnostics summary age (ErrorAgeTurns): the LSP state is the feedback
	// the model iterates on while it is about to act, and the build-log /
	// read-like gates must not miscast a diagnostics block as a long log.
	if (ctx.ToolName == tools.NameEdit || ctx.ToolName == tools.NameApplyPatch || ctx.ToolName == tools.NameWrite) &&
		strings.Contains(ctx.Content, DiagnosticsSectionLabel) && ctx.Age < ctx.Policy.ErrorAgeTurns {
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
	// Resolved once for the gates below: a skill body is instruction state
	// rather than data, and each gate has to keep it off its content-shape
	// rule. For any other tool the check rejects on the tool name alone, so
	// sharing the answer costs nothing here and saves the envelope scan the
	// later gates would otherwise repeat on an aged skill result.
	skillResult := isSkillInstructionResult(ctx)
	// Diffs are durable review evidence, not build logs. Keep them on a
	// dedicated path so source identifiers such as "error" and "failed" do
	// not turn a patch into a misleading log summary. A loaded skill body
	// is instruction state rather than data: it waits for the skill
	// instruction summary below so the envelope anchors survive.
	if ctx.Age >= ctx.Policy.ShellSuccessAgeTurns &&
		len(ctx.Content) > max(ctx.Policy.ShellSuccessBytes, ctx.Policy.ReadLikeOutputBytes) &&
		!skillResult &&
		looksLikeDiffOrPatch(ctx.Content) {
		return ReducedVerdict(requestReductionDiff)
	}
	if ctx.Age >= ctx.Policy.ConfirmAgeTurns && isConfirmationOutput(ctx.Content) {
		return ReducedVerdict(requestReductionConfirm)
	}
	if ctx.Age >= ctx.Policy.ErrorAgeTurns && (ctx.ToolName == tools.NameEdit || ctx.ToolName == tools.NameApplyPatch || ctx.ToolName == tools.NameWrite) && strings.Contains(ctx.Content, DiagnosticsSectionLabel) {
		return ReducedVerdict(Diagnostics)
	}
	if ctx.Age >= ctx.Policy.ShellSuccessAgeTurns && len(ctx.Content) > ctx.Policy.ShellSuccessBytes && ctx.ToolName == tools.NameShell {
		// The command line is a first-hand statement of what this output is;
		// the heuristics below are second-hand inference from the bytes. Prefer
		// the former wherever it is conclusive.
		if shape, ok := commandDerivedShellShape(ctx); ok {
			return ReducedVerdict(shape)
		}
		if LooksLikeSearchResult(ctx) {
			return ReducedVerdict(requestReductionSearch)
		}
		if looksLikeStructuredJSON(ctx.Content) {
			// The JSON skeleton (top-level keys / first items) is the lossiest
			// summary shape, and the model usually consumes values over several
			// requests, so a JSON document waits for the stale age instead of
			// the shell age. NDJSON log streams get no such retention.
			if !looksLikeJSONLinesLog(ctx.Content) && ctx.Age < ctx.Policy.StaleAgeTurns {
				return protectedVerdict(retentionReasonJSONAwaitsStale)
			}
			return ReducedVerdict(requestReductionJSON)
		}
		if looksLikeNumberedSourceOutput(ctx.Content) {
			return ReducedVerdict(requestReductionNumberedSrc)
		}
		if looksLikeBuildLikeLog(ctx) {
			return ReducedVerdict(requestReductionLongLog)
		}
		return ReducedVerdict(requestReductionShellOK)
	}
	// An invalidated or superseded read was already classified above; a web
	// fetch or other read-like output waits for the age gate here. A loaded
	// skill body is instruction state rather than data, so no content-shape
	// rule may describe it as search hits, numbered source or a log: those
	// summaries drop the envelope anchors the workflow's relative paths
	// resolve against. It waits for the skill instruction summary below.
	if ctx.Age >= ctx.Policy.ReadLikeAgeTurns && len(ctx.Content) > ctx.Policy.ReadLikeOutputBytes && !skillResult {
		// Reached by a shell result only when the shell gate above is
		// configured looser than this one (higher ShellSuccessAgeTurns or
		// ShellSuccessBytes); at the default equal thresholds that gate already
		// returned. Kept so a custom policy does not silently fall back to
		// sniffing the bytes.
		if shape, ok := commandDerivedShellShape(ctx); ok {
			return ReducedVerdict(shape)
		}
		switch {
		case LooksLikeSearchResult(ctx):
			return ReducedVerdict(requestReductionSearch)
		// Read-like tools take precedence over the JSON shape: a web fetch
		// of a JSON document must keep its URL rather than collapsing to a
		// key list.
		case IsReadLike(ctx.ToolName):
			return ReducedVerdict(ReadLike)
		case looksLikeStructuredJSON(ctx.Content):
			if !looksLikeJSONLinesLog(ctx.Content) && ctx.Age < ctx.Policy.StaleAgeTurns {
				return protectedVerdict(retentionReasonJSONAwaitsStale)
			}
			return ReducedVerdict(requestReductionJSON)
		case looksLikeNumberedSourceOutput(ctx.Content):
			return ReducedVerdict(requestReductionNumberedSrc)
		case looksLikeBuildLikeLog(ctx):
			return ReducedVerdict(requestReductionLongLog)
		}
	}
	if ctx.ToolResults >= ctx.Policy.MinToolResultsPrune && ctx.Age >= ctx.Policy.StaleAgeTurns && len(ctx.Content) > ctx.Policy.StaleOutputBytes {
		// A skill body keeps the envelope anchors and a workflow outline: the
		// full text stays recoverable at the archived address, but the model
		// still has to resolve the workflow's relative references against the
		// root this copy was loaded from.
		if skillResult {
			return ReducedVerdict(requestReductionSkill)
		}
		// This is the widest gate (1500 bytes), so it is where a real
		// `git log --oneline -30` actually lands — the shell branch above never
		// sees it. The command signal has to be applied here too.
		if shape, ok := commandDerivedShellShape(ctx); ok {
			return ReducedVerdict(shape)
		}
		if LooksLikeSearchResult(ctx) {
			return ReducedVerdict(requestReductionSearch)
		}
		if IsReadLike(ctx.ToolName) {
			return ReducedVerdict(ReadLike)
		}
		if looksLikeStructuredJSON(ctx.Content) {
			return ReducedVerdict(requestReductionJSON)
		}
		if looksLikeNumberedSourceOutput(ctx.Content) {
			return ReducedVerdict(requestReductionNumberedSrc)
		}
		if looksLikeBuildLikeLog(ctx) {
			return ReducedVerdict(requestReductionLongLog)
		}
		return ReducedVerdict(requestReductionGeneric)
	}
	return protectedVerdict(retentionReasonNoRuleMatched)
}

// NextReviewAge returns the next request age at which an
// unchanged non-read tool result can cross an age-based reduction rule. Zero
// means no later age threshold can change its classification; other state
// changes (repeat detection and the generic tool-count gate) are handled by the
// caller before consulting this frontier.
func NextReviewAge(ctx Context) int {
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

func isHighRiskToolOutput(ctx Context) bool {
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

// Render returns the replacement text, applied rule, and whether reduction succeeded.
// Before discarding non-reconstructible bytes it may write their complete payload under
// ctx.ArchiveDir. Archive writes are best effort; success reports a rendering,
// not an archive commit. Durable history is untouched.
func Render(class Class, ctx Context) (string, string, bool) {
	var reduced string
	var rule string
	switch class {
	case requestReductionRepeated:
		reduced, rule = fmt.Sprintf("[Repeated %s output omitted; an identical call appears later.]", ToolNameOrUnknown(ctx.ToolName)), "repeated"
	case requestReductionToolError:
		reduced, rule = reduceToolErrorOutputSummary(ctx), "error"
	case requestReductionConfirm:
		reduced, rule = "[Confirmed]", "confirmation"
	case Diagnostics:
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
	case ReadLike:
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
	case requestReductionSkill:
		summary := reduceSkillInstructionSummary(ctx)
		if len(summary) >= len(ctx.Content) {
			// A body this small gains nothing from a summary that must carry
			// the anchors anyway: keep the instructions complete instead of
			// trading them for a same-size marker.
			return "", "", false
		}
		reduced, rule = summary, string(requestReductionSkill)
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
