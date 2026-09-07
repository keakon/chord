package agent

import (
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

// retentionLevel names how much of a tool result survives into the request
// surface. It is the vocabulary the reduction rules, the compaction input and
// the archive markers share, so a "what happened to this output" question has
// one answer rather than one per call site. It is derived state: nothing
// persists it, and the durable transcript keeps the full payload regardless.
type retentionLevel string

const (
	// retentionFull keeps the complete result inline.
	retentionFull retentionLevel = "full"
	// retentionStructured keeps a summary carrying key fields or excerpts.
	retentionStructured retentionLevel = "structured"
	// retentionReference keeps only a verifiable pointer (a later identical
	// call, a file range, a confirmation) and no payload.
	retentionReference retentionLevel = "reference"
	// retentionArchived drops the body from the request but leaves an address
	// from which the complete payload can be read back.
	retentionArchived retentionLevel = "archived"
	// retentionHidden means the content only exists in the durable transcript
	// or the diagnostic layer and must not drive current working memory.
	// Request-level reduction never produces it: every lossy rendering it
	// emits keeps either an excerpt or a recovery address.
	retentionHidden retentionLevel = "hidden"
)

// retentionRecovery names the action that gets the omitted bytes back. The
// distinction matters because "re-read the file" and "read the artifact" fail
// in opposite situations: re-reading is wrong once the file changed underneath,
// and an artifact address is the only route left precisely then.
type retentionRecovery string

const (
	// retentionRecoveryNone means nothing was dropped that needs recovering:
	// the payload is either intact or duplicated verbatim elsewhere.
	retentionRecoveryNone retentionRecovery = "none"
	// retentionRecoveryRereadFile means the observed bytes are still on disk.
	retentionRecoveryRereadFile retentionRecovery = "reread_file"
	// retentionRecoveryRerunTool means the same call reproduces the output.
	retentionRecoveryRerunTool retentionRecovery = "rerun_tool"
	// retentionRecoveryReadArtifact means the payload survives at an address.
	retentionRecoveryReadArtifact retentionRecovery = "read_artifact"
	// retentionRecoveryUnavailable marks the failure mode this layer exists to
	// prevent: a one-shot payload summarized with no address to get it back.
	retentionRecoveryUnavailable retentionRecovery = "unavailable"
)

// retentionValidity separates "this still describes current state" from "this
// is a historical observation". Conflating the two is what lets a stale read
// keep reading as the current file.
type retentionValidity string

const (
	retentionValidityCurrent    retentionValidity = "current"
	retentionValidityHistorical retentionValidity = "historical"
	retentionValidityStale      retentionValidity = "stale"
	retentionValiditySuperseded retentionValidity = "superseded"
)

// retentionConfidence separates decisions taken from a first-hand signal
// (tool status, tracked file revision, the command line itself) from decisions
// inferred by sniffing the output bytes. Only the former may justify a lossy
// rendering without an excerpt.
type retentionConfidence string

const (
	retentionConfidenceVerified retentionConfidence = "verified"
	retentionConfidenceInferred retentionConfidence = "inferred"
)

// Reasons for keeping a result complete. They name the authority that
// protected it, not the shape of the bytes.
const (
	retentionReasonCurrentRead       = "current_read_authority"
	retentionReasonRecentHighRisk    = "recent_high_risk"
	retentionReasonRecentError       = "recent_error"
	retentionReasonRecentDiagnostics = "recent_diagnostics"
	retentionReasonReadOnlyShell     = "read_only_shell_protect"
	retentionReasonRecentDiff        = "recent_diff_protect"
	retentionReasonJSONAwaitsStale   = "json_awaits_stale_age"
	retentionReasonNoRuleMatched     = "no_rule_matched"
)

// retentionDecision is the unified statement of what happened to one tool
// result on this request: how much of it survives, why, whether it still
// describes current state, and how the model gets the rest back.
//
// It is deliberately not a persisted record. Request-level reduction rebuilds
// it from the transcript on every request, so it can never drift from the
// history it describes, and nothing downstream can mistake it for a second
// source of truth.
type retentionDecision struct {
	Level       retentionLevel
	Class       requestReductionClass
	Rule        string
	Reason      string
	Recovery    retentionRecovery
	ArtifactRef string
	Validity    retentionValidity
	Confidence  retentionConfidence
	// Complete reports whether the rendering carries the whole payload. A
	// repeated marker is not complete even though no bytes were lost overall:
	// the complete copy lives in another message.
	Complete bool
	// Excerpt reports whether the rendering still carries key fields or lines
	// from the payload rather than only a pointer to it. Level alone cannot
	// answer this: an archived rendering usually keeps its summary as well,
	// and the difference decides whether the model can act without first
	// paying a round trip to the address.
	Excerpt bool
}

// retentionDecisionFor derives the decision for one tool result. class and
// rule come from the classification and reduction that already ran; reduced is
// the rendering they produced (empty when the result was kept complete).
func retentionDecisionFor(ctx requestReductionContext, class requestReductionClass, rule, reduced string) retentionDecision {
	decision := retentionDecision{
		Class:      class,
		Rule:       rule,
		Validity:   retentionValidityFor(ctx, class),
		Confidence: retentionConfidenceFor(ctx, class),
	}
	if class == requestReductionNone {
		decision.Level = retentionFull
		decision.Complete = true
		decision.Recovery = retentionRecoveryNone
		decision.Reason = retentionProtectionReason(ctx)
		return decision
	}
	if refs := tools.ExtractArtifactReferences(reduced); len(refs) > 0 {
		decision.ArtifactRef = refs[0]
	} else if address, ok := archivedOutputMarkerAddress(reduced); ok {
		decision.ArtifactRef = address
	}
	// A rendering that spans more than its own marker line kept something of
	// the payload: an excerpt, a file list, a diagnostics body.
	decision.Excerpt = strings.Contains(strings.TrimSpace(reduced), "\n")
	decision.Reason = string(class)
	decision.Level = retentionLevelFor(ctx, class, rule, reduced, decision.ArtifactRef)
	decision.Recovery = retentionRecoveryFor(ctx, class, decision.ArtifactRef)
	return decision
}

// retentionLevelFor maps a reduced rendering onto the shared vocabulary. The
// archive address wins over the shape of the summary: once the full payload is
// addressable, how much of it was inlined no longer decides what was lost.
func retentionLevelFor(ctx requestReductionContext, class requestReductionClass, rule, reduced, artifactRef string) retentionLevel {
	if artifactRef != "" || rule == "archived" {
		return retentionArchived
	}
	switch class {
	case requestReductionRepeated, requestReductionConfirm:
		return retentionReference
	case requestReductionReadLike:
		// A read marker that quotes nothing of the file is a pointer to a
		// range, not a summary of it.
		if toolname.Normalize(ctx.ToolName) == tools.NameRead && !strings.Contains(reduced, "\n") {
			return retentionReference
		}
	}
	if rule == "stale" && !strings.Contains(reduced, "\n") {
		// The bare "[Older X output omitted]" marker carries neither excerpt
		// nor address. Nothing above the archive gate should reach it.
		return retentionHidden
	}
	return retentionStructured
}

// archivedOutputMarkerAddress reads the address out of the marker that
// archiveIrreducibleToolOutput writes. That marker predates the artifact
// reference format and is not an artifact reference, so it needs its own
// reader — without one a one-shot payload that was archived correctly would
// still be reported as unrecoverable.
func archivedOutputMarkerAddress(reduced string) (string, bool) {
	_, rest, ok := strings.Cut(reduced, archivedOutputMarkerFragment)
	if !ok {
		return "", false
	}
	path, _, ok := strings.Cut(rest, ";")
	if !ok {
		return "", false
	}
	path = strings.TrimSpace(path)
	return path, path != ""
}

// retentionRecoveryFor names how the omitted bytes come back.
func retentionRecoveryFor(ctx requestReductionContext, class requestReductionClass, artifactRef string) retentionRecovery {
	if artifactRef != "" {
		return retentionRecoveryReadArtifact
	}
	toolName := toolname.Normalize(ctx.ToolName)
	switch class {
	case requestReductionRepeated:
		// An identical call still carries the full output later in the same
		// request, so there is nothing to recover.
		return retentionRecoveryNone
	case requestReductionConfirm:
		// A confirmation has no payload beyond the fact that it succeeded.
		return retentionRecoveryNone
	}
	if irreducibleToolOutputRequiresArchive(toolName) {
		// A spawn or a notification cannot be replayed and has no URL to
		// re-fetch: without an address its payload is simply gone.
		return retentionRecoveryUnavailable
	}
	if toolName == tools.NameRead {
		if ctx.ReadPriorContentLost {
			return retentionRecoveryUnavailable
		}
		return retentionRecoveryRereadFile
	}
	return retentionRecoveryRerunTool
}

// retentionValidityFor answers whether the rendering still describes current
// state. Only reads carry tracked validity; every other output is a historical
// observation the moment a later turn could have changed the world, which is
// why an aged-out result is never reported as current.
func retentionValidityFor(ctx requestReductionContext, class requestReductionClass) retentionValidity {
	if toolname.Normalize(ctx.ToolName) == tools.NameRead {
		switch {
		case ctx.ReadInvalidated:
			return retentionValidityStale
		case ctx.ReadSuperseded:
			return retentionValiditySuperseded
		}
	}
	if ctx.DiagnosticsSuperseded {
		return retentionValiditySuperseded
	}
	if class == requestReductionNone {
		return retentionValidityCurrent
	}
	return retentionValidityHistorical
}

// retentionConfidenceFor separates first-hand signals from byte sniffing.
func retentionConfidenceFor(ctx requestReductionContext, class requestReductionClass) retentionConfidence {
	toolName := toolname.Normalize(ctx.ToolName)
	if toolName == tools.NameRead {
		// Read validity and read retention both come from tracked file state.
		return retentionConfidenceVerified
	}
	switch class {
	case requestReductionNone:
		if isToolResultUnsuccessfulStatus(ctx.ToolStatus) || ctx.ShellReadOnly {
			return retentionConfidenceVerified
		}
	case requestReductionToolError:
		if isToolResultErrorStatus(ctx.ToolStatus) {
			return retentionConfidenceVerified
		}
	case requestReductionRepeated:
		// Repetition is decided by comparing recorded call arguments and
		// output bytes, not by guessing at the shape.
		return retentionConfidenceVerified
	}
	if _, ok := commandDerivedShellShape(ctx); ok {
		return retentionConfidenceVerified
	}
	return retentionConfidenceInferred
}

// retentionProtectionReason names which rule kept a result complete. The order
// mirrors classifyRequestReductionToolOutput so the reason a reader sees is the
// branch that actually fired.
func retentionProtectionReason(ctx requestReductionContext) string {
	if ctx.Age < ctx.Policy.HighRiskProtectAgeTurns && isHighRiskToolOutput(ctx) {
		return retentionReasonRecentHighRisk
	}
	if ctx.readRetentionProtects() {
		return retentionReasonCurrentRead
	}
	failed := isToolResultErrorStatus(ctx.ToolStatus) ||
		(strings.TrimSpace(ctx.ToolStatus) == "" && isToolErrorContent(ctx.Content))
	if failed && ctx.Age < ctx.Policy.ErrorAgeTurns {
		return retentionReasonRecentError
	}
	if editLikeToolCarriesDiagnostics(ctx) && ctx.Age < ctx.Policy.ErrorAgeTurns {
		return retentionReasonRecentDiagnostics
	}
	if ctx.ShellReadOnly && ctx.Age < ctx.Policy.ShellReadOnlyAgeTurns {
		return retentionReasonReadOnlyShell
	}
	if ctx.Age < ctx.Policy.DiffProtectAgeTurns && looksLikeDiffOrPatch(ctx.Content) {
		return retentionReasonRecentDiff
	}
	if ctx.Age < ctx.Policy.StaleAgeTurns && looksLikeStructuredJSON(ctx.Content) && !looksLikeJSONLinesLog(ctx.Content) {
		return retentionReasonJSONAwaitsStale
	}
	return retentionReasonNoRuleMatched
}

// editLikeToolCarriesDiagnostics reports whether this is an edit-shaped result
// whose body carries an LSP diagnostics section.
func editLikeToolCarriesDiagnostics(ctx requestReductionContext) bool {
	switch toolname.Normalize(ctx.ToolName) {
	case tools.NameEdit, tools.NameApplyPatch, tools.NameWrite:
		return strings.Contains(ctx.Content, diagnosticsSectionLabel)
	}
	return false
}

// retentionDecisionRecoverable reports whether the decision leaves the model a
// way back to the omitted bytes. It is the invariant the reduction layer must
// hold: a lossy rendering always keeps either the payload's key fields or an
// address, never neither.
func (d retentionDecision) retentionDecisionRecoverable() bool {
	if d.Complete {
		return true
	}
	switch d.Recovery {
	case retentionRecoveryNone, retentionRecoveryRereadFile, retentionRecoveryRerunTool, retentionRecoveryReadArtifact:
		return true
	}
	return false
}

// reducedArtifactDirName is the directory request-level reduction archives
// into; toolOutputDirName is the tool layer's own overflow directory. A read
// addressing either is the model exercising a recovery route.
const (
	reducedArtifactDirName = "reduced-artifacts/"
	toolOutputDirName      = "tool-outputs/"
)

// artifactReadbackStats counts how often this transcript reads a payload back
// from a recovery address, and how often that read failed. Both halves matter:
// the read count says whether the addresses are used at all, and the failure
// count is the only way an address that no longer resolves becomes visible
// instead of silently reading as "the model chose not to look".
func artifactReadbackStats(messages []message.Message, callMeta map[string]toolCallMeta, sessionDir string) (reads, failures int) {
	for i := range messages {
		if messages[i].Role != message.RoleTool {
			continue
		}
		meta, ok := callMeta[messages[i].ToolCallID]
		if !ok || toolname.Normalize(meta.Name) != tools.NameRead {
			continue
		}
		if !strings.Contains(meta.Args, reducedArtifactDirName) && !strings.Contains(meta.Args, toolOutputDirName) {
			continue
		}
		if sessionDir != "" && !strings.Contains(meta.Args, sessionDir) {
			continue
		}
		reads++
		if isToolResultUnsuccessfulStatus(messages[i].ToolStatus) || isToolErrorContent(messages[i].Content) {
			failures++
		}
	}
	return reads, failures
}

// laterAssistantReferencesToolResult reports whether an assistant message
// after this tool result leans on its specifics — a quoted identifier from the
// output, or an explicit deictic reference to it. Such a result must not be
// rendered as a bare omission marker: the reasoning that cites it stays in the
// request, so removing what it cites leaves the model with a conclusion whose
// evidence it can no longer check.
//
// The scan is deliberately narrow. It looks only at assistant text between
// this result and the end of the transcript, and it only accepts a token that
// is specific enough to have come from this output, so an unrelated later turn
// does not pin every earlier result in place.
func laterAssistantReferencesToolResult(messages []message.Message, idx int) bool {
	if idx < 0 || idx >= len(messages) {
		return false
	}
	tokens := referenceTokensFromToolOutput(messages[idx].Content)
	if len(tokens) == 0 {
		return false
	}
	for i := idx + 1; i < len(messages); i++ {
		if messages[i].Role != message.RoleAssistant {
			continue
		}
		text := messages[i].Content
		if strings.TrimSpace(text) == "" {
			continue
		}
		for _, token := range tokens {
			if strings.Contains(text, token) {
				return true
			}
		}
	}
	return false
}

const (
	// referenceTokenMinLen keeps short, ambiguous fragments (a bare "go", a
	// two-digit number) from pinning a result: they collide with ordinary
	// prose and would make every output look referenced.
	referenceTokenMinLen = 8
	// referenceTokenScanLines bounds the scan so a megabyte log does not cost
	// a full pass per request; identifiers a later turn cites almost always
	// appear in the head of the output, which is also the part any summary
	// keeps.
	referenceTokenScanLines = 60
	// referenceTokenLimit bounds how many candidates one output contributes.
	referenceTokenLimit = 24
)

// referenceTokensFromToolOutput collects the identifiers from a tool output
// that are distinctive enough that finding one in later assistant text implies
// the text is talking about this output: paths, path:line locations, error
// codes and quoted symbols.
func referenceTokensFromToolOutput(content string) []string {
	if content == "" {
		return nil
	}
	var tokens []string
	seen := make(map[string]struct{})
	lines := 0
	forEachLine(content, func(line string) bool {
		lines++
		if lines > referenceTokenScanLines || len(tokens) >= referenceTokenLimit {
			return false
		}
		for _, field := range strings.FieldsFunc(line, isReferenceTokenSeparator) {
			field = strings.Trim(field, ".,:;")
			if len(field) < referenceTokenMinLen || !isDistinctiveReferenceToken(field) {
				continue
			}
			if _, dup := seen[field]; dup {
				continue
			}
			seen[field] = struct{}{}
			tokens = append(tokens, field)
			if len(tokens) >= referenceTokenLimit {
				return false
			}
		}
		return true
	})
	return tokens
}

func isReferenceTokenSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '"', '\'', '`', '(', ')', '[', ']', '{', '}', ',', ';', '<', '>':
		return true
	}
	return false
}

// isDistinctiveReferenceToken accepts tokens that carry structure a prose
// sentence would not produce by accident: a path separator, a file extension,
// a path:line location, or an identifier written in snake/camel case.
func isDistinctiveReferenceToken(token string) bool {
	if strings.ContainsAny(token, "/\\") {
		return true
	}
	if strings.Contains(token, ":") && strings.ContainsAny(token, "0123456789") {
		return true
	}
	if strings.Contains(token, "_") {
		return true
	}
	if strings.Contains(token, ".") && !strings.HasSuffix(token, ".") {
		return true
	}
	hasUpper := strings.ToLower(token) != token
	hasLower := strings.ToUpper(token) != token
	return hasUpper && hasLower
}
