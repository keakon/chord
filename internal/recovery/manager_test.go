package recovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

// helper creates a temp dir and a RecoveryManager rooted there.
func newTestManager(t *testing.T) (*RecoveryManager, string) {
	t.Helper()
	dir := t.TempDir()
	rm := NewRecoveryManager(dir)
	return rm, dir
}

// ---------------------------------------------------------------------------
// PersistMessage + LoadMessages roundtrip
// ---------------------------------------------------------------------------

func TestListSessionsPreservesOriginalFirstUserMessageAcrossCompaction(t *testing.T) {
	sessionsDir := t.TempDir()
	sessionPath := filepath.Join(sessionsDir, "1000")
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(sessionPath, "main.jsonl")
	f, err := os.OpenFile(mainPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(message.Message{Role: "user", Content: "summary request", IsCompactionSummary: true}); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	ledger := analytics.NewUsageLedger(sessionPath, "")
	if err := ledger.SetFirstUserMessage("original first request"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}
	if err := ledger.RewriteFirstUserMessage("summary request"); err != nil {
		t.Fatalf("RewriteFirstUserMessage: %v", err)
	}

	list, err := ListSessions(sessionsDir, "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(ListSessions) = %d, want 1", len(list))
	}
	if list[0].FirstUserMessage != "summary request" {
		t.Fatalf("FirstUserMessage = %q", list[0].FirstUserMessage)
	}
	if list[0].OriginalFirstUserMessage != "original first request" {
		t.Fatalf("OriginalFirstUserMessage = %q", list[0].OriginalFirstUserMessage)
	}
}

func TestFirstUserMessageFromFileSkipsCompactionSummary(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.jsonl")
	first := message.Message{
		Role:                "user",
		Content:             "[Context Summary]\n## Goal\n…",
		IsCompactionSummary: true,
	}
	second := message.Message{Role: "user", Content: "what is up"}
	enc := func(m message.Message) []byte {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return append(b, '\n')
	}
	if err := os.WriteFile(mainPath, append(enc(first), enc(second)...), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := FirstUserMessageFromFile(mainPath)
	if err != nil {
		t.Fatalf("FirstUserMessageFromFile: %v", err)
	}
	if got != "what is up" {
		t.Fatalf("FirstUserMessageFromFile = %q, want %q", got, "what is up")
	}
}

func TestFirstUserMessageFromFileSkipsSyntheticUserMessages(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.jsonl")
	messages := []message.Message{
		{Role: message.RoleUser, Content: "mailbox", Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: "worker-1-1"}},
		{Role: message.RoleUser, Content: "loop", Kind: message.KindLoopNotice},
		{Role: message.RoleUser, Content: "real user message"},
	}
	var payload []byte
	for _, msg := range messages {
		encoded, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		payload = append(payload, encoded...)
		payload = append(payload, '\n')
	}
	if err := os.WriteFile(mainPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := FirstUserMessageFromFile(mainPath)
	if err != nil {
		t.Fatalf("FirstUserMessageFromFile: %v", err)
	}
	if got != "real user message" {
		t.Fatalf("FirstUserMessageFromFile = %q, want real user message", got)
	}
}

func TestPersistAndLoad_Roundtrip(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()

	msgs := []message.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there", ToolCalls: []message.ToolCall{
			{ID: "tc-1", Name: "Read", Args: json.RawMessage(`{"path":"main.go"}`)},
		}},
		{Role: "tool", ToolCallID: "tc-1", Content: "file contents..."},
	}

	for _, msg := range msgs {
		if err := rm.PersistMessage("main", msg); err != nil {
			t.Fatalf("PersistMessage: %v", err)
		}
	}

	loaded, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	if len(loaded) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(loaded))
	}

	for i, got := range loaded {
		want := msgs[i]
		if got.Role != want.Role {
			t.Errorf("msg[%d].Role = %q, want %q", i, got.Role, want.Role)
		}
		if got.Content != want.Content {
			t.Errorf("msg[%d].Content = %q, want %q", i, got.Content, want.Content)
		}
		if got.ToolCallID != want.ToolCallID {
			t.Errorf("msg[%d].ToolCallID = %q, want %q", i, got.ToolCallID, want.ToolCallID)
		}
		if len(got.ToolCalls) != len(want.ToolCalls) {
			t.Errorf("msg[%d].ToolCalls len = %d, want %d", i, len(got.ToolCalls), len(want.ToolCalls))
		}
	}
}

func TestPersistMessageCreatesPrivateSessionFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	sessionDir := filepath.Join(t.TempDir(), "session")
	rm := NewRecoveryManager(sessionDir)
	defer rm.Close()

	if err := rm.PersistMessage("subagent-1", message.Message{Role: "user", Content: "secret"}); err != nil {
		t.Fatalf("PersistMessage: %v", err)
	}

	for _, path := range []string{sessionDir, filepath.Join(sessionDir, "agents")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("mode(%s) = %04o, want 0700", path, got)
		}
	}
	logPath := filepath.Join(sessionDir, "agents", "subagent-1.jsonl")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat %s: %v", logPath, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode(%s) = %04o, want 0600", logPath, got)
	}
}

func TestPersistMessageRestrictsExistingSessionFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	sessionDir := filepath.Join(t.TempDir(), "session")
	agentsDir := filepath.Join(sessionDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(agentsDir, "subagent-1.jsonl")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	rm := NewRecoveryManager(sessionDir)
	defer rm.Close()
	if err := rm.PersistMessage("subagent-1", message.Message{Role: "user", Content: "secret"}); err != nil {
		t.Fatalf("PersistMessage: %v", err)
	}

	for _, path := range []string{sessionDir, agentsDir} {
		assertMode(t, path, 0o700)
	}
	assertMode(t, logPath, 0o600)
}

func TestPersistMessageAfterCloseDoesNotCreateSessionDir(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session")
	rm := NewRecoveryManager(sessionDir)
	rm.Close()

	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "secret"}); err != nil {
		t.Fatalf("PersistMessage after Close: %v", err)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session directory created after Close: %v", err)
	}
}

func BenchmarkPersistMessageReusesOpenLog(b *testing.B) {
	rm := NewRecoveryManager(b.TempDir())
	b.Cleanup(rm.Close)
	msg := message.Message{Role: "user", Content: "benchmark"}
	if err := rm.PersistMessage("main", msg); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := rm.PersistMessage("main", msg); err != nil {
			b.Fatal(err)
		}
	}
}

func TestRewriteLogCreatesPrivateSessionFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	sessionDir := t.TempDir()
	rm := NewRecoveryManager(sessionDir)
	defer rm.Close()

	if err := rm.RewriteLog("main", []message.Message{{Role: "user", Content: "secret"}}); err != nil {
		t.Fatalf("RewriteLog: %v", err)
	}

	logPath := filepath.Join(sessionDir, "main.jsonl")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat %s: %v", logPath, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode(%s) = %04o, want 0600", logPath, got)
	}
}

func TestRewriteLogRestrictsExistingSessionFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	sessionDir := t.TempDir()
	logPath := filepath.Join(sessionDir, "main.jsonl")
	if err := os.WriteFile(logPath, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := NewRecoveryManager(sessionDir)
	defer rm.Close()
	if err := rm.RewriteLog("main", []message.Message{{Role: "user", Content: "secret"}}); err != nil {
		t.Fatalf("RewriteLog: %v", err)
	}
	assertMode(t, logPath, 0o600)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%s) = %04o, want %04o", path, got, want)
	}
}

