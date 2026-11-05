package sessionimport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// codexPage is one rollout file participating in a stitched import. Pages
// are ordered oldest base first, selected page last. End freezes the inherited
// prefix while the parent continues writing; nil marks the selected page.
type codexPage struct {
	Path string
	End  *codexHistoryBase
}

// codexHistoryBase is the subset of SessionMeta.history_base the importer
// follows. The thread_id field predates thread/revert and carries the base
// page's rollout id, despite its name.
type codexHistoryBase struct {
	ThreadID            string `json:"thread_id"`
	EndOrdinalExclusive uint64 `json:"end_ordinal_exclusive"`
	EndByteOffset       uint64 `json:"end_byte_offset"`
}

// resolveCodexPages walks the history_base chain behind the selected rollout
// page and returns the pages oldest first, selected page last. The walk has
// no depth limit — real chains cross thread ids through referenced sub-agent
// spawns and reach dozens of pages — and terminates on the visited set alone.
// A chain that cannot be followed completely — a missing or compressed base
// page, a cycle — is rejected outright: importing the selected page alone
// would silently drop the base history.
func resolveCodexPages(selectedPath string, roots []string) ([]codexPage, error) {
	if strings.HasSuffix(selectedPath, ".jsonl.zst") {
		return nil, fmt.Errorf("codex import: rollout %s is compressed; compressed rollouts are not supported", selectedPath)
	}
	chain := []codexPage{{Path: selectedPath}}
	visited := map[string]bool{codexPageChainKey(selectedPath): true}
	current := selectedPath
	for {
		base, err := readCodexHistoryBase(current)
		if err != nil {
			return nil, err
		}
		if base == nil {
			break
		}
		match, found := findCodexRolloutByRolloutID(roots, base.ThreadID)
		if !found {
			return nil, fmt.Errorf("codex import: history base rollout %q not found under %s; followed %d page(s) from %s", base.ThreadID, codexRootsDisplay(roots), len(chain), selectedPath)
		}
		if match.Compressed {
			return nil, fmt.Errorf("codex import: history base rollout %q is compressed (%s); compressed rollouts are not supported", base.ThreadID, match.Path)
		}
		key := codexPageChainKey(match.Path)
		if visited[key] {
			return nil, fmt.Errorf("codex import: history_base chain revisits rollout %s after %d page(s); refusing to import a partial history", match.Path, len(chain))
		}
		visited[key] = true
		chain = append(chain, codexPage{Path: match.Path, End: base})
		current = match.Path
	}
	slices.Reverse(chain)
	return chain, nil
}

// codexPageChainKey identifies a page within one chain walk: the rollout id
// encoded in the file name when it parses, otherwise the path itself.
func codexPageChainKey(path string) string {
	if parsed, ok := parseCodexRolloutFileName(filepath.Base(path)); ok {
		return parsed.RolloutID
	}
	return path
}

func codexRootsDisplay(roots []string) string {
	return strings.Join(roots, ", ")
}

func readCodexPage(page codexPage) ([]byte, error) {
	if page.End == nil {
		return os.ReadFile(page.Path)
	}
	f, err := os.Open(page.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	end := page.End.EndByteOffset
	if end == 0 || end > uint64(info.Size()) {
		return nil, fmt.Errorf("history prefix is truncated: expected %d bytes, file has %d", end, info.Size())
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(end)))
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) != end {
		return nil, fmt.Errorf("history prefix is truncated: expected %d bytes, read %d", end, len(data))
	}
	if data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("history byte boundary %d does not end a JSONL record", end)
	}
	return data, nil
}

// readCodexHistoryBase reads the page's first readable session_meta and
// returns its history_base, or nil when the page declares none. Codex writes
// the session meta as the first record. An invalid reference must not be
// mistaken for a standalone page, which would silently discard its ancestors.
func readCodexHistoryBase(path string) (*codexHistoryBase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("codex import: read rollout page %s: %w", path, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var lineObj map[string]json.RawMessage
		if err := importJSONUnmarshalString(line, &lineObj); err != nil {
			continue
		}
		rawType, ok := lineObj["type"]
		if !ok {
			continue
		}
		var typeStr string
		if err := importJSONUnmarshal(rawType, &typeStr); err != nil || typeStr != codexTopLevelTypeSessionMeta {
			continue
		}
		payload := lineObj["payload"]
		if len(payload) == 0 {
			continue
		}
		var meta struct {
			HistoryBase *codexHistoryBase `json:"history_base"`
		}
		if err := importJSONUnmarshal(payload, &meta); err != nil {
			return nil, fmt.Errorf("codex import: parse session metadata in %s: %w", path, err)
		}
		if meta.HistoryBase == nil {
			return nil, nil
		}
		if !isCodexUUID(meta.HistoryBase.ThreadID) || meta.HistoryBase.EndOrdinalExclusive == 0 || meta.HistoryBase.EndByteOffset == 0 {
			return nil, fmt.Errorf("codex import: invalid history_base in %s: rollout id and positive ordinal and byte boundaries are required", path)
		}
		return meta.HistoryBase, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("codex import: scan rollout page %s: %w", path, err)
	}
	return nil, nil
}
