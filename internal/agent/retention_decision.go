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
// result on this request: how much of it survives, why, and how the model gets
// the rest back.
//
// It is derived, not persisted, and it is read by exactly one consumer: the
// per-request retention ledger behind the debug log. Nothing on the request
// surface branches on it, so it is only built when that ledger is being kept —
// a field nobody reads still costs a scan of the payload to fill in.
type retentionDecision struct {
	Level       retentionLevel
	Reason      string
	Recovery    retentionRecovery
	ArtifactRef string
	// Complete reports whether the rendering carries the whole payload. A
	// repeated marker is not complete even though no bytes were lost overall:
	// the complete copy lives in another message.
	Complete bool
}

// retentionDecisionFor derives the decision for one tool result. verdict is the
// classification that already ran, rule and reduced the rendering it produced
// (both empty when the result was kept complete).
func retentionDecisionFor(ctx requestReductionContext, verdict requestReductionVerdict, rule, reduced string) retentionDecision {
	decision := retentionDecision{Reason: verdict.Reason}
	if verdict.Class == requestReductionNone {
		decision.Level = retentionFull
		decision.Complete = true
		decision.Recovery = retentionRecoveryNone
		return decision
	}
	if decision.Reason == "" {
		decision.Reason = string(verdict.Class)
	}
	if refs := tools.ExtractArtifactReferences(reduced); len(refs) > 0 {
		decision.ArtifactRef = refs[0]
	} else if address, ok := archivedOutputMarkerAddress(reduced); ok {
		decision.ArtifactRef = address
	}
	decision.Level = retentionLevelFor(ctx, verdict.Class, rule, reduced, decision.ArtifactRef)
	decision.Recovery = retentionRecoveryFor(ctx, verdict.Class, decision.ArtifactRef)
	return decision
}

// retentionLevelFor maps a reduced rendering onto the shared vocabulary. The
// archive address wins over the shape of the summary: once the full payload is
// addressable, how much of it was inlined no longer decides what was lost.
func retentionLevelFor(ctx requestReductionContext, class requestReductionClass, rule, reduced, artifactRef string) retentionLevel {
	if artifactRef != "" || rule == reductionRuleArchived {
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
