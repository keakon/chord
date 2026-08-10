package lsp

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

type ReviewedFileSnapshot struct {
	Path     string
	ServerID string
	Errors   int
	Warnings int
}

type reviewCounts struct {
	errors     int
	warnings   int
	reviewedAt time.Time
}

var reviewDiagLineRe = regexp.MustCompile(`^\[([EWIH])\]\s+\d+:\d+\s+`) // [E] 1:1 msg

type deleteResultGroups struct {
	Deleted []string
}

func extractReviewFilePaths(args json.RawMessage) []string {
	if path := extractReviewEditPath(args); path != "" {
		return []string{path}
	}
	var parsed struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return nil
	}
	if strings.TrimSpace(parsed.Path) != "" {
		return []string{strings.TrimSpace(parsed.Path)}
	}
	if len(parsed.Paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(parsed.Paths))
	seen := make(map[string]struct{}, len(parsed.Paths))
	for _, path := range parsed.Paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func extractReviewEditPath(args json.RawMessage) string {
	var parsed struct {
		Path  string `json:"path"`
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Path)
}

func parseDeleteReviewResult(text string) deleteResultGroups {
	var groups deleteResultGroups
	currentDeleted := false
	for rawLine := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case line == "Deleted:" || line == "Deleted (0):":
			currentDeleted = true
			continue
		case strings.HasPrefix(line, "Deleted (") && strings.HasSuffix(line, "):"):
			currentDeleted = true
			continue
		case strings.HasSuffix(line, ":"):
			currentDeleted = false
		}
		if !currentDeleted || !strings.HasPrefix(line, "- ") {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if idx := strings.Index(item, " — "); idx >= 0 {
			item = strings.TrimSpace(item[:idx])
		}
		if item != "" {
			groups.Deleted = append(groups.Deleted, item)
		}
	}
	return groups
}

func (m *Manager) normalizeReviewPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) && m != nil && m.projectRoot != "" {
		path = filepath.Join(m.projectRoot, path)
	}
	return normalizeWaiterPath(path)
}

func parseDirectFileDiagnostics(content string) map[string]reviewCounts {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return nil
	}
	var result reviewCounts
	seenAny := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, otherFilesDiagnosticsHeader) {
			break
		}
		if !reviewDiagLineRe.MatchString(line) {
			continue
		}
		seenAny = true
		switch line[1] {
		case 'E':
			result.errors++
		case 'W':
			result.warnings++
		}
	}
	if !seenAny {
		return nil
	}
	return map[string]reviewCounts{"": result}
}

func (m *Manager) CurrentReviewSnapshots(path string) []message.LSPReview {
	return m.currentReviewSnapshots(path)
}

func (m *Manager) currentReviewSnapshots(path string) []message.LSPReview {
	if m == nil {
		return nil
	}
	path = m.normalizeReviewPath(path)
	if path == "" {
		return nil
	}
	m.diagMu.RLock()
	defer m.diagMu.RUnlock()
	serverIDs := m.reviewServerIDsForPathLocked(path)
	if len(serverIDs) == 0 {
		return nil
	}
	out := make([]message.LSPReview, 0, len(serverIDs))
	for _, serverID := range serverIDs {
		counts := m.reviewCountsForPathLocked(serverID, path)
		out = append(out, message.LSPReview{Path: path, ServerID: serverID, Errors: counts.errors, Warnings: counts.warnings})
	}
	return out
}

func (m *Manager) recordReviewSnapshot(path string) {
	if m == nil {
		return
	}
	path = m.normalizeReviewPath(path)
	if path == "" {
		return
	}
	now := time.Now()
	m.diagMu.Lock()
	if m.reviewByServer == nil {
		m.reviewByServer = make(map[string]map[string]reviewCounts)
	}
	serverIDs := m.reviewServerIDsForPathLocked(path)
	for _, serverID := range serverIDs {
		total := m.reviewCountsForPathLocked(serverID, path)
		total.reviewedAt = now
		byPath := m.reviewByServer[serverID]
		if byPath == nil {
			byPath = make(map[string]reviewCounts)
			m.reviewByServer[serverID] = byPath
		}
		byPath[path] = total
	}
	m.diagMu.Unlock()
	m.notifySidebarChanged()
}

