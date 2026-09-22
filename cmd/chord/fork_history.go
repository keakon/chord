package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/recovery"
)

// forkHistoryFlagHelp is the shared --fork-history help text used by both the
// `chord resume` subcommand and the root `chord --resume` form.
const forkHistoryFlagHelp = "Fork the session at an applied compaction boundary and resume the fork instead: pass the boundary number (e.g. =2 for history-2) or omit it for the latest applied boundary. The fork's main.jsonl is created from that boundary's pre-compaction main.pre-compress-N.jsonl record for record — the checkpoint summary card included — with message content preserved unchanged except that image/PDF attachments are relocated into the fork and the checkpoint's archived-history paths are repointed at the fork's own copies of the history-*.md archives. Usage and runtime state start fresh, and sub-agent/task state is not carried over. It works while the source session is open in another process."

// compactionHistoryStatusApplied mirrors the agent-side compaction lifecycle
// constant (compactionHistoryApplied in internal/agent/compaction.go): the
// boundary scan below must agree with that lifecycle, which flips
// history-N.status.json from "pending_apply" to "applied" only after the
// pre-compress rename and the main.jsonl rewrite both completed.
const compactionHistoryStatusApplied = "applied"

// forkCompactionHistoryStatusPath returns the path of the agent-written status
// record that accompanies history-N.md and its main.pre-compress-N.jsonl
// snapshot (the agent derives it from the archive path the same way).
func forkCompactionHistoryStatusPath(sessionDir string, index int) string {
	return filepath.Join(sessionDir, fmt.Sprintf("history-%d.status.json", index))
}

// forkCompactionBoundaryApplied reports whether compaction boundary N was fully
// applied. A missing status file means the apply never completed (the status
// record exists before the pre-compress rename and is only flipped to
// "applied" after the main.jsonl rewrite), so such a snapshot is not forkable.
func forkCompactionBoundaryApplied(sessionDir string, index int) (bool, error) {
	data, err := os.ReadFile(forkCompactionHistoryStatusPath(sessionDir, index))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	var meta struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false, fmt.Errorf("parse compaction status: %w", err)
	}
	return meta.Status == compactionHistoryStatusApplied, nil
}

// forkHistoryBoundary describes one applied compaction generation of a
// session: boundary N corresponds to main.pre-compress-N.jsonl — the session's
// complete pre-compaction transcript, archived verbatim when compaction N
// applied (see compaction_persistence.go).
type forkHistoryBoundary struct {
	index           int
	preCompressPath string
}

// scanForkHistoryBoundaries lists the applied compaction boundaries of a
// session by matching its main.pre-compress-N.jsonl snapshots (produced by
// the agent's rewriteSessionAfterCompaction rename) against the corresponding
// history-N.status.json record. A snapshot only counts as a boundary when that
// record reports status "applied": a snapshot left behind by a crash between
// the rename and the applied-status write is not offered for forking (the
// agent's cleanupStalePendingCompactions deliberately keeps a pending status
// whose backup file exists, in case the apply is still in progress or failed,
// but that apply has not completed either way, and the flag help text promises
// the latest *applied* boundary).
func scanForkHistoryBoundaries(sessionDir string) ([]forkHistoryBoundary, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, err
	}
	var out []forkHistoryBoundary
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "main.pre-compress-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "main.pre-compress-"), ".jsonl"))
		if err != nil || n < 1 {
			continue
		}
		applied, err := forkCompactionBoundaryApplied(sessionDir, n)
		if err != nil {
			log.Warnf("fork session: unreadable compaction status for boundary history-%d path=%v error=%v", n, forkCompactionHistoryStatusPath(sessionDir, n), err)
			continue
		}
		if !applied {
			continue
		}
		out = append(out, forkHistoryBoundary{index: n, preCompressPath: filepath.Join(sessionDir, name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].index < out[j].index })
	return out, nil
}

// forkBoundaryFromFlag normalizes the --fork-history value: an empty value,
// "latest", or "auto" selects the most recent applied boundary; a positive
// integer selects that boundary explicitly.
func forkBoundaryFromFlag(value string) (int, error) {
	switch v := strings.ToLower(strings.TrimSpace(value)); v {
	case "", "latest", "auto":
		return 0, nil
	default:
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("invalid compaction boundary %q: pass a positive history number (e.g. --fork-history=2) or omit the value for the latest boundary", value)
		}
		return n, nil
	}
}

