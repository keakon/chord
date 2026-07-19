// Package recovery implements session persistence and crash recovery for Chord.
// It uses JSONL append-only logs for high-performance message persistence and
// JSON snapshots for state recovery (todo status, active agents).
package recovery

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/tools"
)

const maxSessionDirRetries = 1000

// SessionSnapshot captures the recoverable state of a session at a point in
// time. It is atomically written to snapshot.json for crash recovery.
type SessionSnapshot struct {
	Todos                     []TodoState              `json:"todos"`
	ActiveAgents              []AgentSnapshot          `json:"active_agents"`
	ModelName                 string                   `json:"model_name"`
	ActiveRole                string                   `json:"active_role,omitempty"`
	ModelPoolCurrentModelPool string                   `json:"model_pool_current_model_pool,omitempty"`
	ModelPoolAgentOverrides   map[string]string        `json:"model_pool_agent_overrides,omitempty"`
	CreatedAt                 time.Time                `json:"created_at"`
	LastInputTokens           int                      `json:"last_input_tokens"`                   // prompt size when snapshot was saved (for compaction/input-budget displays)
	LastTotalContextTokens    int                      `json:"last_total_context_tokens,omitempty"` // true input-side context burden when saved (input + cache_write); restored for diagnostics/full-burden displays
	CompactionGeneration      uint64                   `json:"compaction_generation,omitempty"`
	LastHistoryIndex          int                      `json:"last_history_index,omitempty"`
	SessionEpoch              uint64                   `json:"session_epoch,omitempty"`
	ActiveBackgroundObjects   []BackgroundObjectState  `json:"active_background_objects,omitempty"`
	PendingCompactionResume   *PendingCompactionResume `json:"pending_compaction_resume,omitempty"`
	// Usage statistics — restored when a session is resumed via /resume.
	UsageInputTokens      int64                            `json:"usage_input_tokens,omitempty"`
	UsageOutputTokens     int64                            `json:"usage_output_tokens,omitempty"`
	UsageCacheReadTokens  int64                            `json:"usage_cache_read_tokens,omitempty"`
	UsageCacheWriteTokens int64                            `json:"usage_cache_write_tokens,omitempty"`
	UsageReasoningTokens  int64                            `json:"usage_reasoning_tokens,omitempty"`
	UsageLLMCalls         int64                            `json:"usage_llm_calls,omitempty"`
	UsageEstimatedCost    float64                          `json:"usage_estimated_cost,omitempty"`
	UsageByModel          map[string]*analytics.ModelStats `json:"usage_by_model,omitempty"`
	UsageByAgent          map[string]*analytics.AgentStats `json:"usage_by_agent,omitempty"`
}

// BackgroundObjectState captures the durable summary of an active background object.
type BackgroundObjectState struct {
	ID            string    `json:"id"`
	AgentID       string    `json:"agent_id,omitempty"`
	Kind          string    `json:"kind,omitempty"`
	Description   string    `json:"description,omitempty"`
	Command       string    `json:"command"`
	StartedAt     time.Time `json:"started_at"`
	MaxRuntimeSec int       `json:"max_runtime_sec,omitempty"`
	Status        string    `json:"status"`
	FinishedAt    time.Time `json:"finished_at"`
}

// TodoState represents the persisted state of a single todo item.
type TodoState struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // "pending", "in_progress", "completed", "cancelled"
	Content string `json:"content,omitempty"`
}

// SnapshotTodoStates returns an ordered recovery snapshot copy of todo items.
func SnapshotTodoStates(items []tools.TodoItem) []TodoState {
	states := make([]TodoState, len(items))
	for i, item := range items {
		states[i] = TodoState{
			ID:      item.ID,
			Status:  item.Status,
			Content: item.Content,
		}
	}
	return states
}

// RestoreTodoItems returns an ordered todo list reconstructed from snapshot state.
func RestoreTodoItems(states []TodoState) []tools.TodoItem {
	items := make([]tools.TodoItem, len(states))
	for i, state := range states {
		items[i] = tools.TodoItem{
			ID:      state.ID,
			Status:  state.Status,
			Content: state.Content,
		}
	}
	return items
}

