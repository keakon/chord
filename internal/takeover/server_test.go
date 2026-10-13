package takeover

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/recovery"
)

func testStateDir(t *testing.T) string {
	t.Helper()
	if !Supported() {
		t.Skip("takeover requires macOS or Linux")
	}
	dir, err := os.MkdirTemp("/tmp", "ct-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
func testServer(t *testing.T) (*Server, Client, *atomic.Int32) {
	t.Helper()
	dir := testStateDir(t)
	workspace, err := CanonicalWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	record := Record{Protocol: ProtocolVersion, RuntimeID: rand.Text(), SessionID: "session-1", Workspace: workspace, OwnerKind: OwnerTUI}
	var mu sync.Mutex
	var status recovery.SessionPauseStatus
	starts := new(atomic.Int32)
	s, err := StartServer(ServerOptions{StateDir: dir, Record: record,
		Start: func(ctx context.Context, id recovery.TransferIdentity) (recovery.SessionPauseStatus, error) {
			starts.Add(1)
			mu.Lock()
			defer mu.Unlock()
			status = recovery.SessionPauseStatus{TransferIdentity: id, State: recovery.SessionPauseDraining}
			return status, nil
		},
		Status: func(id string) (recovery.SessionPauseStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			if status.TransferID != id {
				return recovery.SessionPauseStatus{}, protocolError(CodeStatusUnknown, "unknown transfer")
			}
			return status, nil
		},
		RetrySave: func(ctx context.Context, id string) error {
			mu.Lock()
			defer mu.Unlock()
			if status.TransferID != id {
				return protocolError(CodeStatusUnknown, "unknown transfer")
			}
			status.State = recovery.SessionPauseSaved
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, Client{StateDir: dir, Record: s.Record()}, starts
}
func testTransfer(record Record) recovery.TransferIdentity {
	return recovery.TransferIdentity{TransferID: "transfer-1", SessionID: record.SessionID, SourceRuntimeID: record.RuntimeID, SourceSessionEpoch: record.SessionEpoch, Workspace: record.Workspace, TargetKind: OwnerGateway, TargetInstanceID: "target-1"}
}
func errorCode(err error) string {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return ""
}

func TestServerAuthenticatesSourceAndStatus(t *testing.T) {
	s, client, starts := testServer(t)
	record, err := Find(client.StateDir, client.Record.SessionID)
	if err != nil || record != client.Record {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	status, err := client.Start(t.Context(), testTransfer(record))
	if err != nil || status.State != recovery.SessionPauseDraining {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if status, err = client.Status(t.Context(), "transfer-1"); err != nil || status.TransferID != "transfer-1" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if status, err = client.RetrySave(t.Context(), "transfer-1"); err != nil || status.State != recovery.SessionPauseSaved {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if starts.Load() != 1 {
		t.Fatalf("requests=%d", starts.Load())
	}
	for _, path := range []string{keyPath(client.StateDir, record.RuntimeID), recordPath(client.StateDir, record.RuntimeID), record.SocketPath} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("path=%s info=%v err=%v", path, info, err)
		}
	}
	data, err := os.ReadFile(recordPath(client.StateDir, record.RuntimeID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), s.secret) {
		t.Fatal("secret leaked into discovery record")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(client.StateDir, record.SessionID); errorCode(err) != CodeStatusUnknown {
		t.Fatalf("closed source returned %v", err)
	}
}

func rawRequest(t *testing.T, client Client, req request) response {
	t.Helper()
	conn, err := net.DialTimeout("unix", client.Record.SocketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if err := writeFrame(conn, req); err != nil {
		t.Fatal(err)
	}
	var reply response
	if err := readFrame(conn, &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}
func TestServerRejectsAuthenticationProtocolAndIdentity(t *testing.T) {
	s, client, starts := testServer(t)
	base := request{Protocol: ProtocolVersion, Secret: s.secret, Source: client.Record, Action: ActionStart, Identity: testTransfer(client.Record)}
	for _, tc := range []struct {
		name, code string
		edit       func(*request)
	}{
		{"secret", CodeInvalidRequest, func(r *request) { r.Secret = "wrong" }},
		{"protocol", CodeInvalidRequest, func(r *request) { r.Protocol++ }},
		{"epoch", CodeStaleSource, func(r *request) { r.Source.SessionEpoch++ }},
		{"workspace", CodeWrongWorkspace, func(r *request) { r.Source.Workspace += "/other" }},
		{"owner", CodeWrongOwnerKind, func(r *request) { r.Source.OwnerKind = OwnerGateway }},
		{"runtime", CodeStaleSource, func(r *request) { r.Source.RuntimeID = "other-runtime" }},
		{"unknown action", CodeInvalidRequest, func(r *request) { r.Action = "resume" }},
		{"same owner", CodeWrongOwnerKind, func(r *request) { r.Identity.TargetKind = OwnerTUI }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.edit(&req)
			reply := rawRequest(t, client, req)
			if reply.Error == nil || reply.Error.Code != tc.code {
				t.Fatalf("reply=%+v", reply)
			}
		})
	}
	if starts.Load() != 0 {
		t.Fatal("rejected request invoked callback")
	}
}

func TestServerUpdateRecordKeepsListenerAndRejectsStaleBinding(t *testing.T) {
	s, client, starts := testServer(t)
	next := client.Record
	next.SessionID = "session-2"
	next.SessionEpoch++
	listener := s.listener
	if err := s.UpdateRecord(next); err != nil {
		t.Fatal(err)
	}
	if s.listener != listener {
		t.Fatal("session switch replaced listener")
	}
	if _, err := client.Start(t.Context(), testTransfer(client.Record)); errorCode(err) != CodeStaleSource {
		t.Fatalf("old binding error=%v", err)
	}
	if starts.Load() != 0 {
		t.Fatal("stale binding invoked callback")
	}
	record, err := Find(client.StateDir, next.SessionID)
	if err != nil || record != next {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	client.Record = record
	if _, err := client.Start(t.Context(), testTransfer(record)); err != nil {
		t.Fatal(err)
	}
}

func TestServerBoundsBlockersAndSupportsPages(t *testing.T) {
	s, client, _ := testServer(t)
	id := testTransfer(client.Record)
	var blockers []recovery.SessionPauseBlocker
	for range 130 {
		blockers = append(blockers, recovery.SessionPauseBlocker{OwnerTaskID: "task", Kind: "tool", Description: strings.Repeat("x", 400)})
	}
	s.mu.Lock()
	s.options.Status = func(string) (recovery.SessionPauseStatus, error) {
		return recovery.SessionPauseStatus{TransferIdentity: id, State: recovery.SessionPauseDraining, Blockers: blockers, BlockerCount: 130}, nil
	}
	s.mu.Unlock()
	page, err := client.StatusPage(t.Context(), id.TransferID, 0)
	if err != nil || len(page.Blockers) != 64 || page.BlockerCount != 130 || page.NextOffset != 64 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	for _, b := range page.Blockers {
		if len(b.Description) > 256 {
			t.Fatal("unbounded blocker description")
		}
	}
	page, err = client.StatusPage(t.Context(), id.TransferID, 128)
	if err != nil || len(page.Blockers) != 2 || page.NextOffset != 0 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	data, err := json.Marshal(page)
	if err != nil || len(data) > MaxFrameBytes {
		t.Fatal("status exceeded frame limit")
	}
}

func waitConnections(t *testing.T, s *Server, want int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		got := len(s.conns)
		s.mu.Unlock()
		if got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("connections=%d want=%d", got, want)
		case <-tick.C:
		}
	}
}
func TestServerLimitsConcurrentWorkersAndReleasesCapacity(t *testing.T) {
	s, client, _ := testServer(t)
	var conns []net.Conn
	for range MaxWorkers {
		conn, err := net.Dial("unix", client.Record.SocketPath)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		defer conn.Close()
	}
	waitConnections(t, s, MaxWorkers)
	extra, err := net.Dial("unix", client.Record.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	extra.SetDeadline(time.Now().Add(time.Second))
	if n, err := extra.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("fifth connection admitted n=%d err=%v", n, err)
	}
	for _, conn := range conns {
		conn.Close()
	}
	waitConnections(t, s, 0)
	if _, err := client.Start(t.Context(), testTransfer(client.Record)); err != nil {
		t.Fatal(err)
	}
}

func TestClientCancellationDoesNotCancelAcceptedTransfer(t *testing.T) {
	s, client, _ := testServer(t)
	accepted := make(chan struct{})
	continueWork := make(chan struct{})
	done := make(chan struct{})
	s.mu.Lock()
	s.options.Start = func(ctx context.Context, id recovery.TransferIdentity) (recovery.SessionPauseStatus, error) {
		close(accepted)
		go func() { <-continueWork; close(done) }()
		return recovery.SessionPauseStatus{TransferIdentity: id, State: recovery.SessionPauseDraining}, nil
	}
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	if _, err := client.Start(ctx, testTransfer(client.Record)); err != nil {
		t.Fatal(err)
	}
	<-accepted
	cancel()
	close(continueWork)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer cancelled source lifecycle")
	}
}

func TestServerRejectsLongSocketAndInsecureDirectory(t *testing.T) {
	dir := testStateDir(t)
	record := Record{Protocol: ProtocolVersion, RuntimeID: rand.Text(), SessionID: "session", OwnerKind: OwnerTUI, Workspace: dir}
	if _, err := normalizeRecord(filepath.Join(dir, strings.Repeat("x", 100)), record); errorCode(err) != CodeInvalidRequest {
		t.Fatalf("long path err=%v", err)
	}
	if err := os.Mkdir(discoveryDir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePrivate(dir, discoveryDir(dir), privatefs.DirMode); err == nil {
		t.Fatal("insecure discovery directory was trusted")
	}
}

func TestServerCloseWaitsForAlreadyAdmittedCallback(t *testing.T) {
	s, client, _ := testServer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan error, 1)
	s.mu.Lock()
	s.options.Start = func(context.Context, recovery.TransferIdentity) (recovery.SessionPauseStatus, error) {
		close(entered)
		<-release
		return recovery.SessionPauseStatus{}, nil
	}
	s.mu.Unlock()
	called := make(chan struct{})
	go func() { client.Start(t.Context(), testTransfer(client.Record)); close(called) }()
	<-entered
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("close returned before callback completed: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not finish")
	}
	<-called
}

func TestServerListenerStartStopDoesNotLeakDescriptors(t *testing.T) {
	s, client, _ := testServer(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	countFD := func() int {
		for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
			if entries, err := os.ReadDir(dir); err == nil {
				return len(entries)
			}
		}
		t.Skip("descriptor directory unavailable")
		return 0
	}
	before := countFD()
	for range 100 {
		record := client.Record
		record.RuntimeID = rand.Text()
		record.SocketPath = ""
		options := s.options
		options.Record = record
		next, err := StartServer(options)
		if err != nil {
			t.Fatal(err)
		}
		if err := next.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if after := countFD(); after > before+1 {
		t.Fatalf("FD leak: before=%d after=%d", before, after)
	}
}

func TestPeerKernelCredentialsRejectWrongUID(t *testing.T) {
	_, client, _ := testServer(t)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: client.Record.SocketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := checkPeer(conn, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	if err := checkPeer(conn, uint32(os.Geteuid())+1); err == nil {
		t.Fatal("foreign UID was trusted")
	}
}

func TestFindIgnoresCrashedRecordButRejectsMultipleLiveSources(t *testing.T) {
	s, client, _ := testServer(t)
	stale := client.Record
	stale.RuntimeID = rand.Text()
	stale.SocketPath = ""
	var err error
	stale, err = normalizeRecord(client.StateDir, stale)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeAtomic(client.StateDir, recordPath(client.StateDir, stale.RuntimeID), data); err != nil {
		t.Fatal(err)
	}
	if record, err := Find(client.StateDir, stale.SessionID); err != nil || record != client.Record {
		t.Fatalf("live record=%+v err=%v", record, err)
	}
	options := s.options
	options.Record = stale
	if err := os.Remove(recordPath(client.StateDir, stale.RuntimeID)); err != nil {
		t.Fatal(err)
	}
	other, err := StartServer(options)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := Find(client.StateDir, stale.SessionID); errorCode(err) != CodeStaleSource {
		t.Fatalf("multiple live sources error=%v", err)
	}
}

func TestDiscoveryRejectsSymlinkAndTrailingJSON(t *testing.T) {
	_, client, _ := testServer(t)
	path := recordPath(client.StateDir, client.Record.RuntimeID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(client.StateDir, "linked-record")
	if err := os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(discoveryDir(client.StateDir), "linked.json")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRecord(client.StateDir, link); err == nil {
		t.Fatal("symlink discovery record accepted")
	}
	if err := os.WriteFile(other, append(data, []byte(" {}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRecord(client.StateDir, other); errorCode(err) != CodeInvalidRequest {
		t.Fatalf("trailing record error=%v", err)
	}
}

func TestSourceObservationDeadlineKeepsStatusUnknown(t *testing.T) {
	s, client, _ := testServer(t)
	s.mu.Lock()
	s.options.Start = func(_ context.Context, id recovery.TransferIdentity) (recovery.SessionPauseStatus, error) {
		return recovery.SessionPauseStatus{TransferIdentity: id, State: recovery.SessionPauseDraining}, context.DeadlineExceeded
	}
	s.mu.Unlock()
	status, err := client.Start(t.Context(), testTransfer(client.Record))
	if errorCode(err) != CodeStatusUnknown || !status.WaitExpired || status.State != recovery.SessionPauseDraining {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestInterruptedStartResponseIsUnknownAndQueryable(t *testing.T) {
	s, client, _ := testServer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	s.mu.Lock()
	original := s.options.Start
	s.options.Start = func(ctx context.Context, id recovery.TransferIdentity) (recovery.SessionPauseStatus, error) {
		status, err := original(ctx, id)
		close(entered)
		<-release
		return status, err
	}
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := client.Start(ctx, testTransfer(client.Record)); result <- err }()
	<-entered
	cancel()
	select {
	case err := <-result:
		if errorCode(err) != CodeStatusUnknown || !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupted observation error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client cancellation did not finish")
	}
	close(release)
	if status, err := client.Status(t.Context(), "transfer-1"); err != nil || status.State != recovery.SessionPauseDraining {
		t.Fatalf("accepted transfer disappeared status=%+v err=%v", status, err)
	}
}