func TestSaveSnapshotWritesCompactJSON(t *testing.T) {
	rm, dir := newTestManager(t)
	defer rm.Close()

	snap := &SessionSnapshot{
		Todos:      []TodoState{{ID: "1", Status: "pending", Content: "test"}},
		ModelName:  "test-model",
		ActiveRole: "planner",
		PendingCompactionResume: &PendingCompactionResume{
			Kind:       "auto_continue",
			Mode:       "replay_user_intent",
			UserIntent: "finish the refactor safely",
		},
	}
	if err := rm.SaveSnapshot(snap); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		t.Fatalf("ReadFile(snapshot.json): %v", err)
	}
	if len(data) > 0 && data[0] != '{' {
		t.Fatalf("snapshot.json should be compact JSON, starts with %q", data[:1])
	}
	var decoded SessionSnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal(snapshot.json): %v", err)
	}
	if decoded.ModelName != snap.ModelName {
		t.Fatalf("ModelName = %q, want %q", decoded.ModelName, snap.ModelName)
	}
	if decoded.ActiveRole != snap.ActiveRole {
		t.Fatalf("ActiveRole = %q, want %q", decoded.ActiveRole, snap.ActiveRole)
	}
	if len(decoded.Todos) != 1 || decoded.Todos[0].ID != "1" {
		t.Fatalf("decoded todos = %+v, want ordered todo with id=1", decoded.Todos)
	}
	if decoded.PendingCompactionResume == nil || decoded.PendingCompactionResume.UserIntent != "finish the refactor safely" {
		t.Fatalf("decoded PendingCompactionResume = %+v, want restored user intent", decoded.PendingCompactionResume)
	}
}

func TestPersistAndLoad_ImagePartsRoundTrip(t *testing.T) {
	rm, dir := newTestManager(t)
	defer rm.Close()

	original := []byte{0x89, 0x50, 0x4e, 0x47, 0x01, 0x02, 0x03}
	msg := message.Message{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "look"},
			{Type: "image", MimeType: "image/png", Data: original, FileName: "sample.png"},
		},
	}
	if err := rm.PersistMessage("main", msg); err != nil {
		t.Fatalf("PersistMessage: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "images"))
	if err != nil {
		t.Fatalf("ReadDir(images): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 persisted image, got %d", len(entries))
	}

	loaded, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 message, got %d", len(loaded))
	}
	if len(loaded[0].Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(loaded[0].Parts))
	}
	img := loaded[0].Parts[1]
	if img.Type != "image" {
		t.Fatalf("loaded image part type = %q, want image", img.Type)
	}
	if img.ImagePath == "" {
		t.Fatal("expected ImagePath to be persisted")
	}
	if img.FileName != "sample.png" {
		t.Fatalf("FileName = %q, want sample.png", img.FileName)
	}
	if string(img.Data) != string(original) {
		t.Fatalf("image data mismatch: got %v want %v", img.Data, original)
	}
}

func TestPersistAndLoad_PDFPartsRoundTrip(t *testing.T) {
	rm, dir := newTestManager(t)
	defer rm.Close()

	original := []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\nfake pdf")
	msg := message.Message{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "read this"},
			{Type: "pdf", MimeType: "application/pdf", Data: original, FileName: "report.pdf"},
		},
	}
	if err := rm.PersistMessage("main", msg); err != nil {
		t.Fatalf("PersistMessage: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "images"))
	if err != nil {
		t.Fatalf("ReadDir(images): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 persisted attachment, got %d", len(entries))
	}
	if ext := filepath.Ext(entries[0].Name()); ext != ".pdf" {
		t.Fatalf("persisted file ext = %q, want .pdf", ext)
	}

	loaded, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 1 || len(loaded[0].Parts) != 2 {
		t.Fatalf("unexpected loaded shape: %#v", loaded)
	}
	pdf := loaded[0].Parts[1]
	if pdf.Type != "pdf" {
		t.Fatalf("loaded pdf part type = %q, want pdf", pdf.Type)
	}
	if pdf.ImagePath == "" {
		t.Fatal("expected ImagePath to be persisted for pdf")
	}
	if pdf.FileName != "report.pdf" {
		t.Fatalf("FileName = %q, want report.pdf", pdf.FileName)
	}
	if string(pdf.Data) != string(original) {
		t.Fatalf("pdf data mismatch: got %v want %v", pdf.Data, original)
	}
}

func TestPersistAndLoad_SubAgent(t *testing.T) {
	rm, dir := newTestManager(t)
	defer rm.Close()

	if err := rm.PersistMessage("agent-1", message.Message{
		Role: "user", Content: "implement auth",
	}); err != nil {
		t.Fatalf("PersistMessage: %v", err)
	}

	loaded, err := rm.LoadMessages("agent-1")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Content != "implement auth" {
		t.Fatalf("unexpected loaded messages: %+v", loaded)
	}

	// Verify the file is in the agents/ subdirectory.
	expectedPath := filepath.Join(dir, "agents", "agent-1.jsonl")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("expected file at %s: %v", expectedPath, err)
	}
}