// AgentSnapshot captures the recoverable state of a running SubAgent.
type AgentSnapshot struct {
	InstanceID              string           `json:"instance_id"`    // e.g. "agent-1"
	TaskID                  string           `json:"task_id"`        // plan task ID or "adhoc-N"
	AgentDefName            string           `json:"agent_def_name"` // agent definition name (e.g. "backend-coder")
	TaskDesc                string           `json:"task_desc"`      // task description
	PlanTaskRef             string           `json:"plan_task_ref,omitempty"`
	SemanticTaskKey         string           `json:"semantic_task_key,omitempty"`
	ExpectedWriteScope      tools.WriteScope `json:"expected_write_scope"`
	SelectedModelRef        string           `json:"selected_model_ref,omitempty"`
	RunningModelRef         string           `json:"running_model_ref,omitempty"`
	OwnerAgentID            string           `json:"owner_agent_id,omitempty"`
	OwnerTaskID             string           `json:"owner_task_id,omitempty"`
	Depth                   int              `json:"depth,omitempty"`
	JoinToOwner             bool             `json:"join_to_owner,omitempty"`
	State                   string           `json:"state,omitempty"`
	LastSummary             string           `json:"last_summary,omitempty"`
	PendingCompleteIntent   bool             `json:"pending_complete_intent,omitempty"`
	PendingCompleteSummary  string           `json:"pending_complete_summary,omitempty"`
	PendingCompleteEnvelope json.RawMessage  `json:"pending_complete_envelope,omitempty"`
	Persistence             struct {
		State       string    `json:"state"`
		LastError   string    `json:"last_error,omitempty"`
		FailedAt    time.Time `json:"failed_at"`
		RecoveredAt time.Time `json:"recovered_at"`
	} `json:"persistence"`
}

// RecoveryManager handles session persistence via JSONL message logs and
// JSON state snapshots. It keeps file handles open for performance (one
// handle per agent's JSONL file) and uses a mutex to serialise writes.
//
// All public methods are goroutine-safe.
type RecoveryManager struct {
	sessionDir string
	mu         sync.Mutex
	handles    map[string]*os.File // agentID → open JSONL file handle
	closed     bool                // true after Close() is called
}

// NewRecoveryManager creates a new RecoveryManager rooted at sessionDir.
// The sessionDir and its agents/ subdirectory are created if they don't exist.
func NewRecoveryManager(sessionDir string) *RecoveryManager {
	return &RecoveryManager{
		sessionDir: sessionDir,
		handles:    make(map[string]*os.File),
	}
}

// CreateNewSessionDir creates a new UTC timestamp session directory using
// YYYYMMDDHHmmSSfff format with collision retries.
func CreateNewSessionDir(sessionsDir string) (string, error) {
	if err := privatefs.EnsureDir(sessionsDir, sessionsDir); err != nil {
		return "", fmt.Errorf("create sessions directory: %w", err)
	}
	lastID := ""
	for range maxSessionDirRetries {
		now := time.Now().UTC()
		sid := now.Format("20060102150405") + fmt.Sprintf("%03d", now.Nanosecond()/int(time.Millisecond))
		if sid == lastID {
			time.Sleep(time.Millisecond)
			now = time.Now().UTC()
			sid = now.Format("20060102150405") + fmt.Sprintf("%03d", now.Nanosecond()/int(time.Millisecond))
		}
		lastID = sid
		sessionDir := filepath.Join(sessionsDir, sid)
		if err := os.Mkdir(sessionDir, privatefs.DirMode); err == nil {
			return sessionDir, nil
		} else if os.IsExist(err) {
			continue
		} else {
			return "", fmt.Errorf("create session directory: %w", err)
		}
	}
	return "", fmt.Errorf("create session directory: too many collisions")
}

// imagesDir returns the path to the directory where image files are stored.
func (r *RecoveryManager) imagesDir() string {
	return filepath.Join(r.sessionDir, "images")
}

