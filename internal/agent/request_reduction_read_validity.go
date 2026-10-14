package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

func reductionReadRevision(meta *requesttrim.ToolCall, state *message.ToolFileState) string {
	if meta == nil || state == nil {
		return ""
	}
	request := meta.ParsedReadRequest()
	if revision := firstReadHashForPath(state, request.Path); revision != "" {
		return revision
	}
	for _, read := range state.Reads {
		if read.Exists && strings.TrimSpace(read.SHA256) != "" {
			return strings.TrimSpace(read.SHA256)
		}
	}
	return ""
}

// shellReadOnlyClassMemo caches the read-only classification of shell calls
// by ToolCallID. A completed call's args never change and the read-only
// allowlist is static, so the verdict is immutable — while the reduction pass
// re-evaluates every shell result still in context on each LLM request.
type shellReadOnlyClassMemo struct {
	mu       sync.Mutex
	verdicts map[string]bool
}

// shellCommandReadOnly reports whether a shell call's command line is on the
// read-only allowlist, memoized per ToolCallID so the JSON args are parsed
// once per call instead of once per LLM request.
func (a *MainAgent) shellCommandReadOnly(toolCallID string, args string) bool {
	if a == nil {
		return false
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return tools.ConcurrencyClassForTool(a.tools, tools.NameShell, json.RawMessage(args)) == tools.ToolConcurrencyClassReadOnly
	}
	a.shellReadOnlyClass.mu.Lock()
	if verdict, ok := a.shellReadOnlyClass.verdicts[toolCallID]; ok {
		a.shellReadOnlyClass.mu.Unlock()
		return verdict
	}
	a.shellReadOnlyClass.mu.Unlock()
	verdict := tools.ConcurrencyClassForTool(a.tools, tools.NameShell, json.RawMessage(args)) == tools.ToolConcurrencyClassReadOnly
	a.shellReadOnlyClass.mu.Lock()
	if a.shellReadOnlyClass.verdicts == nil {
		a.shellReadOnlyClass.verdicts = make(map[string]bool)
	}
	a.shellReadOnlyClass.verdicts[toolCallID] = verdict
	a.shellReadOnlyClass.mu.Unlock()
	return verdict
}

// shellReadInvalidationMemo remembers, per mutating shell result, which
// (path, expected-hash) pairs were already verified against the disk. It is
// keyed by the shell's ToolCallID: a completed command's side effects are
// fixed, so its verdicts never change, and a newer mutating shell resets the
// map.
type shellReadInvalidationMemo struct {
	mu       sync.Mutex
	shellID  string
	verdicts map[string]bool
}

func (a *MainAgent) externallyInvalidatedReadsAfterMutatingShell(messages []message.Message, scan *requesttrim.Scan) map[int]bool {
	if a == nil || a.tools == nil || len(messages) == 0 {
		return nil
	}
	boundary := 0
	if surface, ok := a.stableReductionSurfaceCandidate(a.currentTurnID()); ok {
		boundary = min(len(surface.Messages), len(messages))
	}
	callMeta := scan.CallMeta()
	mutatingShell := false
	shellIndex := -1
	for i := len(messages) - 1; i >= boundary; i-- {
		msg := messages[i]
		if msg.Role != message.RoleTool || message.IsToolResultErrorStatus(msg.ToolStatus) {
			continue
		}
		meta := callMeta[msg.ToolCallID]
		if toolname.Normalize(meta.Name) == tools.NameShell && !a.shellCommandReadOnly(msg.ToolCallID, meta.Args) {
			mutatingShell = true
			shellIndex = i
			break
		}
	}
	if !mutatingShell || shellIndex <= 0 {
		return nil
	}
	type currentFileRevision struct {
		hash   string
		exists bool
		valid  bool
	}
	hashes := make(map[string]currentFileRevision)
	// A completed shell's side effects are fixed, so each (path, expected-hash)
	// pair needs one disk verification per shell result instead of one per LLM
	// request; a newer mutating shell resets the memo and re-verifies.
	shellID := strings.TrimSpace(messages[shellIndex].ToolCallID)
	a.shellReadMemo.mu.Lock()
	defer a.shellReadMemo.mu.Unlock()
	if a.shellReadMemo.shellID != shellID || a.shellReadMemo.verdicts == nil {
		a.shellReadMemo.shellID = shellID
		a.shellReadMemo.verdicts = make(map[string]bool)
	}
	verdicts := a.shellReadMemo.verdicts
	var invalidated map[int]bool
	for i := 0; i < shellIndex; i++ {
		msg := messages[i]
		if msg.Role != message.RoleTool || msg.FileState == nil || toolname.Normalize(callMeta[msg.ToolCallID].Name) != tools.NameRead {
			continue
		}
		for _, read := range msg.FileState.Reads {
			path := strings.TrimSpace(read.Path)
			expected := strings.TrimSpace(read.SHA256)
			if path == "" || expected == "" || !read.Exists {
				continue
			}
			if !filepath.IsAbs(path) && a.effectiveToolBaseDir() != "" {
				path = filepath.Join(a.effectiveToolBaseDir(), path)
			}
			key := path + "\x00" + expected
			verdict, cached := verdicts[key]
			if !cached {
				current, checked := hashes[path]
				if !checked {
					hash, exists, _, err := verifiedCurrentFileHash(path)
					current = currentFileRevision{hash: hash, exists: exists, valid: err == nil}
					hashes[path] = current
				}
				if !current.valid {
					// Transient verify error: leave the pair unmemoized so the
					// next pass retries instead of freezing a bad verdict.
					continue
				}
				verdict = !current.exists || current.hash != expected
				verdicts[key] = verdict
			}
			if verdict {
				if invalidated == nil {
					invalidated = make(map[int]bool)
				}
				invalidated[i] = true
				break
			}
		}
	}
	return invalidated
}

