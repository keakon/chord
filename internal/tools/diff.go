package tools

import (
	"fmt"
	"strings"
	"unicode"
)

// maxDiffOutputLines is the maximum number of diff output lines rendered.
// Diffs longer than this are truncated with a notice.
const maxDiffOutputLines = 200

// DiffSummary carries both the rendered unified diff (possibly truncated) and
// the exact total add/remove counts computed from the full edit script.
type DiffSummary struct {
	Text    string
	Added   int
	Removed int
}

// GenerateUnifiedDiff produces a unified-diff string comparing oldContent to
// newContent. filename is used only in the header lines. Returns an empty
// string when there are no differences. The edit script is computed on the
// middle region after stripping common prefix/suffix lines so typical small
// edits in large files avoid a full-file LCS. Very long diff output is
// truncated after maxDiffOutputLines with a trailing notice.
func GenerateUnifiedDiff(oldContent, newContent, filename string) string {
	return GenerateUnifiedDiffSummary(oldContent, newContent, filename).Text
}

// GenerateUnifiedDiffSummary returns the rendered unified diff plus the exact
// full add/remove counts before any maxDiffOutputLines truncation is applied.
func GenerateUnifiedDiffSummary(oldContent, newContent, filename string) DiffSummary {
	if oldContent == newContent {
		return DiffSummary{}
	}

	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	ops := lcsEditScriptWindow(oldLines, newLines, 3)
	hunks := buildHunks(ops, oldLines, newLines, 3)
	if len(hunks) == 0 {
		return DiffSummary{}
	}

	added, removed := diffOpStats(ops)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- %s\n", filename))
	sb.WriteString(fmt.Sprintf("+++ %s\n", filename))
	lineCount := 0
	truncated := false
	for _, h := range hunks {
		for _, l := range strings.Split(h, "\n") {
			if lineCount >= maxDiffOutputLines {
				truncated = true
				break
			}
			sb.WriteString(l + "\n")
			lineCount++
		}
		if truncated {
			break
		}
	}
	if truncated {
		sb.WriteString("... (diff truncated)\n")
	}
	return DiffSummary{Text: sb.String(), Added: added, Removed: removed}
}

// splitLines splits content into lines, normalising \r\n to \n first so that
// Windows line-endings do not produce spurious diffs.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// op codes for edit script.
const (
	opEqual  = 0
	opInsert = 1
	opDelete = 2
)

type editOp struct {
	kind   int
	oldIdx int // index in old (for Equal/Delete)
	newIdx int // index in new (for Equal/Insert)
}

func diffOpStats(ops []editOp) (added, removed int) {
	for _, op := range ops {
		switch op.kind {
		case opInsert:
			added++
		case opDelete:
			removed++
		}
	}
	return added, removed
}

// lcsEditScriptWindow builds an edit script by stripping common prefix and
// suffix lines, running LCS only on the middle window, then prepending and
// appending up to `context` Equal lines from the shared edges for hunk
// context. Indices in returned ops refer to the original old/new slices.
func lcsEditScriptWindow(old, new []string, context int) []editOp {
	n, m := len(old), len(new)

	// Common prefix
	prefixLen := 0
	for prefixLen < n && prefixLen < m && old[prefixLen] == new[prefixLen] {
		prefixLen++
	}

	// Common suffix (must not overlap with prefix)
	suffixLen := 0
	for suffixLen < n-prefixLen && suffixLen < m-prefixLen && old[n-1-suffixLen] == new[m-1-suffixLen] {
		suffixLen++
	}

	oldStart, oldEnd := prefixLen, n-suffixLen
	newStart, newEnd := prefixLen, m-suffixLen

	oldWindow := old[oldStart:oldEnd]
	newWindow := new[newStart:newEnd]

	ctxBefore := min(context, prefixLen)
	ctxAfter := min(context, suffixLen)

	var ops []editOp

	// Equal ops for context lines before the changed window
	for i := prefixLen - ctxBefore; i < prefixLen; i++ {
		ops = append(ops, editOp{kind: opEqual, oldIdx: i, newIdx: i})
	}

	// LCS on the changed window, with indices shifted back to original
	for _, op := range lcsEditScript(oldWindow, newWindow) {
		switch op.kind {
		case opEqual:
			op.oldIdx += oldStart
			op.newIdx += newStart
		case opDelete:
			op.oldIdx += oldStart
		case opInsert:
			op.newIdx += newStart
		}
		ops = append(ops, op)
	}

	// Equal ops for context lines after the changed window
	for i := range ctxAfter {
		ops = append(ops, editOp{kind: opEqual, oldIdx: oldEnd + i, newIdx: newEnd + i})
	}

	return ops
}