// persistBinaryParts writes any image/pdf ContentParts with raw Data to disk,
// replacing Data with an empty slice and setting ImagePath. The returned
// message is a shallow copy safe to marshal without the large byte slices.
func (r *RecoveryManager) persistBinaryParts(msg message.Message) (message.Message, error) {
	if len(msg.Parts) == 0 {
		return msg, nil
	}
	hasBinary := false
	for _, p := range msg.Parts {
		if p.IsBinary() && len(p.Data) > 0 && p.ImagePath == "" {
			hasBinary = true
			break
		}
	}
	if !hasBinary {
		return msg, nil
	}

	parts := make([]message.ContentPart, len(msg.Parts))
	copy(parts, msg.Parts)
	for i, p := range parts {
		if !p.IsBinary() || len(p.Data) == 0 || p.ImagePath != "" {
			continue
		}
		ext := ".bin"
		switch p.MimeType {
		case "image/png":
			ext = ".png"
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		case "application/pdf":
			ext = ".pdf"
		}
		fileName := fmt.Sprintf("%d-%d%s", time.Now().UnixNano(), i, ext)
		filePath := filepath.Join(r.imagesDir(), fileName)
		if err := privatefs.WriteFile(r.sessionDir, filePath, p.Data); err != nil {
			return msg, fmt.Errorf("write attachment file: %w", err)
		}
		parts[i].Data = nil
		parts[i].ImagePath = filePath
	}
	msg.Parts = parts
	return msg, nil
}

// PersistMessage appends a message to the agent's JSONL log file. The write
// is serialised with a mutex to prevent interleaving when multiple goroutines
// call this concurrently (e.g. handleLLMResponse and handleToolResult).
//
// Each message is written as a single JSON line terminated by '\n'.
// Image/PDF data in ContentParts is written to separate files under images/;
// only the file path is stored in the JSONL record.
// After Close is called, PersistMessage is a no-op and returns nil.
func (r *RecoveryManager) PersistMessage(agentID string, msg message.Message) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	var err error
	msg, err = r.persistBinaryParts(msg)
	if err != nil {
		log.Warnf("failed to persist binary parts, storing inline error=%v", err)
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}

	f, ok := r.handles[agentID]
	if !ok {
		path := r.messageLogPath(agentID)
		f, err = privatefs.OpenFile(r.sessionDir, path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
		if err != nil {
			return err
		}
		r.handles[agentID] = f
	}

	_, err = f.Write(data)
	return err
}

