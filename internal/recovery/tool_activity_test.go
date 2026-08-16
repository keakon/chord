package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestToolActivityAppendAndLoad(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()

	started := time.Now().UnixNano()
	for _, rec := range []ToolActivityRecord{
		{CallID: "call-1", AgentID: "main", TurnID: 1, Tool: "shell", State: ToolActivityStateStarted, TS: started},
		{CallID: "call-2", AgentID: "agent-a", TurnID: 2, Tool: "write", State: ToolActivityStateStarted, TS: started + 1},
	} {
		if err := rm.AppendToolActivity(rec); err != nil {
			t.Fatalf("AppendToolActivity(%+v): %v", rec, err)
		}
	}

	got, err := rm.LoadToolActivity()
	if err != nil {
		t.Fatalf("LoadToolActivity: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("started set size = %d, want 2", len(got))
	}
	for _, key := range []ToolActivityKey{{AgentID: "main", CallID: "call-1"}, {AgentID: "agent-a", CallID: "call-2"}} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing started key %+v in %#v", key, got)
		}
	}
}

func TestToolActivityLoadMissingJournalReturnsNil(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()

	got, err := rm.LoadToolActivity()
	if err != nil {
		t.Fatalf("LoadToolActivity: %v", err)
	}
	if got != nil {
		t.Fatalf("missing journal returned non-nil set: %#v", got)
	}
}

func TestToolActivityLoadRejectsTruncatedFinalLine(t *testing.T) {
	rm, dir := newTestManager(t)
	if err := rm.AppendToolActivity(ToolActivityRecord{CallID: "call-1", AgentID: "main", Tool: "shell", State: ToolActivityStateStarted}); err != nil {
		t.Fatalf("AppendToolActivity: %v", err)
	}
	rm.Close()

	path := filepath.Join(dir, ToolActivityFilename)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	if _, err := f.WriteString(`{"call_id":"trunc`); err != nil {
		t.Fatalf("append truncated line: %v", err)
	}
	f.Close()

	rm2 := NewRecoveryManager(dir)
	defer rm2.Close()
	got, err := rm2.LoadToolActivity()
	if err == nil {
		t.Fatalf("LoadToolActivity returned partial evidence without an error: %#v", got)
	}
	if got != nil {
		t.Fatalf("LoadToolActivity returned partial started set: %#v", got)
	}
}

func TestToolActivityLoadRejectsCorruptMiddleRecord(t *testing.T) {
	rm, dir := newTestManager(t)
	if err := rm.AppendToolActivity(ToolActivityRecord{CallID: "call-1", AgentID: "main", Tool: "shell", State: ToolActivityStateStarted}); err != nil {
		t.Fatalf("AppendToolActivity: %v", err)
	}
	rm.Close()

	// Rebuild the journal with a corrupt line between two valid records.
	path := filepath.Join(dir, ToolActivityFilename)
	content := `{"call_id":"call-1","agent_id":"main","tool":"shell","state":"started","ts":1}
{not-json}
{"call_id":"call-2","agent_id":"agent-a","tool":"write","state":"started","ts":2}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("rewrite journal: %v", err)
	}

	rm2 := NewRecoveryManager(dir)
	defer rm2.Close()
	got, err := rm2.LoadToolActivity()
	if err == nil {
		t.Fatalf("LoadToolActivity returned partial evidence without an error: %#v", got)
	}
	if got != nil {
		t.Fatalf("LoadToolActivity returned partial started set: %#v", got)
	}
}

func TestToolActivityLoadRejectsIncompleteRecord(t *testing.T) {
	rm, dir := newTestManager(t)
	rm.Close()
	path := filepath.Join(dir, ToolActivityFilename)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	rm2 := NewRecoveryManager(dir)
	defer rm2.Close()
	got, err := rm2.LoadToolActivity()
	if err == nil || got != nil {
		t.Fatalf("LoadToolActivity = %#v, %v; want no classification evidence", got, err)
	}
}

func TestToolActivityLoadFailsClosedWhenScanStopsEarly(t *testing.T) {
	rm, dir := newTestManager(t)
	if err := rm.AppendToolActivity(ToolActivityRecord{CallID: "call-1", AgentID: "main", Tool: "shell", State: ToolActivityStateStarted}); err != nil {
		t.Fatalf("AppendToolActivity: %v", err)
	}
	rm.Close()

	path := filepath.Join(dir, ToolActivityFilename)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	if _, err := f.WriteString(strings.Repeat("x", recoveryMaxJournalLineSize+1) + "\n"); err != nil {
		f.Close()
		t.Fatalf("append oversized record: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}

	rm2 := NewRecoveryManager(dir)
	defer rm2.Close()
	got, err := rm2.LoadToolActivity()
	if err == nil {
		t.Fatalf("LoadToolActivity returned partial evidence without an error: %#v", got)
	}
	if got != nil {
		t.Fatalf("LoadToolActivity returned partial started set on scan failure: %#v", got)
	}
}

func TestToolActivityAppendAfterCloseReturnsError(t *testing.T) {
	rm, _ := newTestManager(t)
	rm.Close()
	if err := rm.AppendToolActivity(ToolActivityRecord{CallID: "call-1", AgentID: "main", Tool: "shell", State: ToolActivityStateStarted}); err == nil {
		t.Fatal("AppendToolActivity after Close returned nil, want error")
	}
}

func TestToolActivityRetriesDirectorySyncAfterFailure(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()
	var syncCalls int
	rm.syncJournalDir = func() error {
		syncCalls++
		if syncCalls == 1 {
			return fmt.Errorf("temporary directory sync failure")
		}
		return nil
	}
	record := ToolActivityRecord{CallID: "call-1", AgentID: "main", Tool: "write", State: ToolActivityStateStarted}
	if err := rm.AppendToolActivity(record); err == nil {
		t.Fatal("first AppendToolActivity succeeded despite directory sync failure")
	}
	if err := rm.AppendToolActivity(record); err != nil {
		t.Fatalf("second AppendToolActivity: %v", err)
	}
	if syncCalls != 2 {
		t.Fatalf("directory sync calls = %d, want 2", syncCalls)
	}
}

func TestToolActivityConcurrentWithMessageWrites(t *testing.T) {
	rm, _ := newTestManager(t)
	defer rm.Close()

	const n = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range n {
			rec := ToolActivityRecord{CallID: fmt.Sprintf("call-%d", i), AgentID: "main", Tool: "shell", State: ToolActivityStateStarted}
			if err := rm.AppendToolActivity(rec); err != nil {
				t.Errorf("AppendToolActivity: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range n {
			if err := rm.PersistMessage("main", message.Message{Role: "tool", ToolCallID: "msg-call", Content: "ok"}); err != nil {
				t.Errorf("PersistMessage: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	got, err := rm.LoadToolActivity()
	if err != nil {
		t.Fatalf("LoadToolActivity: %v", err)
	}
	if len(got) != n {
		t.Fatalf("started set size = %d, want %d unique call ids", len(got), n)
	}
	msgs, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != n {
		t.Fatalf("message count = %d, want %d", len(msgs), n)
	}
}
