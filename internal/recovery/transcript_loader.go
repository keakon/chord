package recovery

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

// sessionIDPattern matches local wall-clock session directory names
// (YYYYMMDDHHmmSSfff, digits only) produced by SessionIDForTime.
var sessionIDPattern = regexp.MustCompile(`^\d{17}$`)

// ReadOnlyTranscriptLoader reads main.jsonl transcripts strictly read-only. It
// is the only loader Memory extraction (and the cross-session reference
// surface) may use: it validates the session directory against the project
// sessions root, rejects path/symlink escape, never acquires the owner session
// lock, never loads attachments, and never modifies or normalizes the source
// session. Concurrent appends are handled by the same stable full-record and
// bounded-retry semantics as session restore.
type ReadOnlyTranscriptLoader struct {
	sessionsRoot string
}

// NewReadOnlyTranscriptLoader builds a loader rooted at the project sessions
// directory.
func NewReadOnlyTranscriptLoader(sessionsRoot string) *ReadOnlyTranscriptLoader {
	return &ReadOnlyTranscriptLoader{sessionsRoot: sessionsRoot}
}

// ValidateSessionID reports whether id is a valid local session directory name.
func ValidateSessionID(id string) bool {
	return sessionIDPattern.MatchString(id)
}

// resolveSessionDir resolves and validates a session directory under the
// sessions root. It refuses anything that is not a direct child, escapes the
// root through "..", or resolves through a symlink to outside the root.
func (l *ReadOnlyTranscriptLoader) resolveSessionDir(sessionID string) (string, error) {
	if !ValidateSessionID(sessionID) {
		return "", fmt.Errorf("invalid session id %q", sessionID)
	}
	if l == nil || l.sessionsRoot == "" {
		return "", fmt.Errorf("transcript loader has no sessions root")
	}
	root, err := filepath.EvalSymlinks(l.sessionsRoot)
	if err != nil {
		return "", fmt.Errorf("resolve sessions root: %w", err)
	}
	dir := filepath.Join(root, sessionID)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("session %s not found", sessionID)
		}
		return "", fmt.Errorf("resolve session dir: %w", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(dir) {
		return "", fmt.Errorf("session %s resolves outside the sessions root", sessionID)
	}
	return dir, nil
}

// Load reads the main transcript of sessionID, returning parsed messages in
// order. A truncated trailing record (crash mid-write) is skipped like session
// restore. Attachments (image/PDF bytes) are never loaded.
func (l *ReadOnlyTranscriptLoader) Load(sessionID string) ([]message.Message, error) {
	dir, err := l.resolveSessionDir(sessionID)
	if err != nil {
		return nil, err
	}
	return l.LoadDir(dir)
}

// LoadDir reads the main transcript from an already-validated session
// directory. It is used after a session switch where the frozen session dir is
// known; containment against the sessions root is still enforced.
func (l *ReadOnlyTranscriptLoader) LoadDir(sessionDir string) ([]message.Message, error) {
	if l == nil || l.sessionsRoot == "" {
		return nil, fmt.Errorf("transcript loader has no sessions root")
	}
	rootResolved, err := filepath.EvalSymlinks(l.sessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve sessions root: %w", err)
	}
	dirResolved, err := filepath.EvalSymlinks(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session dir not found: %s", sessionDir)
		}
		return nil, fmt.Errorf("resolve session dir: %w", err)
	}
	rel, err := filepath.Rel(rootResolved, dirResolved)
	// Only a direct child of the sessions root is accepted; anything nested,
	// escaping (..), or equal to the root itself is refused.
	if err != nil || rel == "." || rel == ".." || strings.ContainsRune(rel, filepath.Separator) || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("session dir %s is outside the sessions root", sessionDir)
	}
	mainPath := filepath.Join(dirResolved, identity.MainSessionLogFilename)
	return readTranscriptFile(mainPath)
}

// readTranscriptFile parses a JSONL transcript with stable full-record
// semantics: a truncated last record is skipped, and transient read failures
// (file being written concurrently) retry a bounded number of times.
func readTranscriptFile(path string) ([]message.Message, error) {
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		msgs, err := readTranscriptOnce(path)
		if err == nil {
			return msgs, nil
		}
		lastErr = err
		if !errors.Is(err, errTranscriptTransient) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("read transcript %s after %d attempts: %w", path, maxAttempts, lastErr)
}

// errTranscriptTransient marks a retryable read failure (concurrent writer).
var errTranscriptTransient = errors.New("transcript read transient failure")

// readTranscriptOnce performs one stable full-record parse of a JSONL file.
func readTranscriptOnce(path string) ([]message.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("transcript not found: %w", err)
		}
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("transcript is empty")
	}
	bufSize := min(recoveryMaxReadBufferSize, max(recoveryMinReadBufferSize, int(info.Size())))
	dec := json.NewDecoder(bufio.NewReaderSize(f, bufSize))
	var messages []message.Message
	for {
		var msg message.Message
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return messages, nil
			}
			// A mid-file parse error could mean the file is being rewritten
			// concurrently; a truncated tail is normal after a crash. Session
			// restore treats the tail as truncated and keeps prior records; for
			// memory extraction we require complete records, so a truncated tail
			// that happened mid-record is retried, and other parse errors fail.
			if isTruncatedRecordError(err) {
				return messages, nil
			}
			return nil, fmt.Errorf("%w: %v", errTranscriptTransient, err)
		}
		// Attachments are deliberately NOT loaded from disk here.
		messages = append(messages, msg)
	}
}

// isTruncatedRecordError reports whether the JSONL decoder error is a truncated
// final record (crash mid-write), which session restore tolerates by stopping.
func isTruncatedRecordError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}

// ListRecentCandidates returns the most recent unlocked (not held by a live
// process) sessions under the root, newest first, up to maxN. It powers the
// startup backfill; the caller decides which fingerprints are uncovered.
func (l *ReadOnlyTranscriptLoader) ListRecentCandidates(excludeDir string, maxN int) ([]SessionInfo, error) {
	if l == nil {
		return nil, nil
	}
	if maxN <= 0 {
		return nil, nil
	}
	list, err := ListSessions(l.sessionsRoot, excludeDir)
	if err != nil {
		return nil, err
	}
	var out []SessionInfo
	for _, s := range list {
		if s.Locked {
			// Active in another process: never backfill from it.
			continue
		}
		out = append(out, s)
		if len(out) >= maxN {
			break
		}
	}
	return out, nil
}