func TestLoadMessages_NonexistentFile(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()

	loaded, err := rm.LoadMessages("nonexistent")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent file, got: %v", err)
	}
	if loaded != nil {
		t.Fatalf("expected nil messages, got: %+v", loaded)
	}
}

// ---------------------------------------------------------------------------
// Concurrent PersistMessage
// ---------------------------------------------------------------------------

func TestPersistMessage_ConcurrentSafety(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()

	const goroutines = 50
	const msgsPerGoroutine = 10

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			for range msgsPerGoroutine {
				msg := message.Message{
					Role:    "user",
					Content: "msg from goroutine",
				}
				if err := rm.PersistMessage("main", msg); err != nil {
					t.Errorf("PersistMessage error: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	loaded, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	expected := goroutines * msgsPerGoroutine
	if len(loaded) != expected {
		t.Errorf("expected %d messages, got %d", expected, len(loaded))
	}
}

// ---------------------------------------------------------------------------
// Snapshot save + recover
// ---------------------------------------------------------------------------

func TestSaveSnapshot_AndRecover(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()

	now := time.Now().UTC().Truncate(time.Second) // truncate for JSON roundtrip
	snap := &SessionSnapshot{
		Todos: []TodoState{
			{ID: "1", Status: "completed", Content: "plan step 1"},
			{ID: "2", Status: "in_progress", Content: "plan step 2"},
			{ID: "3", Status: "pending", Content: "plan step 3"},
		},
		ActiveAgents: []AgentSnapshot{
			{
				InstanceID:   "agent-2",
				TaskID:       "2",
				AgentDefName: "backend-coder",
				TaskDesc:     "implement API endpoints",
			},
		},
		ModelName:                 "claude-opus-4.7",
		ActiveRole:                "planner",
		ModelPoolCurrentModelPool: "strong",
		ModelPoolAgentOverrides: map[string]string{
			"reviewer": "fast",
		},
		CreatedAt: now,
	}

	if err := rm.SaveSnapshot(snap); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(rm.sessionDir, "snapshot.json"))
	if err != nil {
		t.Fatalf("ReadFile(snapshot.json): %v", err)
	}
	if !strings.Contains(string(raw), "model_pool_current_model_pool") || strings.Contains(string(raw), "model_pool_current_role") {
		t.Fatalf("snapshot should use model_pool_current_model_pool only, got: %s", raw)
	}

	recovered, err := rm.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if recovered.ModelName != snap.ModelName {
		t.Errorf("ModelName = %q, want %q", recovered.ModelName, snap.ModelName)
	}
	if recovered.ActiveRole != snap.ActiveRole {
		t.Errorf("ActiveRole = %q, want %q", recovered.ActiveRole, snap.ActiveRole)
	}
	if recovered.ModelPoolCurrentModelPool != snap.ModelPoolCurrentModelPool {
		t.Errorf("ModelPoolCurrentModelPool = %q, want %q", recovered.ModelPoolCurrentModelPool, snap.ModelPoolCurrentModelPool)
	}
	if recovered.ModelPoolAgentOverrides["reviewer"] != "fast" {
		t.Errorf("ModelPoolAgentOverrides = %+v, want reviewer=fast", recovered.ModelPoolAgentOverrides)
	}
	if !recovered.CreatedAt.Equal(snap.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", recovered.CreatedAt, snap.CreatedAt)
	}
	if len(recovered.Todos) != 3 {
		t.Errorf("expected 3 todos, got %d", len(recovered.Todos))
	}
	if recovered.Todos[0].ID != "1" || recovered.Todos[0].Status != "completed" {
		t.Errorf("todo[0] = %+v, want id=1 completed", recovered.Todos[0])
	}
	if recovered.Todos[1].ID != "2" || recovered.Todos[1].Status != "in_progress" {
		t.Errorf("todo[1] = %+v, want id=2 in_progress", recovered.Todos[1])
	}
	if len(recovered.ActiveAgents) != 1 {
		t.Fatalf("expected 1 active agent, got %d", len(recovered.ActiveAgents))
	}
	aa := recovered.ActiveAgents[0]
	if aa.InstanceID != "agent-2" || aa.AgentDefName != "backend-coder" {
		t.Errorf("active agent = %+v, unexpected", aa)
	}
}

func TestRecover_NoSnapshot(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()

	_, err := rm.Recover()
	if err == nil {
		t.Fatal("expected error when no snapshot exists")
	}
}

func TestSaveSnapshot_AtomicOverwrite(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()

	// Save first snapshot.
	snap1 := &SessionSnapshot{
		ModelName:  "model-v1",
		ActiveRole: "builder",
		CreatedAt:  time.Now().UTC(),
		Todos:      []TodoState{{ID: "1", Status: "pending"}},
	}
	if err := rm.SaveSnapshot(snap1); err != nil {
		t.Fatalf("SaveSnapshot v1: %v", err)
	}

	// Save second snapshot (overwrites).
	snap2 := &SessionSnapshot{
		ModelName:  "model-v2",
		ActiveRole: "planner",
		CreatedAt:  time.Now().UTC(),
		Todos:      []TodoState{{ID: "1", Status: "completed"}},
	}
	if err := rm.SaveSnapshot(snap2); err != nil {
		t.Fatalf("SaveSnapshot v2: %v", err)
	}

	recovered, err := rm.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if recovered.ModelName != "model-v2" {
		t.Errorf("ModelName = %q, want model-v2", recovered.ModelName)
	}
	if recovered.ActiveRole != "planner" {
		t.Errorf("ActiveRole = %q, want planner", recovered.ActiveRole)
	}
	if len(recovered.Todos) != 1 || recovered.Todos[0].Status != "completed" {
		t.Errorf("todo[0] = %+v, want completed", recovered.Todos)
	}
}

// ---------------------------------------------------------------------------
// Truncated JSONL handling
// ---------------------------------------------------------------------------

func TestLoadMessages_TruncatedLastRecord(t *testing.T) {
	rm, dir := newTestManager(t)

	// Write two valid messages, then append a truncated JSON line.
	msg1 := message.Message{Role: "user", Content: "first"}
	msg2 := message.Message{Role: "assistant", Content: "second"}
	if err := rm.PersistMessage("main", msg1); err != nil {
		t.Fatalf("PersistMessage 1: %v", err)
	}
	if err := rm.PersistMessage("main", msg2); err != nil {
		t.Fatalf("PersistMessage 2: %v", err)
	}
	rm.Close()

	// Manually append a truncated JSON line to simulate crash mid-write.
	path := filepath.Join(dir, "main.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	f.WriteString(`{"role":"user","content":"trunca`)
	f.Close()

	// Create a fresh manager and load.
	rm2 := NewRecoveryManager(dir)
	defer rm2.Close()

	loaded, err := rm2.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages with truncated record: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("expected 2 valid messages, got %d", len(loaded))
	}
	if loaded[0].Content != "first" {
		t.Errorf("msg[0].Content = %q, want first", loaded[0].Content)
	}
	if loaded[1].Content != "second" {
		t.Errorf("msg[1].Content = %q, want second", loaded[1].Content)
	}
}

func TestLoadMessages_EmptyFile(t *testing.T) {
	rm, dir := newTestManager(t)
	defer rm.Close()

	// Create an empty file.
	path := filepath.Join(dir, "main.jsonl")
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatalf("create empty file: %v", err)
	}

	loaded, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages empty file: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected 0 messages from empty file, got %d", len(loaded))
	}
}

// ---------------------------------------------------------------------------
// messageLogPath
// ---------------------------------------------------------------------------

func TestMessageLogPath(t *testing.T) {
	rm := NewRecoveryManager("/tmp/sessions/abc")

	tests := []struct {
		agentID  string
		expected string
	}{
		{"main", "/tmp/sessions/abc/main.jsonl"},
		{"agent-1", "/tmp/sessions/abc/agents/agent-1.jsonl"},
		{"agent-42", "/tmp/sessions/abc/agents/agent-42.jsonl"},
	}

	for _, tt := range tests {
		got := rm.messageLogPath(tt.agentID)
		if got != tt.expected {
			t.Errorf("messageLogPath(%q) = %q, want %q", tt.agentID, got, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestClose_DoubleCloseNoPanic(t *testing.T) {
	rm, _ := newTestManager(t)

	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("PersistMessage: %v", err)
	}

	rm.Close()
	rm.Close() // should not panic
}

// ---------------------------------------------------------------------------
// ListSessions, SessionInfoForDir, firstUserMessageFromFile
// ---------------------------------------------------------------------------

func TestListSessions_FlagsLockedSessions(t *testing.T) {
	sessionsDir := t.TempDir()
	s1 := filepath.Join(sessionsDir, "1000")
	if err := os.MkdirAll(s1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s1, "main.jsonl"), []byte(`{"role":"user","content":"hello"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireSessionLock(s1)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	defer lock.Release()

	list, err := ListSessions(sessionsDir, "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if !list[0].Locked {
		t.Fatal("expected locked session to be flagged as Locked")
	}
}

func TestListSessions_AndSessionInfoForDir(t *testing.T) {
	sessionsDir := t.TempDir()

	// Empty: no sessions
	list, err := ListSessions(sessionsDir, "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(list))
	}

	// Create two session dirs with main.jsonl
	s1 := filepath.Join(sessionsDir, "1000")
	s2 := filepath.Join(sessionsDir, "2000")
	if err := os.MkdirAll(filepath.Join(s1, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s2, "agents"), 0755); err != nil {
		t.Fatal(err)
	}

	writeMsg := func(dir string, role message.Role, content string) {
		f, _ := os.OpenFile(filepath.Join(dir, "main.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		data, _ := json.Marshal(message.Message{Role: role, Content: content})
		f.Write(append(data, '\n'))
		f.Close()
	}
	writeMsg(s1, message.RoleUser, "First user message in session 1000")
	writeMsg(s1, message.RoleAssistant, "Hi")
	writeMsg(s2, message.RoleUser, "Second session first message")
	if err := SaveSessionMeta(s2, SessionMeta{ForkedFrom: "1000"}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	list, err = ListSessions(sessionsDir, "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	// Newest first (2000 > 1000 by name)
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
	if list[0].ID != "2000" || list[1].ID != "1000" {
		t.Errorf("order: got %q, %q; want 2000, 1000", list[0].ID, list[1].ID)
	}
	if list[0].FirstUserMessage != "" {
		t.Errorf("session 2000 FirstUserMessage = %q, want empty before detail load", list[0].FirstUserMessage)
	}
	if list[0].ForkedFrom != "1000" {
		t.Errorf("session 2000 ForkedFrom = %q, want 1000", list[0].ForkedFrom)
	}
	if list[1].FirstUserMessage != "" {
		t.Errorf("session 1000 FirstUserMessage = %q, want empty before detail load", list[1].FirstUserMessage)
	}
	if list[0].MessageCount != UnknownMessageCount {
		t.Errorf("session 2000 MessageCount = %d, want %d before detail load", list[0].MessageCount, UnknownMessageCount)
	}
	if list[1].MessageCount != UnknownMessageCount {
		t.Errorf("session 1000 MessageCount = %d, want %d before detail load", list[1].MessageCount, UnknownMessageCount)
	}
	if count, err := CountSessionMessages(s2); err != nil || count != 1 {
		t.Fatalf("CountSessionMessages(2000) = %d, %v; want 1, nil", count, err)
	}
	if count, err := CountSessionMessages(s1); err != nil || count != 2 {
		t.Fatalf("CountSessionMessages(1000) = %d, %v; want 2, nil", count, err)
	}

	// Appending invalidates the (size, mtime) cache entry; a second list must
	// reflect the new count instead of the memoized one.
	writeMsg(s2, message.RoleAssistant, "Reply in session 2000")
	list, err = ListSessions(sessionsDir, "")
	if err != nil {
		t.Fatalf("ListSessions after append: %v", err)
	}
	if list[0].MessageCount != UnknownMessageCount {
		t.Errorf("session 2000 MessageCount after append = %d, want %d before detail reload", list[0].MessageCount, UnknownMessageCount)
	}
	if count, err := CountSessionMessages(s2); err != nil || count != 2 {
		t.Errorf("CountSessionMessages(2000) after append = %d, %v; want 2, nil", count, err)
	}

	// Exclude s2
	list, _ = ListSessions(sessionsDir, s2)
	if len(list) != 1 {
		t.Errorf("exclude 2000: got %d sessions, want 1", len(list))
	} else if list[0].ID != "1000" {
		t.Errorf("exclude 2000: got ID %q, want 1000", list[0].ID)
	}

	// SessionInfoForDir
	info := SessionInfoForDir(s1)
	if info == nil {
		t.Fatal("SessionInfoForDir(s1) = nil")
	}
	if info.ID != "1000" || info.FirstUserMessage != "First user message in session 1000" {
		t.Errorf("SessionInfoForDir: ID=%q FirstUserMessage=%q", info.ID, info.FirstUserMessage)
	}
	if forked := SessionInfoForDir(s2); forked == nil || forked.ForkedFrom != "1000" {
		t.Fatalf("SessionInfoForDir(s2) forkedFrom = %v, want 1000", forked)
	}
	if info := SessionInfoForDir(t.TempDir()); info != nil {
		t.Error("SessionInfoForDir(empty) should be nil")
	}
}

func TestLoadMessagesCountCacheOnlyProvidesCapacityHint(t *testing.T) {
	rm, dir := newTestManager(t)
	defer rm.Close()
	for i := range 3 {
		if err := rm.PersistMessage("main", message.Message{Role: message.RoleUser, Content: fmt.Sprintf("message-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, identity.MainSessionLogFilename)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	messageCountCache.Store(path, messageCountEntry{size: info.Size(), modTime: info.ModTime(), count: 1})

	messages, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(messages))
	}
}

func TestLoadMessagesSupportsRecordLargerThanReadBuffer(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()
	content := strings.Repeat("x", recoveryMaxReadBufferSize*2)
	if err := rm.PersistMessage("main", message.Message{Role: message.RoleUser, Content: content}); err != nil {
		t.Fatal(err)
	}

	messages, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("restored messages = %d, want 1", len(messages))
	}
	if messages[0].Content != content {
		t.Fatalf("restored content length = %d, want %d", len(messages[0].Content), len(content))
	}
}