// LoadMessages reads all messages from an agent's JSONL log file. If the file
// does not exist, it returns nil (no error). A truncated last record (from a
// crash mid-write) is silently skipped — all records before it are returned.
func (r *RecoveryManager) LoadMessages(agentID string) ([]message.Message, error) {
	path := r.messageLogPath(agentID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var messages []message.Message
	readBufferSize := recoveryMinReadBufferSize
	if info, statErr := f.Stat(); statErr == nil {
		if count, ok := cachedMessageCount(path, info); ok {
			messages = make([]message.Message, 0, count)
		}
		readBufferSize = min(recoveryMaxReadBufferSize, max(recoveryMinReadBufferSize, int(info.Size())))
	}
	dec := json.NewDecoder(bufio.NewReaderSize(f, readBufferSize))
	for {
		var msg message.Message
		if err := dec.Decode(&msg); err != nil {
			if err != io.EOF {
				// Last record may be truncated due to crash mid-write.
				// Log at debug level and return what we have so far.
				log.Debugf("truncated record at end of JSONL, stopping recovery here agent=%v err=%v", agentID, err)
			}
			break
		}
		// Restore image/pdf data from disk for any parts that have ImagePath set.
		for i, p := range msg.Parts {
			if p.IsBinary() && p.ImagePath != "" && len(p.Data) == 0 {
				data, err := os.ReadFile(p.ImagePath)
				if err != nil {
					log.Warnf("failed to load attachment from disk path=%v error=%v", p.ImagePath, err)
					continue
				}
				msg.Parts[i].Data = data
			}
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// SaveSnapshot atomically writes a session snapshot to snapshot.json.
// It first writes to a temporary file, then renames it to the final path,
// ensuring the snapshot file is never in a partially-written state.
func (r *RecoveryManager) SaveSnapshot(snap *SessionSnapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}

	tmpPath := filepath.Join(r.sessionDir, fmt.Sprintf("snapshot.%d.json.tmp", time.Now().UnixNano()))
	if err := privatefs.WriteFile(r.sessionDir, tmpPath, data); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(r.sessionDir, "snapshot.json"))
}

// Recover loads the session snapshot from snapshot.json. Returns an error if
// the file does not exist or cannot be parsed.
func (r *RecoveryManager) Recover() (*SessionSnapshot, error) {
	data, err := os.ReadFile(filepath.Join(r.sessionDir, "snapshot.json"))
	if err != nil {
		return nil, err
	}

	var snap SessionSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// Close flushes and closes all open JSONL file handles. This should be called
// during graceful shutdown. After Close, PersistMessage is a no-op.
func (r *RecoveryManager) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.handles {
		f.Close()
	}
	r.handles = make(map[string]*os.File)
	r.closed = true
}

// RewriteLog replaces the JSONL file for agentID with the given messages.
// The existing file handle is closed, the file is truncated, and messages
// are written in order. Used when the last message needs to be surgically
// removed (e.g. removing an interrupted thinking-only assistant block).
func (r *RecoveryManager) RewriteLog(agentID string, msgs []message.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}

	// Close and remove the existing handle.
	if f, ok := r.handles[agentID]; ok {
		f.Close()
		delete(r.handles, agentID)
	}

	path := r.messageLogPath(agentID)

	// Truncate by rewriting.
	f, err := privatefs.OpenFile(r.sessionDir, path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("rewrite log: create %s: %w", path, err)
	}

	for _, msg := range msgs {
		data, err := json.Marshal(msg)
		if err != nil {
			f.Close()
			return fmt.Errorf("rewrite log: marshal: %w", err)
		}
		data = append(data, '\n')
		if _, err := f.Write(data); err != nil {
			f.Close()
			return fmt.Errorf("rewrite log: write: %w", err)
		}
	}
	f.Close()

	// Re-open in append mode for subsequent PersistMessage calls.
	af, err := privatefs.OpenFile(r.sessionDir, path, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return fmt.Errorf("rewrite log: reopen: %w", err)
	}
	r.handles[agentID] = af
	return nil
}

// SessionInfo holds metadata for one session directory, for listing and
// display (e.g. session picker). LastModTime is the mtime of main.jsonl;
// FirstUserMessage is a short preview of the first user message.
type SessionInfo struct {
	ID                                  string    // directory name (e.g. Unix millisecond timestamp)
	Path                                string    // full path to session directory
	Title                               string    // user-defined title, when set
	MessageCount                        int       // total messages in main.jsonl
	LastModTime                         time.Time // last write to main.jsonl
	FirstUserMessage                    string    // preview of first user message (truncated, newlines replaced)
	FirstUserMessageIsCompactionSummary bool      // true when FirstUserMessage is the synthetic compaction summary
	OriginalFirstUserMessage            string    // original first user message, preserved across compaction
	ForkedFrom                          string    // parent session ID when this session was created via fork
	Locked                              bool      // true when another live Chord process currently holds session.lock
}

// maxFirstUserMessagePreview is the max rune count for FirstUserMessage in SessionInfo.
const (
	maxFirstUserMessagePreview = 80
	recoveryMinReadBufferSize  = 4 * 1024
	recoveryMaxReadBufferSize  = 64 * 1024
)

// ListSessions scans the sessions directory and returns SessionInfo for each
// session that has a non-empty main.jsonl. Results are sorted by directory
// name descending (newest first). excludeDir is the full path of a session
// directory to omit (e.g. current session); pass "" to include all.
func ListSessions(sessionsDir string, excludeDir string) ([]SessionInfo, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// Sort by name descending (newest first).
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	var list []SessionInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionPath := filepath.Join(sessionsDir, entry.Name())
		if excludeDir != "" && sessionPath == excludeDir {
			continue
		}
		mainPath := filepath.Join(sessionPath, identity.MainSessionLogFilename)
		info, err := os.Stat(mainPath)
		if err != nil || info.Size() == 0 {
			continue
		}
		firstUser, firstUserIsCompactionSummary, originalFirstUser, lastUpdatedAt := cachedSessionPreview(sessionPath)
		lastModTime := info.ModTime()
		if !lastUpdatedAt.IsZero() && lastUpdatedAt.After(lastModTime) {
			lastModTime = lastUpdatedAt
		}
		if originalFirstUser == "" && !firstUserIsCompactionSummary {
			originalFirstUser = firstUser
		}
		messageCount := UnknownMessageCount
		if count, ok := cachedMessageCount(mainPath, info); ok {
			messageCount = count
		}
		locked, err := sessionDirLockedByLiveOwner(sessionPath)
		if err != nil {
			return nil, fmt.Errorf("check session lock for %s: %w", entry.Name(), err)
		}
		forkedFrom := ""
		title := ""
		if meta, err := LoadSessionMeta(sessionPath); err != nil {
			return nil, fmt.Errorf("load session meta for %s: %w", entry.Name(), err)
		} else if meta != nil {
			forkedFrom = meta.ForkedFrom
			title = meta.Title
		}
		list = append(list, SessionInfo{
			ID:                                  entry.Name(),
			Path:                                sessionPath,
			Title:                               title,
			MessageCount:                        messageCount,
			LastModTime:                         lastModTime,
			FirstUserMessage:                    firstUser,
			FirstUserMessageIsCompactionSummary: firstUserIsCompactionSummary,
			OriginalFirstUserMessage:            originalFirstUser,
			ForkedFrom:                          forkedFrom,
			Locked:                              locked,
		})
	}
	return list, nil
}

