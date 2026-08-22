// Package sessionview projects conversation transcripts into a detached,
// memory-safe text surface. It is shared by Memory extraction and cross-session
// reference (single projection/fingerprint implementation, no duplicated
// filtering). It holds no references to source messages or attachments and
// never opens files.
package sessionview

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
)

// Kind classifies a projected item.
type Kind string

const (
	// KindUser is plain user-authored text (not a compaction summary).
	KindUser Kind = "user"
	// KindSummary is a compaction checkpoint rendered as historical context (it
	// must not be treated as the latest user command).
	KindSummary Kind = "compaction_summary"
	// KindAssistant is stable completed assistant text.
	KindAssistant Kind = "assistant"
)

// Projected is one detached text item from the transcript.
type Projected struct {
	Kind Kind
	Text string
	// Omitted is true when a single oversized item was truncated to fit the
	// budget (UTF-8-safe head/tail).
	Omitted bool
}

// assistantStableStopReasons is the whitelist of assistant stop reasons whose
// Content is considered a stable completed response. Anything else (interrupted,
// max_tokens, length, tool_calls) is excluded so partial or tool-call frames
// are never treated as durable assistant content.
var assistantStableStopReasons = map[string]bool{
	"":         true,
	"stop":     true,
	"end_turn": true,
}

// Project projects one message into a detached text surface.
//
// Projection rules (whitelist, not blacklist):
//   - user: only Kind == "" and not a compaction summary; when Parts are
//     present only explicit non-file-ref text parts are concatenated, never a
//     fallback to raw Content.
//   - compaction checkpoint: projected as KindSummary historical context.
//   - assistant: only stable completed Content (excludes interrupted /
//     max-token / tool-call frames). Provider-native thinking, reasoning, and
//     replay payload fields accompany the visible Content and never replace
//     it, so their presence alone does not drop a completed reply.
//   - tool/system/synthetic (mailbox, loop, background, permission, hook):
//     excluded.
//   - image/PDF/file-injection text and binary attachments are never read or
//     copied.
func Project(m message.Message) (Projected, bool) {
	switch m.Role {
	case message.RoleUser:
		if m.Kind != "" || m.IsCompactionSummary {
			return Projected{}, false
		}
		if len(m.Parts) > 0 {
			var sb strings.Builder
			for _, p := range m.Parts {
				if p.Type != message.ContentPartText {
					continue
				}
				if message.IsFileRefContent(p.Text) {
					continue
				}
				sb.WriteString(p.Text)
			}
			if t := strings.TrimSpace(sb.String()); t != "" {
				return Projected{Kind: KindUser, Text: t}, true
			}
			return Projected{}, false
		}
		if t := strings.TrimSpace(m.Content); t != "" {
			return Projected{Kind: KindUser, Text: t}, true
		}
		return Projected{}, false

	case message.RoleAssistant:
		if m.Kind != "" {
			return Projected{}, false
		}
		// A tool-call frame has no durable standalone reply; ThinkingBlocks,
		// ReasoningContent, and provider replay payloads (ResponsesOutput /
		// GeminiParts) accompany the visible Content and must not cause a
		// stable completed reply to be dropped.
		if len(m.ToolCalls) > 0 {
			return Projected{}, false
		}
		if !assistantStableStopReasons[m.StopReason] {
			return Projected{}, false
		}
		if t := strings.TrimSpace(m.Content); t != "" {
			return Projected{Kind: KindAssistant, Text: t}, true
		}
		return Projected{}, false

	case message.RoleTool, message.RoleSystem:
		return Projected{}, false
	default:
		return Projected{}, false
	}
}

// SummaryProject projects a compaction checkpoint. Callers must keep it as a
// KindSummary history item and never treat it as the latest user command.
func SummaryProject(m message.Message) (Projected, bool) {
	if m.Role != message.RoleUser || !m.IsCompactionSummary {
		return Projected{}, false
	}
	if t := strings.TrimSpace(m.Content); t != "" {
		return Projected{Kind: KindSummary, Text: t}, true
	}
	return Projected{}, false
}

// EstimatedTokens is a deterministic byte-based estimate (~4 bytes/token). It
// is used only for bounding; not for billing.
func EstimatedTokens(text string) int {
	return (len(text) + 3) / 4
}

