package sessionimport

import (
	"bytes"
	"fmt"

	"github.com/keakon/chord/internal/message"
)

// convertCodexRollout converts ordered Codex rollout pages (typically under
// ~/.codex/sessions/**/rollout-*.jsonl, oldest history_base page first and
// the selected page last) into a Chord main transcript. Pages carrying a
// history_base bound are truncated at it, so a forked thread imports the
// full history the source replayed rather than the newest page alone.
//
// The conversion pipeline:
//  1. Read and parse each page's JSONL into typed rollout entries
//  2. Build intermediate representation (IR) with turn reconstruction
//     and source-precedence decisions
//  3. Linearize IR into Chord messages with structured tool imports where
//     safe, source tool calls where no mapping exists, and readable fallback
//     blocks for records that cannot be paired
func convertCodexRollout(pages []codexPage, reasoningMode string, report *ImportReport) ([]message.Message, error) {
	if len(pages) == 0 {
		return nil, errEmptyInput
	}

	// Stage 1: Parse each page's JSONL, oldest page first. Ordinals are
	// contiguous across the chain, so page order is the record order; no
	// re-sorting is needed.
	var entries []codexRolloutEntry
	sessionID := ""
	var firstOrdinal *uint64
	if len(pages) > 1 {
		firstOrdinal = new(uint64(0))
	}
	baseEntries := 0
	pagePaths := make([]string, 0, len(pages))
	for i, page := range pages {
		data, err := readCodexPage(page)
		if err != nil {
			return nil, fmt.Errorf("codex import: read rollout page %s: %w", page.Path, err)
		}
		if i == len(pages)-1 && len(bytes.TrimSpace(data)) == 0 {
			return nil, errEmptyInput
		}
		var endOrdinal uint64
		if page.End != nil {
			endOrdinal = page.End.EndOrdinalExclusive
		}
		pageEntries, pageSessionID, err := parseCodexJSONL(data, firstOrdinal, endOrdinal)
		if err != nil {
			return nil, fmt.Errorf("codex import: parse rollout page %s: %w", page.Path, err)
		}
		if page.End != nil {
			firstOrdinal = new(endOrdinal)
		}
		if i == len(pages)-1 {
			// Ancestors can belong to other threads, so their identity must
			// never substitute for missing metadata in the selected page.
			sessionID = pageSessionID
		} else {
			baseEntries += len(pageEntries)
		}
		entries = append(entries, pageEntries...)
		pagePaths = append(pagePaths, page.Path)
	}
	if sessionID != "" {
		report.SourceSessionID = sessionID
	}
	report.Codex = &CodexImportReport{
		HistoryPagesStitched: len(pages),
		HistoryItemsFromBase: baseEntries,
		Pages:                pagePaths,
	}

	// Stage 2: Build IR with turn reconstruction.
	turns, err := buildCodexIR(entries, reasoningMode, report)
	if err != nil {
		return nil, err
	}

	// Stage 3: Linearize IR into Chord messages.
	msgs := linearizeCodexTurns(turns, report)
	if err := validateImportedCodexMessages(msgs, report); err != nil {
		return nil, err
	}

	report.ImportedMessages = len(msgs)
	return msgs, nil
}

// convertCodexRolloutFile resolves the history_base chain behind the selected
// rollout page and converts the stitched pages. A chain that cannot be
// followed completely is rejected instead of importing a partial history.
func convertCodexRolloutFile(selectedPath string, roots []string, reasoningMode string, report *ImportReport) ([]message.Message, error) {
	pages, err := resolveCodexPages(selectedPath, roots)
	if err != nil {
		return nil, err
	}
	return convertCodexRollout(pages, reasoningMode, report)
}
