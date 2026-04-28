package recovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
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

func TestSaveSnapshotWritesCompactJSON(t *testing.T) {
	rm, dir := newTestManager(t)
	defer rm.Close()

	snap := &SessionSnapshot{
		Todos:      []TodoState{{ID: "1", Status: "pending", Content: "test"}},
		ModelName:  "test-model",
		ActiveRole: "planner",
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

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < msgsPerGoroutine; j++ {
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
		ModelName:  "claude-opus-4.7",
		ActiveRole: "planner",
		CreatedAt:  now,
	}

	if err := rm.SaveSnapshot(snap); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
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

	writeMsg := func(dir, role, content string) {
		f, _ := os.OpenFile(filepath.Join(dir, "main.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		data, _ := json.Marshal(message.Message{Role: role, Content: content})
		f.Write(append(data, '\n'))
		f.Close()
	}
	writeMsg(s1, "user", "First user message in session 1000")
	writeMsg(s1, "assistant", "Hi")
	writeMsg(s2, "user", "Second session first message")
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
	if list[0].FirstUserMessage != "Second session first message" {
		t.Errorf("session 2000 FirstUserMessage = %q", list[0].FirstUserMessage)
	}
	if list[0].ForkedFrom != "1000" {
		t.Errorf("session 2000 ForkedFrom = %q, want 1000", list[0].ForkedFrom)
	}
	if list[1].FirstUserMessage != "First user message in session 1000" {
		t.Errorf("session 1000 FirstUserMessage = %q", list[1].FirstUserMessage)
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