// Retain selects a budget-bounded, ordered subset of projected items.
//
// The most recent user item and the most recent compaction checkpoint are
// always retained; remaining items fill from oldest to newest up to the budget.
// Truncation happens only at item boundaries; a single item that still exceeds
// the per-item budget is UTF-8-safely head/tail truncated and marked Omitted.
//
// Returns the kept items in original order plus the number of items pruned. The
// caller must compute its extraction fingerprint only over items that were kept;
// pruned history is never claimed to have been extracted.
func Retain(items []Projected, maxTokens, maxItemBytes int) (kept []Projected, pruned int) {
	if maxTokens <= 0 || len(items) == 0 {
		return nil, len(items)
	}

	// Locate the newest user item and the newest compaction checkpoint.
	newestUser := -1
	newestSummary := -1
	for i, item := range slices.Backward(items) {
		if item.Text == "" {
			continue
		}
		if newestSummary < 0 && item.Kind == KindSummary {
			newestSummary = i
		}
		if newestUser < 0 && item.Kind == KindUser {
			newestUser = i
		}
		if newestSummary >= 0 && newestUser >= 0 {
			break
		}
	}

	// Reserve budget for the two newest items, then fill the rest oldest-first.
	reserved := 0
	always := make(map[int]bool)
	for _, i := range []int{newestUser, newestSummary} {
		if i >= 0 {
			always[i] = true
			reserved += EstimatedTokens(items[i].Text)
		}
	}
	if reserved > maxTokens {
		// Even the reserved set alone exceeds the budget: keep only the newest
		// user (or summary if no user) and trim it to the item budget.
		keep := newestUser
		if keep < 0 {
			keep = newestSummary
		}
		if keep < 0 {
			return nil, len(items)
		}
		out := bounds(items[keep], maxItemBytes)
		return []Projected{out}, len(items) - 1
	}
	remaining := maxTokens - reserved

	selected := make([]bool, len(items))
	selectedCount := 0
	used := 0
	for i := range items {
		if always[i] {
			selected[i] = true
			selectedCount++
			continue
		}
		if items[i].Text == "" {
			continue
		}
		tok := EstimatedTokens(items[i].Text)
		if used+tok <= remaining || used == 0 {
			used += tok
			selected[i] = true
			selectedCount++
		}
	}

	if selectedCount == 0 {
		return nil, len(items)
	}
	kept = make([]Projected, 0, selectedCount)
	for i := range items {
		if selected[i] {
			kept = append(kept, bounds(items[i], maxItemBytes))
		} else if items[i].Text != "" {
			pruned++
		}
	}
	return kept, pruned
}

// bounds applies the per-item byte budget with UTF-8-safe head/tail truncation.
func bounds(p Projected, maxItemBytes int) Projected {
	if maxItemBytes <= 0 || len(p.Text) <= maxItemBytes {
		return p
	}
	return Projected{Kind: p.Kind, Text: truncateHeadTail(p.Text, maxItemBytes), Omitted: true}
}

// truncateHeadTail takes the head and tail of s within maxBytes, never cutting a
// rune in half, and marks the middle as elided.
func truncateHeadTail(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const marker = "\n…[truncated]…\n"
	remaining := maxBytes - len(marker)
	if remaining < 8 {
		// Not enough room for the marker: return a bounded prefix instead.
		out := s[:maxBytes]
		for len(out) > 0 && !utf8.ValidString(out) {
			out = out[:len(out)-1]
		}
		return out
	}
	headLen := remaining / 2
	head := s[:headLen]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	tail := s[len(s)-headLen:]
	for len(tail) > 0 && !utf8.ValidString(tail) {
		tail = tail[1:]
	}
	return head + marker + tail
}

// Fingerprint returns a stable content fingerprint of the projected items. Two
// identical projection sequences produce identical fingerprints; any append or
// rewrite changes it. It is computed only over items actually included in the
// extraction input.
func Fingerprint(items []Projected) string {
	h := sha256.New()
	for _, p := range items {
		h.Write([]byte(string(p.Kind)))
		h.Write([]byte{0})
		h.Write([]byte(p.Text))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