// UnknownMessageCount marks SessionInfo.MessageCount as not yet computed.
// Session pickers can render this as a placeholder and fill exact counts in a
// background pass without blocking the initial list on reading every log file.
const UnknownMessageCount = -1

func cachedSessionPreview(sessionPath string) (firstUser string, firstUserIsCompactionSummary bool, originalFirstUser string, lastUpdatedAt time.Time) {
	summary, err := analytics.LoadCachedSessionUsageSummary(sessionPath)
	if err != nil || summary == nil {
		return "", false, "", time.Time{}
	}
	return summary.FirstUserMessage, summary.FirstUserMessageIsCompactionSummary, summary.OriginalFirstUserMessage, summary.LastUpdatedAt
}

// messageCountCache memoizes per-log newline counts keyed by the file's
// (size, mtime) identity, so reopening the session picker does not re-read
// every session's main.jsonl in full. Successful session deletion evicts the
// corresponding entry.
var messageCountCache sync.Map // mainPath string -> messageCountEntry

type messageCountEntry struct {
	size    int64
	modTime time.Time
	count   int
}

func cachedMessageCount(mainPath string, info os.FileInfo) (int, bool) {
	if cached, ok := messageCountCache.Load(mainPath); ok {
		entry := cached.(messageCountEntry)
		if entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) {
			return entry.count, true
		}
	}
	return 0, false
}

func countMessages(mainPath string, info os.FileInfo) int {
	if count, ok := cachedMessageCount(mainPath, info); ok {
		return count
	}

	f, err := os.Open(mainPath)
	if err != nil {
		return 0
	}
	defer f.Close()

	// Each message is persisted by PersistMessage as a single JSON line
	// terminated by '\n' (in-string newlines are escaped by json.Marshal), so
	// counting newline bytes yields the message count without decoding every
	// record — important when the picker lists hundreds of sessions.
	buf := make([]byte, 64*1024)
	count := 0
	for {
		n, err := f.Read(buf)
		count += bytes.Count(buf[:n], []byte{'\n'})
		if err != nil {
			break
		}
	}
	messageCountCache.Store(mainPath, messageCountEntry{size: info.Size(), modTime: info.ModTime(), count: count})
	return count
}

// CountSessionMessages returns the exact number of messages in a session's
// main log. It is separated from ListSessions so callers can fill this heavier
// display field after showing the initial session list.
func CountSessionMessages(sessionPath string) (int, error) {
	mainPath := filepath.Join(sessionPath, identity.MainSessionLogFilename)
	info, err := os.Stat(mainPath)
	if err != nil {
		return 0, err
	}
	if info.Size() == 0 {
		return 0, nil
	}
	return countMessages(mainPath, info), nil
}

// firstUserMessageFromFile reads main.jsonl and returns a preview of the first
// user message (truncated, newlines replaced with space). Returns empty string
// on error or if no user message exists.
func firstUserMessageFromFile(mainPath string) (string, error) {
	f, err := os.Open(mainPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	for {
		var msg message.Message
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			continue // skip malformed lines
		}
		// Synthetic user-role messages must not become the session's original
		// user prompt when restoring metadata after compaction.
		if !message.IsUserAuthored(msg) {
			continue
		}
		s := message.UserPromptPlainText(msg)
		s = strings.ReplaceAll(s, "\r\n", " ")
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, "\r", " ")
		s = strings.TrimSpace(s)
		if utf8.RuneCountInString(s) > maxFirstUserMessagePreview {
			s = string([]rune(s)[:maxFirstUserMessagePreview]) + "…"
		}
		return s, nil
	}
	return "", nil
}

