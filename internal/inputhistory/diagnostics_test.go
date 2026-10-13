package inputhistory

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/logtest"
)

func TestMain(m *testing.M) {
	log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	os.Exit(m.Run())
}

func TestInputHistoryRecoveryDiagnosticsExcludeContent(t *testing.T) {
	var output bytes.Buffer
	log.SetDefaultLogger(logtest.NewLogger(&output, golog.WarnLevel))
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	dir := t.TempDir()
	row, _ := json.Marshal(historyRecord("one", "sample private body"))
	data := append(append(append([]byte("invalid private body\n"), row...), '\n'), row...)
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "input-history.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s := historyStore(t, dir, DefaultOptions())
	for range 2 {
		if _, err := s.Snapshot(context.Background(), true); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if strings.Count(text, "skipped records") != 1 || !strings.Contains(text, "invalid=1 duplicate_ids=1") || strings.Contains(text, "private body") {
		t.Fatal("unexpected recovery diagnostic", text)
	}
}

func TestInputHistoryLocalBudgetAfterSettlement(t *testing.T) {
	s := historyStore(t, t.TempDir(), DefaultOptions())
	for i := range 3 {
		s.TryRecord(historyRecord(string(rune('a'+i)), "sample input"))
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.local) != 0 || s.localBytes != 0 {
		t.Fatal("settled inputs retained local budget", s.localBytes)
	}
}