// reviewServerIDsForPathLocked returns the servers whose latest post-write review
// should be snapshotted for path. m.diagMu must be held. It intentionally includes
// servers with an existing review for path even when their current diagnostics are
// empty, so a clean follow-up edit overwrites stale non-zero sidebar counts.
func (m *Manager) reviewServerIDsForPathLocked(path string) []string {
	seen := make(map[string]struct{})
	for serverID, byURI := range m.diagByServer {
		for uri := range byURI {
			if normalizeWaiterPath(uriToPath(uri)) == path {
				seen[serverID] = struct{}{}
				break
			}
		}
	}
	for serverID, byPath := range m.reviewByServer {
		if _, ok := byPath[path]; ok {
			seen[serverID] = struct{}{}
		}
	}
	m.clientsMu.RLock()
	for serverID := range m.clients {
		if m.cfg == nil {
			seen[serverID] = struct{}{}
			continue
		}
		if srvCfg, ok := m.cfg.LSP[serverID]; ok && !srvCfg.Disabled && m.handles(srvCfg, path) {
			seen[serverID] = struct{}{}
		}
	}
	m.clientsMu.RUnlock()
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for serverID := range seen {
		out = append(out, serverID)
	}
	sort.Strings(out)
	return out
}

// reviewCountsForPathLocked returns the current diagnostics for path on serverID.
// m.diagMu must be held. Missing diagnostics deliberately mean a reviewed clean
// file, not "unknown", once reviewServerIDsForPathLocked selected the server.
func (m *Manager) reviewCountsForPathLocked(serverID, path string) reviewCounts {
	var total reviewCounts
	for uri, counts := range m.diagByServer[serverID] {
		if normalizeWaiterPath(uriToPath(uri)) != path {
			continue
		}
		total.errors += counts.errors
		total.warnings += counts.warnings
	}
	return total
}

func (m *Manager) ResetReviews() {
	if m == nil {
		return
	}
	m.diagMu.Lock()
	changed := len(m.reviewByServer) > 0
	m.reviewByServer = make(map[string]map[string]reviewCounts)
	m.diagMu.Unlock()
	if changed {
		m.notifySidebarChanged()
	}
}

