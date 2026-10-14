package requesttrim

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tooloutput"
	"github.com/keakon/chord/internal/tools"
)

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
func ensureReducedOutputRecoverable(class Class, rule, reduced string, ctx Context) (string, bool) {
	if rule == reductionRuleDiagnostics {
		return "", false
	}
	switch class {
	case requestReductionConfirm:
		return "", false
	case ReadLike:
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
		(len(tooloutput.ExtractArtifactReferences(reduced)) > 0 || strings.Contains(reduced, archivedOutputMarkerFragment)) {
		return "", false
	}
	path, ok := writeReducedOutputArchive(ctx)
	if !ok {
		return "", false
	}
	// Canonical reference form: the trailing period is what makes this parse
	// as an artifact reference, so later reductions of this marker carry the
	// address forward instead of dropping it.
	return tooloutput.ArtifactReferencePrefix + path + ".", true
}

// writeReducedOutputArchive persists content under a content-addressed name so
// repeated writes of the same payload converge on one file.
func writeReducedOutputArchive(ctx Context) (string, bool) {
	if strings.TrimSpace(ctx.Content) == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(ctx.Content))
	fileName := fmt.Sprintf("%s-%s.txt", ToolNameOrUnknown(ctx.ToolName), hex.EncodeToString(sum[:6]))
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
	refs := tooloutput.ExtractArtifactReferences(original)
	if len(refs) == 0 {
		return reduced
	}
	// Compare against what the reference parser finds in the summary, not
	// against raw containment: an excerpt line can quote an address behind a
	// list bullet, where the parser no longer recognizes it. A summary of a
	// summary would then mention an address the model cannot follow.
	kept := tooloutput.ExtractArtifactReferences(reduced)
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
func archiveIrreducibleToolOutput(ctx Context) (string, bool) {
	if ctx.ArchiveDir == "" || strings.TrimSpace(ctx.Content) == "" {
		return "", false
	}
	absPath, ok := writeReducedOutputArchive(ctx)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("[Older %s%s%s; read it back to recover the full payload (bytes=%d).]",
		ToolNameOrUnknown(ctx.ToolName), archivedOutputMarkerFragment, absPath, len(ctx.Content)), true
}