func stableReductionSurfaceNeedsReview(surface stableReductionSurface, scan *requesttrim.Scan, currentBatch uint64, externalInvalidated map[int]bool) bool {
	messages := scan.Messages()
	boundary := len(surface.Messages)
	if boundary == 0 || len(messages) < boundary {
		return true
	}
	latestBatch := uint64(0)
	for i := range boundary {
		if messages[i].Role == message.RoleAssistant && len(messages[i].ToolCalls) > 0 && messages[i].RequestBatch > 0 {
			latestBatch = messages[i].RequestBatch
		}
		if i >= len(surface.NextReviewAge) || surface.NextReviewAge[i] <= 0 || messages[i].Role != message.RoleTool || latestBatch == 0 || currentBatch <= latestBatch {
			continue
		}
		if int(currentBatch-latestBatch) >= surface.NextReviewAge[i] {
			return true
		}
	}
	if len(externalInvalidated) > 0 {
		return true
	}
	newToolResults := countToolResults(messages[boundary:])
	toolResults := surface.ToolResults + newToolResults
	if surface.ToolResults < surface.Policy.MinToolResultsPrune && toolResults >= surface.Policy.MinToolResultsPrune {
		return true
	}
	if newToolResults == 0 {
		return false
	}
	callMeta := scan.CallMeta()
	repeated := scan.RepeatedOutputs()
	validity := scan.ReadValidity()
	for i := range boundary {
		alreadyReduced := i < len(surface.ReducedIndices) && surface.ReducedIndices[i]
		// Repeated detection runs over the immutable original history, so a
		// duplicated call flags its earlier index forever. Once that index is
		// reduced to the repeated marker there is nothing left to review, and
		// treating it as reviewable would permanently disable surface reuse.
		if repeated[i] && !alreadyReduced {
			return true
		}
		if !alreadyReduced || messages[i].Role != message.RoleTool || toolname.Normalize(callMeta[messages[i].ToolCallID].Name) != tools.NameRead {
			continue
		}
		state := validity[i]
		marker := surface.Messages[i].Content
		// Validity here is computed over the ORIGINAL messages while the
		// frozen marker was built from the PREPARED view, so the two can
		// legitimately disagree on stale-vs-superseded for an already-reduced
		// read (a later edit overlaps the original range but not the kept
		// head). Either marker means the read was trimmed with guidance; the
		// still-full covering read carries the stale warning. Requiring the
		// exact class here would re-run the full scan on every request for
		// the rest of the session without changing any output.
		reducedMarked := readMarkerCarriesValidityGuidance(marker)
		if (state.Invalidated || state.Superseded) && !reducedMarked {
			return true
		}
	}
	return false
}

// readMarkerCarriesValidityGuidance reports whether a reduced read marker
// already tells the model the content is stale or superseded. Re-rendering
// such a marker rewrites bytes the provider cached without changing what the
// model can act on, and it is the shape stableReductionSurfaceNeedsReview
// treats as settled.
func readMarkerCarriesValidityGuidance(content string) bool {
	return strings.Contains(content, "truncated="+tools.ReadTruncatedStale) ||
		strings.Contains(content, "truncated="+tools.ReadTruncatedSuperseded)
}