// forkBoundaryList renders the available boundaries for error messages.
func forkBoundaryList(boundaries []forkHistoryBoundary) string {
	if len(boundaries) == 0 {
		return "no applied compaction boundaries"
	}
	indexes := make([]string, 0, len(boundaries))
	for _, b := range boundaries {
		indexes = append(indexes, fmt.Sprintf("history-%d", b.index))
	}
	return "available: " + strings.Join(indexes, ", ")
}

// readJSONLLines returns the non-empty lines of a JSONL file with each line's
// trailing line ending stripped. Line content is preserved byte-for-byte apart
// from that ending, so archived records can be copied into a fork unchanged.
func readJSONLLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	reader := bufio.NewReader(f)
	for {
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 {
			line := raw
			if n := len(line); n > 0 && line[n-1] == '\n' {
				line = line[:n-1]
			}
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}
			if strings.TrimSpace(string(line)) != "" {
				lines = append(lines, string(line))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	return lines, nil
}

// copyForkArchives copies the source session's history-*.md compaction
// archives with index < boundary into newDir. The generation being forked
// leads with the previous checkpoint's summary card, whose history map points
// at those archives (e.g. fork 2 = pre-compress-2.jsonl whose summary card
// references history-1.md); copying them and repointing the map's paths at the
// fork (relocForkCheckpointPaths) keeps the map resolvable inside the fork so
// the model can read the older content on demand. The boundary's own archive
// (history-N.md) is deliberately not copied: pre-compress-N.jsonl still
// carries the messages that archive would contain — compaction N never applied
// to this generation — so copying it would duplicate content that is already
// inline in the fork's transcript. Only the .md archives are copied — their
// .status.json provenance stays with the source session, whose generation ids
// it references.
func copyForkArchives(srcDir, newDir string, boundary int) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "history-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "history-"), ".md"))
		if err != nil || n < 1 || n >= boundary {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			return fmt.Errorf("read archive %s: %w", name, err)
		}
		if err := privatefs.WriteFile(newDir, filepath.Join(newDir, name), data); err != nil {
			return fmt.Errorf("copy archive %s: %w", name, err)
		}
	}
	return nil
}