// lcsEditScript computes the edit script (sequence of Equal/Insert/Delete ops)
// between old and new using a simple LCS-based algorithm (O(N*M) time/space).
func lcsEditScript(old, new []string) []editOp {
	n := len(old)
	m := len(new)

	if n == 0 && m == 0 {
		return nil
	}
	if n == 0 {
		ops := make([]editOp, m)
		for i := range m {
			ops[i] = editOp{kind: opInsert, newIdx: i}
		}
		return ops
	}
	if m == 0 {
		ops := make([]editOp, n)
		for i := range n {
			ops[i] = editOp{kind: opDelete, oldIdx: i}
		}
		return ops
	}

	// dp[i][j] = length of LCS of old[:i] and new[:j]
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if old[i-1] == new[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack to produce edit ops.
	var ops []editOp
	i, j := n, m
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && old[i-1] == new[j-1] {
			ops = append(ops, editOp{kind: opEqual, oldIdx: i - 1, newIdx: j - 1})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			ops = append(ops, editOp{kind: opInsert, newIdx: j - 1})
			j--
		} else {
			ops = append(ops, editOp{kind: opDelete, oldIdx: i - 1})
			i--
		}
	}

	// Reverse (backtrack produces ops in reverse order).
	for lo, hi := 0, len(ops)-1; lo < hi; lo, hi = lo+1, hi-1 {
		ops[lo], ops[hi] = ops[hi], ops[lo]
	}
	return ops
}

type hunkRange struct {
	start, end int
}

// buildHunks groups edit ops into unified-diff hunk strings with ctx context
// lines. It independently tracks old/new line counters so @@ headers are
// always correct.
func buildHunks(ops []editOp, old, new []string, ctx int) []string {
	if len(ops) == 0 {
		return nil
	}

	// Collect indices of non-Equal ops and merge nearby ones.
	var ranges []hunkRange
	for i, op := range ops {
		if op.kind != opEqual {
			if len(ranges) == 0 || i > ranges[len(ranges)-1].end+1 {
				ranges = append(ranges, hunkRange{i, i})
			} else {
				ranges[len(ranges)-1].end = i
			}
		}
	}

	// Merge ranges whose expanded contexts would overlap (gap ≤ 2*ctx).
	if ctx > 0 && len(ranges) > 1 {
		merged := []hunkRange{ranges[0]}
		for _, r := range ranges[1:] {
			last := &merged[len(merged)-1]
			if r.start <= last.end+2*ctx+1 {
				last.end = r.end
			} else {
				merged = append(merged, r)
			}
		}
		ranges = merged
	}

	var hunks []string
	for _, r := range ranges {
		start := r.start - ctx
		if start < 0 {
			start = 0
		}
		end := r.end + ctx
		if end >= len(ops) {
			end = len(ops) - 1
		}

		slice := ops[start : end+1]

		// Compute correct 1-based old/new start line numbers and counts.
		oldStart, newStart := -1, -1
		oldCount, newCount := 0, 0
		for _, op := range slice {
			switch op.kind {
			case opEqual:
				if oldStart < 0 {
					oldStart = op.oldIdx + 1
				}
				if newStart < 0 {
					newStart = op.newIdx + 1
				}
				oldCount++
				newCount++
			case opDelete:
				if oldStart < 0 {
					oldStart = op.oldIdx + 1
				}
				oldCount++
			case opInsert:
				if newStart < 0 {
					newStart = op.newIdx + 1
				}
				newCount++
			}
		}
		// Anchor the side that had no lines of its own against the first Equal.
		if oldStart < 0 {
			for _, op := range slice {
				if op.kind == opEqual {
					oldStart = op.oldIdx + 1
					break
				}
			}
			if oldStart < 0 {
				oldStart = 1
			}
		}
		if newStart < 0 {
			for _, op := range slice {
				if op.kind == opEqual {
					newStart = op.newIdx + 1
					break
				}
			}
			if newStart < 0 {
				newStart = 1
			}
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount))
		for _, op := range slice {
			switch op.kind {
			case opEqual:
				sb.WriteString(" " + old[op.oldIdx] + "\n")
			case opDelete:
				sb.WriteString("-" + old[op.oldIdx] + "\n")
			case opInsert:
				sb.WriteString("+" + new[op.newIdx] + "\n")
			}
		}
		hunks = append(hunks, sb.String())
	}
	return hunks
}

// InlineSegment is one segment of an inline (character-level) diff.
// Kind is "equal", "delete", or "insert".
type InlineSegment struct {
	Text string
	Kind string
}

