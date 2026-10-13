package inputhistory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func historyRecord(id, body string) Record {
	return Record{ID: id, SubmittedAt: time.Now().UTC(), ProjectKey: "project", ProjectLabel: "sample", Kind: Prompt, TextParts: []TextPart{{Text, body}}}
}
func historyStore(t *testing.T, dir string, o Options) *Store {
	t.Helper()
	s := New(dir, o)
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}
func TestInputHistoryPersistenceAndClear(t *testing.T) {
	dir := t.TempDir()
	s := historyStore(t, dir, DefaultOptions())
	r := historyRecord("one", "  original\ntext  ")
	if a := s.TryRecord(r); a.Outcome != Queued {
		t.Fatal(a)
	}
	snap, _ := s.Snapshot(context.Background(), false)
	if len(snap.Entries) != 1 || snap.Entries[0].Record.TextParts[0].Text != r.TextParts[0].Text {
		t.Fatal(snap)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader := historyStore(t, dir, DefaultOptions())
	snap, err := reader.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) != 1 {
		t.Fatal(snap, err)
	}
	version := snap.Version
	snap, err = reader.Snapshot(context.Background(), true)
	if err != nil || snap.Version != version {
		t.Fatal("unchanged refresh changed version", snap, err)
	}
	ticket, err := reader.TryClear()
	if err != nil {
		t.Fatal(err)
	}
	if result := <-ticket.Result; result.Err != nil {
		t.Fatal(result)
	}
	snap, err = reader.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) != 0 {
		t.Fatal(snap, err)
	}
}
func TestInputHistoryReaderRecovery(t *testing.T) {
	dir := t.TempDir()
	encode := func(r Record) []byte { b, _ := json.Marshal(r); return b }
	data := append(encode(historyRecord("one", strings.Repeat("x", 70<<10))), '\n')
	data = append(data, []byte("{broken}\n")...)
	data = append(data, []byte(strings.Repeat("x", MaxRecordBytes+2)+"\n")...)
	data = append(data, encode(historyRecord("two", "final"))...)
	if err := os.WriteFile(filepath.Join(dir, "input-history.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s := historyStore(t, dir, DefaultOptions())
	snap, err := s.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) != 2 {
		t.Fatal(len(snap.Entries), err)
	}
	s.TryRecord(historyRecord("three", "after tail"))
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := historyStore(t, dir, DefaultOptions())
	snap, err = next.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) != 3 {
		t.Fatal(len(snap.Entries), err)
	}
}
func TestInputHistoryRetentionAndExternalClear(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions()
	opts.MaxEntries = 3
	s := historyStore(t, dir, opts)
	for i := range 5 {
		s.TryRecord(historyRecord(fmt.Sprint(i), "same body"))
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader := historyStore(t, dir, opts)
	snap, err := reader.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) > 3 || snap.Entries[len(snap.Entries)-1].Record.ID != "4" {
		t.Fatal(snap, err)
	}
	other := historyStore(t, dir, opts)
	ticket, err := other.TryClear()
	if err != nil {
		t.Fatal(err)
	}
	if result := <-ticket.Result; result.Err != nil {
		t.Fatal(result)
	}
	snap, err = reader.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) != 0 {
		t.Fatal("synced entry resurrected", snap, err)
	}
}
func TestInputHistoryDisabledAndEscapedLimit(t *testing.T) {
	opts := DefaultOptions()
	opts.Enabled = false
	s := historyStore(t, t.TempDir(), opts)
	if a := s.TryRecord(historyRecord("one", "text")); a.Outcome != Disabled {
		t.Fatal(a)
	}
	opts.Enabled = true
	opts.MaxBytes = 512
	s = historyStore(t, t.TempDir(), opts)
	s.TryRecord(historyRecord("two", strings.Repeat("<", 100)))
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := s.Status(); status.Reason != TooLarge {
		t.Fatal(status)
	}
	snap, _ := s.Snapshot(context.Background(), false)
	if len(snap.Entries) != 0 {
		t.Fatal(snap)
	}
}
func TestInputHistoryProcess(t *testing.T) {
	if dir := os.Getenv("CHORD_INPUT_HISTORY_HELPER"); dir != "" {
		s := New(dir, DefaultOptions())
		for i := range 15 {
			s.TryRecord(historyRecord(fmt.Sprintf("%s-%d", os.Getenv("CHORD_INPUT_HISTORY_PREFIX"), i), "text"))
		}
		if err := s.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	var commands []*exec.Cmd
	for _, prefix := range []string{"a", "b"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestInputHistoryProcess$")
		cmd.Env = append(os.Environ(), "CHORD_INPUT_HISTORY_HELPER="+dir, "CHORD_INPUT_HISTORY_PREFIX="+prefix)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	s := historyStore(t, dir, DefaultOptions())
	snap, err := s.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) != 30 {
		t.Fatal(len(snap.Entries), err)
	}
}
func TestInputHistoryLockCancellation(t *testing.T) {
	s := historyStore(t, t.TempDir(), DefaultOptions())
	f, err := s.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release(f)
	s.TryRecord(historyRecord("one", "text"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.Close(ctx); err == nil {
		t.Fatal("expected cancelled close")
	}
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not exit")
	}
}
func TestInputHistorySearch(t *testing.T) {
	records := []Record{historyRecord("one", "Alpha beta"), historyRecord("two", "alpha something beta"), historyRecord("three", "a_l_p_h_a"), historyRecord("four", "你好世界")}
	var snapshot Snapshot
	for _, r := range records {
		snapshot.Entries = append(snapshot.Entries, Entry{Record: r})
	}
	result, err := Match(context.Background(), snapshot, Query{Text: "ALPHA", ProjectKey: "project"})
	if err != nil || result.TotalMatched != 3 || result.Hits[0].ID != "two" || result.Hits[2].Rank != 2 {
		t.Fatal(result, err)
	}
	result, err = Match(context.Background(), snapshot, Query{Text: "你好", ProjectKey: "project"})
	if err != nil || len(result.Hits) != 1 {
		t.Fatal(result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Match(ctx, snapshot, Query{}); err == nil {
		t.Fatal("expected cancellation")
	}
}
func TestInputHistoryPreviewBounded(t *testing.T) {
	r := historyRecord("one", strings.Repeat("a", MaxRecordBytes-512))
	page, err := Preview(context.Background(), r, PreviewRequest{Width: 80, Lines: 8})
	if err != nil || len(page.Text) > 650 || !page.HasAfter {
		t.Fatal(page, err)
	}
	page, err = Preview(context.Background(), r, PreviewRequest{Offset: len(r.TextParts[0].Text) - 2, Width: 80, Lines: 8})
	if err != nil || page.HasAfter || page.Text != "aa" {
		t.Fatal(page, err)
	}
}
func BenchmarkInputHistoryMatch(b *testing.B) {
	snap := Snapshot{cache: &searchCache{maxBytes: 32 << 20}}
	for i := range 10000 {
		snap.Entries = append(snap.Entries, Entry{Record: historyRecord(fmt.Sprint(i), strings.Repeat("sample text ", 270))})
	}
	_, _ = Match(context.Background(), snap, Query{Text: "sample", ProjectKey: "project"})
	b.ReportAllocs()
	var samples []int64
	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		_, _ = Match(context.Background(), snap, Query{Text: "sample", ProjectKey: "project"})
		if len(samples) < 10000 {
			samples = append(samples, time.Since(start).Nanoseconds())
		}
	}
	b.StopTimer()
	reportInputHistoryP95(b, samples)
}

func TestInputHistoryUnknownWriteAndClearFailure(t *testing.T) {
	dir := t.TempDir()
	s := newStore(dir, DefaultOptions(), fileHooks{write: func(f *os.File, b []byte) (int, error) {
		n, err := f.Write(b)
		if err != nil {
			return n, err
		}
		return n, fmt.Errorf("injected write outcome")
	}})
	s.TryRecord(historyRecord("one", "text"))
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Status().State != Unknown {
		t.Fatal(s.Status())
	}
	before, _ := s.Snapshot(context.Background(), false)
	if len(before.Entries) != 1 || before.Entries[0].State != Unknown {
		t.Fatal(before)
	}
	reader := historyStore(t, dir, DefaultOptions())
	snap, err := reader.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) != 1 {
		t.Fatal(snap, err)
	}
	failed := newStore(dir, DefaultOptions(), fileHooks{replace: func(string, []byte) error { return fmt.Errorf("injected replacement failure") }})
	defer failed.Close(context.Background())
	snap, err = failed.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) != 1 {
		t.Fatal(snap, err)
	}
	ticket, err := failed.TryClear()
	if err != nil {
		t.Fatal(err)
	}
	if result := <-ticket.Result; result.Err == nil {
		t.Fatal("failed clear reported success")
	}
	snap, _ = failed.Snapshot(context.Background(), false)
	if len(snap.Entries) != 1 {
		t.Fatal("failed clear discarded history")
	}
}
func TestInputHistoryClearBarrier(t *testing.T) {
	dir := t.TempDir()
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s := newStore(dir, DefaultOptions(), fileHooks{write: func(f *os.File, b []byte) (int, error) {
		once.Do(func() { close(entered); <-proceed })
		return f.Write(b)
	}})
	defer s.Close(context.Background())
	s.TryRecord(historyRecord("before", "text"))
	<-entered
	ticket, err := s.TryClear()
	if err != nil {
		t.Fatal(err)
	}
	s.TryRecord(historyRecord("after", "text"))
	close(proceed)
	if result := <-ticket.Result; result.Err != nil {
		t.Fatal(result)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader := historyStore(t, dir, DefaultOptions())
	snap, err := reader.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) != 1 || snap.Entries[0].Record.ID != "after" {
		t.Fatal(snap, err)
	}
}
func TestInputHistoryQueueBoundAndLocalRetention(t *testing.T) {
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	opts := DefaultOptions()
	opts.MaxEntries = 4
	s := newStore(t.TempDir(), opts, fileHooks{write: func(f *os.File, b []byte) (int, error) {
		once.Do(func() { close(entered); <-proceed })
		return f.Write(b)
	}})
	s.TryRecord(historyRecord("first", "text"))
	<-entered
	var last Admission
	for i := range 140 {
		last = s.TryRecord(historyRecord(fmt.Sprint(i), "text"))
	}
	if last.Outcome != LocalOnly || !last.Retained {
		t.Fatal(last)
	}
	snap, _ := s.Snapshot(context.Background(), false)
	if len(snap.Entries) > 4 {
		t.Fatal("local history grew without bound")
	}
	ticket, err := s.TryClear()
	if err != nil {
		t.Fatal("clear control slot blocked", err)
	}
	close(proceed)
	if result := <-ticket.Result; result.Err != nil {
		t.Fatal(result)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestInputHistorySnapshotImmutable(t *testing.T) {
	s := historyStore(t, t.TempDir(), DefaultOptions())
	r := historyRecord("one", "original")
	s.TryRecord(r)
	r.TextParts[0].Text = "changed"
	snap, _ := s.Snapshot(context.Background(), false)
	snap.Entries[0].Record.TextParts[0].Text = "changed snapshot"
	next, _ := s.Snapshot(context.Background(), false)
	if next.Entries[0].Record.TextParts[0].Text != "original" {
		t.Fatal(next)
	}
}

func TestInputHistoryCloseSettlesQueuedClear(t *testing.T) {
	entered, releaseWrite := make(chan struct{}), make(chan struct{})
	s := newStore(t.TempDir(), DefaultOptions(), fileHooks{write: func(f *os.File, b []byte) (int, error) { close(entered); <-releaseWrite; return f.Write(b) }})
	s.TryRecord(historyRecord("one", "text"))
	<-entered
	ticket, err := s.TryClear()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.Close(ctx); err == nil {
		t.Fatal("close should report unconfirmed write")
	}
	select {
	case result := <-ticket.Result:
		if result.Outcome != Cancelled {
			t.Fatal(result)
		}
	default:
		t.Fatal("queued clear ticket was not settled")
	}
	close(releaseWrite)
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInputHistoryTopKAndBackwardPreview(t *testing.T) {
	var snap Snapshot
	for i := range 140 {
		snap.Entries = append(snap.Entries, Entry{Record: historyRecord(fmt.Sprint(i), "sample text")})
	}
	result, err := Match(context.Background(), snap, Query{Text: "sample", ProjectKey: "project"})
	if err != nil || len(result.Hits) != 100 || result.TotalMatched != 140 || result.Hits[0].ID != "139" || result.Hits[99].ID != "40" {
		t.Fatal(result, err)
	}
	r := historyRecord("page", strings.Repeat("sample ", 10000))
	first, err := Preview(context.Background(), r, PreviewRequest{Width: 80, Lines: 8})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Preview(context.Background(), r, PreviewRequest{Offset: first.End, Width: 80, Lines: 8})
	if err != nil {
		t.Fatal(err)
	}
	back, err := Preview(context.Background(), r, PreviewRequest{Offset: second.Start, Direction: -1, Width: 80, Lines: 8})
	if err != nil || back.Start != first.Start || back.End != first.End {
		t.Fatal(first, second, back, err)
	}
}