// forkSessionAtHistory creates a new session under projectSessionsDir that is
// a record-for-record copy of the source session at srcDir as of an applied
// compaction boundary boundaryIndex (<= 0 selects the latest applied
// boundary): the records of pre-compress-N.jsonl become the fork's main.jsonl
// — including the leading [Context Summary] checkpoint card, which was part of
// that generation's real state — with message content preserved unchanged
// except that binary-attachment records are relocated and the checkpoint
// card's archived-history paths are repointed so the fork owns its bytes and
// its archive references. The session's earlier history-1..N-1.md archives are
// copied alongside so the checkpoint's history map stays resolvable; the
// boundary's own archive (history-N.md) is not — its content is still inline
// in the forked transcript. Source session files are only read, so the
// source may be locked by a running process. The fork directory is written
// under its own session lock, and main.jsonl is published last through an
// atomic rename, so a crash mid-fork leaves only an incomplete directory that
// session listings ignore instead of a partial session. It returns the new
// session directory, the boundary actually used, and the number of seeded
// records.
func forkSessionAtHistory(srcDir, projectSessionsDir string, boundaryIndex int) (newDir string, chosen int, seeded int, err error) {
	boundaries, err := scanForkHistoryBoundaries(srcDir)
	if err != nil {
		return "", 0, 0, fmt.Errorf("scan compaction history of session %s: %w", filepath.Base(srcDir), err)
	}
	if len(boundaries) == 0 {
		return "", 0, 0, fmt.Errorf("session %s has no applied compaction history (no main.pre-compress-N.jsonl snapshot with an applied history-N.status.json record); resume it directly with chord resume %s", filepath.Base(srcDir), filepath.Base(srcDir))
	}
	chosen = boundaryIndex
	if chosen <= 0 {
		chosen = boundaries[len(boundaries)-1].index
	}
	found := false
	for _, b := range boundaries {
		if b.index == chosen {
			found = true
			break
		}
	}
	if !found {
		return "", 0, 0, fmt.Errorf("applied boundary history-%d does not exist for session %s: %s", chosen, filepath.Base(srcDir), forkBoundaryList(boundaries))
	}

	srcPath := filepath.Join(srcDir, fmt.Sprintf("main.pre-compress-%d.jsonl", chosen))
	lines, err := readJSONLLines(srcPath)
	if err != nil {
		return "", 0, 0, fmt.Errorf("read %s: %w", filepath.Base(srcPath), err)
	}
	if len(lines) == 0 {
		return "", 0, 0, fmt.Errorf("session %s has an empty pre-compaction record at boundary history-%d", filepath.Base(srcDir), chosen)
	}

	newDir, err = recovery.CreateNewSessionDir(projectSessionsDir)
	if err != nil {
		return "", 0, 0, fmt.Errorf("create fork session directory: %w", err)
	}
	// The fork directory is written under its own exclusive session lock, and
	// main.jsonl is published last through an atomic rename (see
	// writeForkTranscript), so a crash mid-fork leaves only an incomplete
	// directory that session listings ignore instead of a partial session.
	lock, err := recovery.AcquireSessionLock(newDir)
	if err != nil {
		_ = os.RemoveAll(newDir)
		return "", 0, 0, fmt.Errorf("lock fork session directory: %w", err)
	}
	fail := func(stepErr error) (string, int, int, error) {
		if releaseErr := lock.Release(); releaseErr != nil {
			log.Warnf("fork session: release lock on %s after failure error=%v", newDir, releaseErr)
		}
		_ = os.RemoveAll(newDir)
		return "", 0, 0, stepErr
	}

	meta, err := forkSessionMeta(srcDir)
	if err != nil {
		return fail(err)
	}
	if err := recovery.SaveSessionMeta(newDir, meta); err != nil {
		return fail(fmt.Errorf("save fork session meta: %w", err))
	}
	if err := copyForkArchives(srcDir, newDir, chosen); err != nil {
		return fail(fmt.Errorf("copy compaction archives into fork: %w", err))
	}
	if seeded, err = writeForkTranscript(newDir, srcDir, lines); err != nil {
		return fail(err)
	}
	mainTmp := filepath.Join(newDir, identity.MainSessionLogFilename+".tmp")
	mainPath := filepath.Join(newDir, identity.MainSessionLogFilename)
	if err := os.Rename(mainTmp, mainPath); err != nil {
		return fail(fmt.Errorf("publish fork transcript: %w", err))
	}
	if err := privatefs.SyncDir(newDir); err != nil {
		log.Warnf("fork session: sync fork directory %s error=%v", newDir, err)
	}
	if err := lock.Release(); err != nil {
		log.Warnf("fork session: release lock on %s error=%v", newDir, err)
	}
	return newDir, chosen, seeded, nil
}

// forkMessageNeedsRelocation reports whether a persisted record carries binary
// parts whose bytes must land in the fork's own images/ directory. Records
// without such parts are copied into the fork byte-for-byte.
func forkMessageNeedsRelocation(msg message.Message) bool {
	for _, p := range msg.Parts {
		if p.IsBinary() && (p.ImagePath != "" || len(p.Data) > 0) {
			return true
		}
	}
	return false
}

// forkAttachmentExtension mirrors the extension selection the recovery manager
// applies when persisting binary parts (see RecoveryManager.persistBinaryParts).
func forkAttachmentExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	default:
		return ".bin"
	}
}

// relocForkAttachmentParts stages a record's binary parts into newDir's
// images/ directory and rewrites their ImagePath references, mirroring how the
// recovery manager's PersistMessage persists live messages: inline bytes are
// written out to a file, and ImagePath references are re-read from the source
// session and copied over so the fork does not depend on the source surviving.
// Message text is never touched. An attachment the source session can no
// longer provide is a hard error: silently forking a record whose image would
// be missing on restore is what this relocation exists to prevent.
func relocForkAttachmentParts(msg message.Message, srcDir, newDir string) (message.Message, error) {
	parts := make([]message.ContentPart, len(msg.Parts))
	copy(parts, msg.Parts)
	for i := range parts {
		p := &parts[i]
		if !p.IsBinary() {
			continue
		}
		if p.ImagePath == "" && len(p.Data) == 0 {
			continue
		}
		data := p.Data
		if p.ImagePath != "" && len(data) == 0 {
			srcPath := p.ImagePath
			if !filepath.IsAbs(srcPath) {
				srcPath = filepath.Join(srcDir, srcPath)
			}
			readData, readErr := os.ReadFile(srcPath)
			if readErr != nil {
				return msg, fmt.Errorf("fork session: source attachment %s is no longer readable and the boundary cannot be reproduced without it: %w", srcPath, readErr)
			}
			data = readData
		}
		fileName := fmt.Sprintf("%d-%d%s", time.Now().UnixNano(), i, forkAttachmentExtension(p.MimeType))
		imgPath := filepath.Join(newDir, "images", fileName)
		if err := privatefs.WriteFile(newDir, imgPath, data); err != nil {
			return msg, fmt.Errorf("fork session: write attachment %s: %w", imgPath, err)
		}
		p.Data = nil
		p.ImagePath = imgPath
	}
	msg.Parts = parts
	return msg, nil
}