type tokenSegment struct {
	Text string
	Kind string
}

func isWordTokenRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func tokenizeInlineDiffLine(s string) []tokenSegment {
	if s == "" {
		return nil
	}
	var tokens []tokenSegment
	var cur strings.Builder
	curKind := ""
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		tokens = append(tokens, tokenSegment{Text: cur.String(), Kind: curKind})
		cur.Reset()
	}
	for _, r := range s {
		kind := "punct"
		switch {
		case isWordTokenRune(r):
			kind = "word"
		case unicode.IsSpace(r):
			kind = "space"
		}
		if curKind != "" && curKind != kind {
			flush()
		}
		curKind = kind
		cur.WriteRune(r)
	}
	flush()
	return tokens
}

func refineTokenSegments(oldText, newText string) (oldSegs, newSegs []InlineSegment) {
	oldR := []rune(oldText)
	newR := []rune(newText)
	if len(oldR) == 0 && len(newR) == 0 {
		return nil, nil
	}
	oldStrs := make([]string, len(oldR))
	for i, r := range oldR {
		oldStrs[i] = string(r)
	}
	newStrs := make([]string, len(newR))
	for i, r := range newR {
		newStrs[i] = string(r)
	}
	ops := lcsEditScript(oldStrs, newStrs)
	for _, op := range ops {
		switch op.kind {
		case opEqual:
			oldSegs = appendMergedSegment(oldSegs, "equal", oldStrs[op.oldIdx])
			newSegs = appendMergedSegment(newSegs, "equal", newStrs[op.newIdx])
		case opDelete:
			oldSegs = appendMergedSegment(oldSegs, "delete", oldStrs[op.oldIdx])
		case opInsert:
			newSegs = appendMergedSegment(newSegs, "insert", newStrs[op.newIdx])
		}
	}
	return oldSegs, newSegs
}

func appendMergedSegment(segs []InlineSegment, kind, text string) []InlineSegment {
	if text == "" {
		return segs
	}
	if len(segs) > 0 && segs[len(segs)-1].Kind == kind {
		segs[len(segs)-1].Text += text
		return segs
	}
	return append(segs, InlineSegment{Text: text, Kind: kind})
}

func tokenAwareInlineDiff(old, new string) (oldSegs, newSegs []InlineSegment) {
	oldTokens := tokenizeInlineDiffLine(old)
	newTokens := tokenizeInlineDiffLine(new)
	if len(oldTokens) == 0 && len(newTokens) == 0 {
		return nil, nil
	}
	oldSeq := make([]string, len(oldTokens))
	for i, tok := range oldTokens {
		oldSeq[i] = tok.Kind + "\x00" + tok.Text
	}
	newSeq := make([]string, len(newTokens))
	for i, tok := range newTokens {
		newSeq[i] = tok.Kind + "\x00" + tok.Text
	}
	ops := lcsEditScript(oldSeq, newSeq)
	for i := 0; i < len(ops); i++ {
		op := ops[i]
		switch op.kind {
		case opEqual:
			text := oldTokens[op.oldIdx].Text
			oldSegs = appendMergedSegment(oldSegs, "equal", text)
			newSegs = appendMergedSegment(newSegs, "equal", text)
		case opDelete:
			if i+1 < len(ops) && ops[i+1].kind == opInsert {
				oldTok := oldTokens[op.oldIdx]
				newTok := newTokens[ops[i+1].newIdx]
				if oldTok.Kind == newTok.Kind {
					refOld, refNew := refineTokenSegments(oldTok.Text, newTok.Text)
					for _, seg := range refOld {
						oldSegs = appendMergedSegment(oldSegs, seg.Kind, seg.Text)
					}
					for _, seg := range refNew {
						newSegs = appendMergedSegment(newSegs, seg.Kind, seg.Text)
					}
					i++
					continue
				}
			}
			oldSegs = appendMergedSegment(oldSegs, "delete", oldTokens[op.oldIdx].Text)
		case opInsert:
			newSegs = appendMergedSegment(newSegs, "insert", newTokens[op.newIdx].Text)
		}
	}
	return oldSegs, newSegs
}

// InlineDiff computes a token-aware diff between old and new and returns
// segments for the old line (equal + delete) and new line (equal + insert).
// It aligns on coarse code-friendly tokens first, then refines matching changed
// tokens with rune-level diff so the TUI gets stable structure plus precise
// intra-token highlighting.
func InlineDiff(old, new string) (oldSegs, newSegs []InlineSegment) {
	return tokenAwareInlineDiff(old, new)
}