func (m *Manager) ReviewedPaths() []string {
	if m == nil {
		return nil
	}
	m.diagMu.RLock()
	defer m.diagMu.RUnlock()
	seen := make(map[string]struct{})
	for _, byPath := range m.reviewByServer {
		for path := range byPath {
			seen[path] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) RebuildReviewSnapshots(items []ReviewedFileSnapshot) {
	if m == nil {
		return
	}
	rebuilt := make(map[string]map[string]reviewCounts)
	for _, item := range items {
		path := m.normalizeReviewPath(item.Path)
		serverID := strings.TrimSpace(item.ServerID)
		if path == "" || serverID == "" {
			continue
		}
		byPath := rebuilt[serverID]
		if byPath == nil {
			byPath = make(map[string]reviewCounts)
			rebuilt[serverID] = byPath
		}
		byPath[path] = reviewCounts{errors: item.Errors, warnings: item.Warnings}
	}
	m.diagMu.Lock()
	m.reviewByServer = rebuilt
	m.diagMu.Unlock()
	m.notifySidebarChanged()
}

// RebuildReviewSnapshotsFromMessages reconstructs the per-file last-review
// snapshots used by the LSP sidebar/info panel. Only directly edited files'
// own diagnostics count; diagnostics in other files from the same tool result are ignored.
func RebuildReviewSnapshotsFromMessages(msgs []message.Message) []ReviewedFileSnapshot {
	type callInfo struct {
		name  string
		paths []string
	}
	calls := make(map[string]callInfo)
	for _, msg := range msgs {
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range msg.ToolCalls {
			toolName := toolname.Normalize(tc.Name)
			if toolName != toolname.Write && toolName != toolname.Edit && toolName != toolname.ApplyPatch && toolName != toolname.Delete {
				continue
			}
			paths := extractReviewFilePaths(tc.Args)
			info := callInfo{name: toolName, paths: paths}
			calls[tc.ID] = info
		}
	}
	if len(calls) == 0 {
		return nil
	}
	byKey := make(map[string]ReviewedFileSnapshot)
	deletePaths := func(paths []string) {
		if len(paths) == 0 {
			return
		}
		for key, snap := range byKey {
			for _, path := range paths {
				if reviewPathsMatch(snap.Path, path) {
					delete(byKey, key)
					break
				}
			}
		}
	}
	storeReview := func(path string, review message.LSPReview) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		serverID := strings.TrimSpace(review.ServerID)
		if serverID == "" {
			return
		}
		key := serverID + "\x00" + path
		byKey[key] = ReviewedFileSnapshot{Path: path, ServerID: serverID, Errors: review.Errors, Warnings: review.Warnings}
	}
	for _, msg := range msgs {
		succeeded := msg.ToolStatus == message.ToolStatusSuccess ||
			(msg.ToolStatus == "" && message.ToolResultSucceeded(msg.Content))
		if msg.Role != "tool" || !succeeded {
			continue
		}
		info, ok := calls[msg.ToolCallID]
		if !ok {
			continue
		}
		var writePaths, deletedPaths []string
		if msg.FileState != nil {
			for _, write := range msg.FileState.Writes {
				writePaths = append(writePaths, write.Path)
			}
			for _, deleted := range msg.FileState.Deletes {
				deletedPaths = append(deletedPaths, deleted.Path)
			}
		}
		deletePaths(deletedPaths)
		switch info.name {
		case toolname.Delete:
			if len(deletedPaths) == 0 {
				deletePaths(parseDeleteReviewResult(msg.Content).Deleted)
			}
		case toolname.Write, toolname.Edit, toolname.ApplyPatch:
			if len(writePaths) == 0 {
				writePaths = info.paths
			}
			if len(msg.LSPReviews) > 0 {
				var pathless []message.LSPReview
				for _, review := range msg.LSPReviews {
					if strings.TrimSpace(review.Path) == "" {
						pathless = append(pathless, review)
						continue
					}
					if path, ok := canonicalReviewWritePath(review.Path, writePaths); ok {
						storeReview(path, review)
					}
				}
				// Sessions written before reviews carried their own path only have
				// unambiguous ownership for single-file tool results.
				if len(writePaths) == 1 {
					for _, review := range pathless {
						storeReview(writePaths[0], review)
					}
				}
				continue
			}
			if len(writePaths) == 1 {
				for serverID, counts := range parseDirectFileDiagnostics(msg.Content) {
					key := serverID + "\x00" + writePaths[0]
					byKey[key] = ReviewedFileSnapshot{Path: writePaths[0], ServerID: serverID, Errors: counts.errors, Warnings: counts.warnings}
				}
			}
		}
	}
	if len(byKey) == 0 {
		return nil
	}
	out := make([]ReviewedFileSnapshot, 0, len(byKey))
	for _, snap := range byKey {
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ServerID != out[j].ServerID {
			return out[i].ServerID < out[j].ServerID
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func canonicalReviewWritePath(reviewPath string, writePaths []string) (string, bool) {
	reviewPath = strings.TrimSpace(reviewPath)
	if reviewPath == "" {
		return "", false
	}
	if len(writePaths) == 0 {
		return reviewPath, true
	}
	match := ""
	for _, path := range writePaths {
		if !reviewPathsMatch(reviewPath, path) {
			continue
		}
		if match != "" && filepath.Clean(match) != filepath.Clean(path) {
			return "", false
		}
		match = path
	}
	return match, strings.TrimSpace(match) != ""
}

func reviewPathsMatch(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if a == "." || b == "." {
		return false
	}
	if a == b {
		return true
	}
	if filepath.IsAbs(a) == filepath.IsAbs(b) {
		return false
	}
	absPath, relPath := a, b
	if !filepath.IsAbs(absPath) {
		absPath, relPath = relPath, absPath
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return false
	}
	return strings.HasSuffix(absPath, string(filepath.Separator)+relPath)
}