// forkMessageNeedsRewrite reports whether a record must be re-encoded for the
// fork instead of being copied byte-for-byte. Binary attachments need their
// bytes relocated into the fork's images/ directory, and a compaction
// checkpoint's archived-history map references the source session's directory
// by its abbreviated path — that prefix must be repointed at the fork's own
// copy so the fork is self-contained. The archives are copied alongside by
// copyForkArchives, but the map's paths still point at the source session
// until rewritten here.
func forkMessageNeedsRewrite(msg message.Message, srcDir string) bool {
	if forkMessageNeedsRelocation(msg) {
		return true
	}
	if !msg.IsCompactionSummary {
		return false
	}
	return checkpointHistoryMapHasSourcePath(msg.Content, pathutil.AbbreviateHome(srcDir))
}

const checkpointHistoryMapHeading = "Archived history files ("

func checkpointHistoryMapHasSourcePath(content, srcPrefix string) bool {
	if srcPrefix == "" {
		return false
	}
	lines := strings.Split(content, "\n")
	inHistoryMap := false
	for _, line := range lines {
		if strings.HasPrefix(line, checkpointHistoryMapHeading) {
			inHistoryMap = true
			continue
		}
		if !inHistoryMap {
			continue
		}
		if after, ok := strings.CutPrefix(line, "- "); ok {
			ref := strings.TrimSpace(after)
			if checkpointHistoryMapSourcePath(ref, srcPrefix) {
				return true
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "Each archive begins") {
			break
		}
	}
	return false
}

func checkpointHistoryMapSourcePath(ref, srcPrefix string) bool {
	if separator := strings.Index(ref, ": "); separator >= 0 {
		ref = ref[:separator]
	}
	if !strings.HasPrefix(ref, srcPrefix+"/history-") {
		return false
	}
	rest := strings.TrimPrefix(ref, srcPrefix+"/history-")
	if len(rest) < len(".md") || !strings.HasSuffix(rest, ".md") {
		return false
	}
	index := strings.TrimSuffix(rest, ".md")
	if index == "" {
		return false
	}
	for _, r := range index {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// relocForkCheckpointPaths repoints a compaction checkpoint's archived-history
// references from the source session's directory to the fork's own copy. The
// compaction pipeline writes the map as AbbreviateHome(srcDir/history-N.md), so
// the session directory itself surfaces as AbbreviateHome(srcDir); rewriting
// those exact history-map path lines to the fork prefix makes the fork
// self-contained — reading the map's paths lands in the fork's session
// directory whether or not the source survives. This mirrors
// relocForkAttachmentParts: both relocate references the fork must own instead
// of depending on the source session. Text outside the history map is left
// unchanged.
func relocForkCheckpointPaths(msg message.Message, srcDir, newDir string) message.Message {
	if !msg.IsCompactionSummary {
		return msg
	}
	srcPrefix := pathutil.AbbreviateHome(srcDir)
	dstPrefix := pathutil.AbbreviateHome(newDir)
	if srcPrefix == "" || srcPrefix == dstPrefix {
		return msg
	}
	lines := strings.Split(msg.Content, "\n")
	inHistoryMap := false
	for i, line := range lines {
		if strings.HasPrefix(line, checkpointHistoryMapHeading) {
			inHistoryMap = true
			continue
		}
		if !inHistoryMap {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			prefix := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			ref := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if checkpointHistoryMapSourcePath(ref, srcPrefix) {
				lines[i] = prefix + "- " + dstPrefix + strings.TrimPrefix(ref, srcPrefix)
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "Each archive begins") {
			break
		}
	}
	msg.Content = strings.Join(lines, "\n")
	return msg
}

// writeForkTranscript streams the archived records of the chosen boundary into
// a temporary main.jsonl inside newDir. Records without binary parts and
// without source-session path references are copied byte-for-byte; records
// that need relocation are re-encoded by relocForkAttachmentParts (binary
// attachments) and relocForkCheckpointPaths (a checkpoint's history map). The
// per-record differences from the archived bytes are the relocated image_path
// and the repointed history-map paths. The caller renames the temp file into
// place once the whole transcript — and the rest of the fork — is staged, so
// main.jsonl appears complete or not at all.
func writeForkTranscript(newDir, srcDir string, lines []string) (int, error) {
	tmpPath := filepath.Join(newDir, identity.MainSessionLogFilename+".tmp")
	f, err := privatefs.OpenFile(newDir, tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return 0, fmt.Errorf("create fork transcript: %w", err)
	}
	fail := func(writeErr error) (int, error) {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return 0, writeErr
	}
	w := bufio.NewWriter(f)
	seeded := 0
	for _, line := range lines {
		var msg message.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return fail(fmt.Errorf("decode archived message: %w", err))
		}
		var record []byte
		if !forkMessageNeedsRewrite(msg, srcDir) {
			record = make([]byte, 0, len(line)+1)
			record = append(record, line...)
			record = append(record, '\n')
		} else {
			relocated := msg
			if forkMessageNeedsRelocation(relocated) {
				var relocErr error
				relocated, relocErr = relocForkAttachmentParts(relocated, srcDir, newDir)
				if relocErr != nil {
					return fail(relocErr)
				}
			}
			relocated = relocForkCheckpointPaths(relocated, srcDir, newDir)
			record, err = json.Marshal(relocated)
			if err != nil {
				return fail(fmt.Errorf("encode fork message: %w", err))
			}
			record = append(record, '\n')
		}
		if _, err := w.Write(record); err != nil {
			return fail(fmt.Errorf("write fork transcript: %w", err))
		}
		seeded++
	}
	if err := w.Flush(); err != nil {
		return fail(fmt.Errorf("flush fork transcript: %w", err))
	}
	if err := f.Sync(); err != nil {
		return fail(fmt.Errorf("sync fork transcript: %w", err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("close fork transcript: %w", err)
	}
	return seeded, nil
}

// forkSessionMeta builds the session metadata of a fork. The fork records the
// source session in ForkedFrom, keeps the source's worktree provenance (it
// runs in the same project/worktree) and manual-MCP intent, and deliberately
// drops session-scoped state that cannot be carried over truthfully — usage,
// title, and import provenance.
func forkSessionMeta(srcDir string) (recovery.SessionMeta, error) {
	meta := recovery.SessionMeta{ForkedFrom: filepath.Base(srcDir)}
	src, err := recovery.LoadSessionMeta(srcDir)
	if err != nil {
		return recovery.SessionMeta{}, fmt.Errorf("load source session meta: %w", err)
	}
	if src == nil {
		return meta, nil
	}
	meta.MCPEnabledServers = recovery.NormalizeMCPEnabledServers(src.MCPEnabledServers)
	meta.RepoID = src.RepoID
	meta.RepoRoot = src.RepoRoot
	meta.WorktreeName = src.WorktreeName
	meta.WorktreeBranch = src.WorktreeBranch
	meta.WorktreePath = src.WorktreePath
	meta.IsMainWorktree = src.IsMainWorktree
	return meta, nil
}

// forkResumeSessionByCurrentProject forks sid at compaction boundary target
// inside the current working directory's project — the same lookup plain
// `chord --resume <sid>` uses — and returns the new session id. The session
// must belong to the current project; use `chord resume <sid> --fork-history`
// to locate a session that lives in another chord-managed worktree.
func forkResumeSessionByCurrentProject(sid string, target int) (newSID string, chosen int, seeded int, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", 0, 0, fmt.Errorf("resolve current directory: %w", err)
	}
	pl, err := startupPathLocator()
	if err != nil {
		return "", 0, 0, fmt.Errorf("resolve path locator: %w", err)
	}
	loc, err := resolveSessionInProject(context.Background(), pl, resolveContentRoot(context.Background(), cwd), sid)
	if err != nil {
		return "", 0, 0, err
	}
	srcDir, err := sessionDirForLocation(loc, sid)
	if err != nil {
		return "", 0, 0, err
	}
	newDir, chosen, seeded, err := forkSessionAtHistory(srcDir, filepath.Dir(srcDir), target)
	if err != nil {
		return "", 0, 0, err
	}
	return filepath.Base(newDir), chosen, seeded, nil
}