// FirstUserMessageFromFile returns the first user message preview from main.jsonl.
func FirstUserMessageFromFile(mainPath string) (string, error) {
	return firstUserMessageFromFile(mainPath)
}

// SessionInfoForDir returns SessionInfo for a single session directory.
// Returns nil if the directory has no main.jsonl or it is empty.
func SessionInfoForDir(sessionPath string) *SessionInfo {
	mainPath := filepath.Join(sessionPath, identity.MainSessionLogFilename)
	info, err := os.Stat(mainPath)
	if err != nil || info.Size() == 0 {
		return nil
	}
	lastModTime := info.ModTime()
	firstUser, _ := firstUserMessageFromFile(mainPath)
	firstUserIsCompactionSummary := false
	originalFirstUser := ""
	if summary, err := analytics.LoadSessionUsageSummary(sessionPath); err == nil && summary != nil {
		if !summary.LastUpdatedAt.IsZero() && summary.LastUpdatedAt.After(lastModTime) {
			lastModTime = summary.LastUpdatedAt
		}
		if summary.FirstUserMessage != "" {
			firstUser = summary.FirstUserMessage
			firstUserIsCompactionSummary = summary.FirstUserMessageIsCompactionSummary
		}
		if summary.OriginalFirstUserMessage != "" {
			originalFirstUser = summary.OriginalFirstUserMessage
		}
	}
	if originalFirstUser == "" && !firstUserIsCompactionSummary {
		originalFirstUser = firstUser
	}
	locked, err := sessionDirLockedByLiveOwner(sessionPath)
	if err != nil {
		return nil
	}
	forkedFrom := ""
	if meta, err := LoadSessionMeta(sessionPath); err == nil && meta != nil {
		forkedFrom = meta.ForkedFrom
	}
	return &SessionInfo{
		ID:                                  filepath.Base(sessionPath),
		Path:                                sessionPath,
		LastModTime:                         lastModTime,
		FirstUserMessage:                    firstUser,
		FirstUserMessageIsCompactionSummary: firstUserIsCompactionSummary,
		OriginalFirstUserMessage:            originalFirstUser,
		ForkedFrom:                          forkedFrom,
		Locked:                              locked,
	}
}

// FindMostRecentSession scans the sessions directory for the most recent
// session that has a non-empty main.jsonl, regardless of CleanExit status.
// This is used by /resume to find any previous session to restore from.
//
// excludeDir is the path of a session directory to skip (typically the
// current session). Pass "" to not exclude any directory.
//
// Sessions are sorted by directory name (newest first, since names are
// Unix millisecond timestamps). Returns "" if no suitable session is found.
func FindMostRecentSession(sessionsDir string, excludeDir string) string {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return ""
	}

	// Sort by name descending (newest first).
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionPath := filepath.Join(sessionsDir, entry.Name())

		// Skip the excluded directory (e.g. current session).
		if excludeDir != "" && sessionPath == excludeDir {
			continue
		}

		// Check for main.jsonl with content.
		mainJSONL := filepath.Join(sessionPath, identity.MainSessionLogFilename)
		if info, err := os.Stat(mainJSONL); err == nil && info.Size() > 0 {
			return sessionPath
		}
	}

	return ""
}

// messageLogPath returns the JSONL file path for the given agent.
// "main" maps to {sessionDir}/main.jsonl; all other agents map to
// {sessionDir}/agents/{agentID}.jsonl.
func (r *RecoveryManager) messageLogPath(agentID string) string {
	if agentID == identity.MainAgentID {
		return filepath.Join(r.sessionDir, identity.MainSessionLogFilename)
	}
	return filepath.Join(r.sessionDir, "agents", agentID+".jsonl")
}

// ListSubAgentIDs scans the {sessionDir}/agents/ directory and returns all
// SubAgent instance IDs that have JSONL files. The IDs are derived from the
// file names (e.g. "builder-1.jsonl" → "builder-1").
func (r *RecoveryManager) ListSubAgentIDs() []string {
	agentsDir := filepath.Join(r.sessionDir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
