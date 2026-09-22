package acpagent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// fakeBackend is a Chord agent stand-in that lets a test drive turn events.
type fakeBackend struct {
	events chan agent.AgentEvent
	sent   chan struct{}

	mu       sync.Mutex
	messages [][]message.ContentPart
	cancels  int
	// sendHold pins the next send inside the hand-off to the backend: while it
	// is set, SendUserMessageWithParts closes sendEntered and then waits for
	// sendHold to be closed before it records the message.
	sendHold    chan struct{}
	sendEntered chan struct{}
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		events: make(chan agent.AgentEvent, 8),
		sent:   make(chan struct{}, 8),
	}
}

// holdNextSend makes the next SendUserMessageWithParts stop before it records
// the message: entered is closed when the hand-off reaches the backend, and
// release lets it finish.
func (f *fakeBackend) holdNextSend() (entered <-chan struct{}, release func()) {
	hold := make(chan struct{})
	enteredCh := make(chan struct{})
	f.mu.Lock()
	f.sendHold = hold
	f.sendEntered = enteredCh
	f.mu.Unlock()
	var once sync.Once
	return enteredCh, func() { once.Do(func() { close(hold) }) }
}

func (f *fakeBackend) SendUserMessageWithParts(parts []message.ContentPart) {
	f.mu.Lock()
	hold, entered := f.sendHold, f.sendEntered
	f.sendHold, f.sendEntered = nil, nil
	f.mu.Unlock()
	if hold != nil {
		close(entered)
		<-hold
	}
	f.mu.Lock()
	f.messages = append(f.messages, parts)
	f.mu.Unlock()
	f.sent <- struct{}{}
}

func (f *fakeBackend) Events() <-chan agent.AgentEvent { return f.events }

func (f *fakeBackend) CancelCurrentTurn() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	return true
}

func (f *fakeBackend) cancelCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancels
}

func (f *fakeBackend) lastMessage() []message.ContentPart {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messages) == 0 {
		return nil
	}
	return f.messages[len(f.messages)-1]
}

func (f *fakeBackend) messageCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages)
}

// sessionDirName is the Chord session directory name the test runtime reports;
// it is deliberately unlike the session id the server mints.
const sessionDirName = "20260922010101"

func newTestServer(t *testing.T) (*Server, *fakeBackend) {
	t.Helper()
	backend := newFakeBackend()
	sessionDir := filepath.Join(t.TempDir(), sessionDirName)
	server := New(Options{
		Version: "test",
		Bootstrap: func(cwd string) (*Runtime, error) {
			return &Runtime{Backend: backend, SessionDir: sessionDir}, nil
		},
	})
	t.Cleanup(func() { close(backend.events) })
	return server, backend
}

func newTestSession(t *testing.T, server *Server) acp.SessionId {
	t.Helper()
	resp, err := server.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	if resp.SessionId == "" {
		t.Fatal("NewSession returned an empty session id")
	}
	return resp.SessionId
}

// TestCloseSessionReportsSessionClosed covers what the entrypoint watches: a
// live session must not report itself closed, and closing it has to report that
// exactly once, however many closes race.
func TestCloseSessionReportsSessionClosed(t *testing.T) {
	server, _ := newTestServer(t)
	session := newTestSession(t, server)

	select {
	case <-server.SessionClosed():
		t.Fatal("a live session must not report itself closed")
	default:
	}

	if _, err := server.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: session}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	select {
	case <-server.SessionClosed():
	case <-time.After(time.Second):
		t.Fatal("CloseSession did not report the session closed")
	}

	// The session is terminal: it must not start another turn.
	if _, err := server.Prompt(context.Background(), acp.PromptRequest{
		SessionId: session,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hi")},
	}); err == nil {
		t.Error("a closed session must reject prompts")
	}

	// Concurrent closes must not close the channel twice, which would panic.
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, _ = server.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: session})
		})
	}
	wg.Wait()
	select {
	case <-server.SessionClosed():
	default:
		t.Error("the session stopped reporting itself closed")
	}
}

func TestInitializeAdvertisesImagePromptsOnly(t *testing.T) {
	server, _ := newTestServer(t)
	resp, err := server.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	if err != nil {
		t.Fatalf("Initialize returned error: %v", err)
	}
	if resp.ProtocolVersion != acp.ProtocolVersionNumber {
		t.Fatalf("ProtocolVersion = %v", resp.ProtocolVersion)
	}
	if resp.AgentInfo == nil || resp.AgentInfo.Name != "chord" {
		t.Fatalf("AgentInfo = %#v", resp.AgentInfo)
	}
	caps := resp.AgentCapabilities
	if !caps.PromptCapabilities.Image {
		t.Fatal("image prompts should be advertised")
	}
	if caps.LoadSession || caps.PromptCapabilities.Audio || caps.PromptCapabilities.EmbeddedContext {
		t.Fatalf("unexpected capabilities: %#v", caps)
	}
}

func TestNewSessionIsSingleShot(t *testing.T) {
	server, _ := newTestServer(t)
	newTestSession(t, server)

	_, err := server.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err == nil {
		t.Fatal("a second session/new must be rejected")
	}
}

func TestNewSessionRequiresReadableCwd(t *testing.T) {
	server, _ := newTestServer(t)
	if _, err := server.NewSession(context.Background(), acp.NewSessionRequest{}); err == nil {
		t.Fatal("an empty cwd must be rejected")
	}
	if _, err := server.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/definitely/missing/dir"}); err == nil {
		t.Fatal("a missing cwd must be rejected")
	}
}

// The ACP session id is the client's handle and stays opaque; the Chord session
// directory travels in the response metadata, because that is the id a later
// `chord resume` or a bug report needs.
func TestNewSessionMintsIdAndReportsChordSessionMetadata(t *testing.T) {
	server, _ := newTestServer(t)
	resp, err := server.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	if !strings.HasPrefix(string(resp.SessionId), "chord-") {
		t.Fatalf("SessionId = %q, want a minted id", resp.SessionId)
	}
	if string(resp.SessionId) == sessionDirName {
		t.Fatal("the ACP session id must not be the Chord session directory name")
	}
	chord, ok := resp.Meta["chord"].(map[string]any)
	if !ok {
		t.Fatalf("Meta = %#v, want a chord entry", resp.Meta)
	}
	if chord["sessionId"] != sessionDirName {
		t.Fatalf("Meta chord sessionId = %#v, want %q", chord["sessionId"], sessionDirName)
	}

	otherServer, _ := newTestServer(t)
	other, err := otherServer.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	if other.SessionId == resp.SessionId {
		t.Fatalf("both sessions got the id %q", resp.SessionId)
	}
}

// Client-scoped session fields are accepted and recorded, not honoured: Chord
// takes its MCP servers and roots from its own configuration, and the warning
// in the log is what makes the client's intent visible.
func TestNewSessionAcceptsClientScopedFields(t *testing.T) {
	server, _ := newTestServer(t)
	resp, err := server.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: t.TempDir(),
		McpServers: []acp.McpServer{
			{Stdio: &acp.McpServerStdio{Name: "client-mcp", Command: "/usr/bin/true"}},
			{Http: &acp.McpServerHttpInline{Name: "client-http", Url: "https://example.invalid/mcp"}},
		},
		AdditionalDirectories: []string{t.TempDir()},
	})
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	if resp.SessionId == "" {
		t.Fatal("NewSession returned an empty session id")
	}
}

func TestPromptSendsPartsAndEndsTurnOnGlobalIdle(t *testing.T) {
	server, backend := newTestServer(t)
	session := newTestSession(t, server)
	cwd := server.Cwd()
	if cwd == "" {
		t.Fatal("session cwd was not recorded")
	}

	go func() {
		<-backend.sent
		backend.events <- agent.StreamTextEvent{Text: "hi"}
		backend.events <- agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1}
		backend.events <- agent.GlobalIdleEvent{}
	}()

	resp, err := server.Prompt(context.Background(), acp.PromptRequest{
		SessionId: session,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
	})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	parts := backend.lastMessage()
	if len(parts) != 1 || parts[0].Type != message.ContentPartText || parts[0].Text != "hello" {
		t.Fatalf("sent parts = %#v", parts)
	}
}

func TestPromptReportsTurnError(t *testing.T) {
	server, backend := newTestServer(t)
	session := newTestSession(t, server)

	go func() {
		<-backend.sent
		backend.events <- agent.ErrorEvent{Err: errors.New("provider exhausted")}
		backend.events <- agent.GlobalIdleEvent{}
	}()

	_, err := server.Prompt(context.Background(), acp.PromptRequest{
		SessionId: session,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
	})
	if err == nil || !strings.Contains(err.Error(), "provider exhausted") {
		t.Fatalf("Prompt error = %v, want the turn error", err)
	}
}

func TestPromptCancelReportsCancelledOnce(t *testing.T) {
	server, backend := newTestServer(t)
	session := newTestSession(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-backend.sent
		backend.events <- agent.StreamTextEvent{Text: "working"}
		// session/cancel cancels the prompt context through the SDK and then
		// calls Cancel; both paths must abort the Chord turn exactly once.
		if err := server.Cancel(context.Background(), acp.CancelNotification{SessionId: session}); err != nil {
			t.Errorf("Cancel returned error: %v", err)
		}
		cancel()
		backend.events <- agent.GlobalIdleEvent{}
		backend.events <- agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1}
	}()

	resp, err := server.Prompt(ctx, acp.PromptRequest{
		SessionId: session,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
	})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if resp.StopReason != acp.StopReasonCancelled {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonCancelled)
	}
	if got := backend.cancelCount(); got != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 1", got)
	}
}

func TestPromptRejectsUnknownSessionAndClosedSession(t *testing.T) {
	server, _ := newTestServer(t)
	session := newTestSession(t, server)
	prompt := acp.PromptRequest{SessionId: "other", Prompt: []acp.ContentBlock{acp.TextBlock("hello")}}

	if _, err := server.Prompt(context.Background(), prompt); err == nil {
		t.Fatal("an unknown session id must be rejected")
	}

	if _, err := server.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: session}); err != nil {
		t.Fatalf("CloseSession returned error: %v", err)
	}
	prompt.SessionId = session
	if _, err := server.Prompt(context.Background(), prompt); err == nil {
		t.Fatal("a closed session must be rejected")
	}
}

func TestOptionalMethodsReportMethodNotFound(t *testing.T) {
	server, _ := newTestServer(t)
	ctx := context.Background()
	checks := []struct {
		name string
		call func() error
	}{
		{"authenticate", func() error { _, err := server.Authenticate(ctx, acp.AuthenticateRequest{}); return err }},
		{"logout", func() error { _, err := server.Logout(ctx, acp.LogoutRequest{}); return err }},
		{"list", func() error { _, err := server.ListSessions(ctx, acp.ListSessionsRequest{}); return err }},
		{"resume", func() error { _, err := server.ResumeSession(ctx, acp.ResumeSessionRequest{}); return err }},
		{"set_mode", func() error { _, err := server.SetSessionMode(ctx, acp.SetSessionModeRequest{}); return err }},
		{"set_config", func() error {
			_, err := server.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{})
			return err
		}},
	}
	for _, check := range checks {
		reqErr, ok := errors.AsType[*acp.RequestError](check.call())
		if !ok || reqErr.Code != -32601 {
			t.Fatalf("%s error = %v, want method not found", check.name, reqErr)
		}
	}
}

// A cancelled turn can report global idle before the request goroutine flushes
// its last buffered chunks. The prompt must then wait for the segment end that
// follows that flush, otherwise the tail arrives after the client was told the
// turn was over and can be attributed to the next one.
func TestPromptCancelWaitsForStreamDrain(t *testing.T) {
	server, backend := newTestServer(t)
	session := newTestSession(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	answered := make(chan acp.PromptResponse, 1)
	go func() {
		resp, err := server.Prompt(ctx, acp.PromptRequest{
			SessionId: session,
			Prompt:    []acp.ContentBlock{acp.TextBlock("count to five thousand")},
		})
		if err != nil {
			t.Errorf("Prompt returned error: %v", err)
		}
		answered <- resp
	}()

	<-backend.sent
	backend.events <- agent.StreamTextEvent{Text: "1\n2\n3\n"}
	if err := server.Cancel(context.Background(), acp.CancelNotification{SessionId: session}); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	cancel()
	backend.events <- agent.GlobalIdleEvent{}

	select {
	case resp := <-answered:
		t.Fatalf("Prompt answered %q before the final flush was drained", resp.StopReason)
	case <-time.After(50 * time.Millisecond):
	}

	backend.events <- agent.StreamTextEvent{Text: "4\n5\n"}
	backend.events <- agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1}

	select {
	case resp := <-answered:
		if resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonCancelled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not answer after the stream drained")
	}
}

// A prompt that arrives while a turn runs supersedes it: the SDK cancels the
// running prompt's context before it calls Prompt for the new one, so the older
// call answers cancelled and only then does the new turn start.
func TestPromptSupersededByNextPrompt(t *testing.T) {
	server, backend := newTestServer(t)
	session := newTestSession(t, server)

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstAnswered := make(chan acp.PromptResponse, 1)
	go func() {
		resp, err := server.Prompt(firstCtx, acp.PromptRequest{
			SessionId: session,
			Prompt:    []acp.ContentBlock{acp.TextBlock("first")},
		})
		if err != nil {
			t.Errorf("first Prompt returned error: %v", err)
		}
		firstAnswered <- resp
	}()

	<-backend.sent
	backend.events <- agent.StreamTextEvent{Text: "working", TurnID: 1, RequestSeq: 1}

	second := make(chan acp.PromptResponse, 1)
	go func() {
		resp, err := server.Prompt(context.Background(), acp.PromptRequest{
			SessionId: session,
			Prompt:    []acp.ContentBlock{acp.TextBlock("second")},
		})
		if err != nil {
			t.Errorf("second Prompt returned error: %v", err)
		}
		second <- resp
	}()

	cancelFirst()
	select {
	case resp := <-second:
		t.Fatalf("the new prompt answered %q while the old turn was still running", resp.StopReason)
	case <-time.After(50 * time.Millisecond):
	}

	backend.events <- agent.GlobalIdleEvent{}
	backend.events <- agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1}

	select {
	case resp := <-firstAnswered:
		if resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("superseded prompt answered %q, want %q", resp.StopReason, acp.StopReasonCancelled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the superseded prompt never answered")
	}

	<-backend.sent
	backend.events <- agent.StreamTextEvent{Text: "second answer", TurnID: 2, RequestSeq: 1}
	backend.events <- agent.StreamSegmentEndedEvent{TurnID: 2, RequestSeq: 1}
	backend.events <- agent.GlobalIdleEvent{}

	select {
	case resp := <-second:
		if resp.StopReason != acp.StopReasonEndTurn {
			t.Fatalf("the superseding prompt answered %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the superseding prompt never answered")
	}

	if got := backend.cancelCount(); got != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 1", got)
	}
	if got := backend.messageCount(); got != 2 {
		t.Fatalf("messages sent = %d, want 2", got)
	}
}

// session/close cancels the Chord turn without cancelling the prompt context,
// so the stop reason has to come from the turn state rather than the context.
func TestCloseSessionCancelsRunningPrompt(t *testing.T) {
	server, backend := newTestServer(t)
	session := newTestSession(t, server)

	answered := make(chan acp.PromptResponse, 1)
	go func() {
		resp, err := server.Prompt(context.Background(), acp.PromptRequest{
			SessionId: session,
			Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
		})
		if err != nil {
			t.Errorf("Prompt returned error: %v", err)
		}
		answered <- resp
	}()

	<-backend.sent
	// Settle the turn only once the adapter has asked Chord to cancel it, which
	// is what an in-flight turn does when the client closes the session.
	go func() {
		for backend.cancelCount() == 0 {
			time.Sleep(time.Millisecond)
		}
		backend.events <- agent.StreamTextEvent{Text: "working", TurnID: 1, RequestSeq: 1}
		backend.events <- agent.GlobalIdleEvent{}
		backend.events <- agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1}
	}()

	if _, err := server.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: session}); err != nil {
		t.Fatalf("CloseSession returned error: %v", err)
	}
	select {
	case resp := <-answered:
		if resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("StopReason = %q, want %q after the session was closed", resp.StopReason, acp.StopReasonCancelled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not answer after the session was closed")
	}
}

// A session/cancel that arrives while the prompt is still handing its message
// to the backend must not be lost: Chord can only cancel a message it has
// accepted, so the cancel shares the hand-off lock and runs after the message is
// in. Without that it would find nothing, the turn would run to completion, and
// the prompt would report cancelled over work that actually finished.
func TestCancelRacingTheHandoffIsNotLost(t *testing.T) {
	server, backend := newTestServer(t)
	session := newTestSession(t, server)

	entered, release := backend.holdNextSend()
	answered := make(chan acp.PromptResponse, 1)
	go func() {
		resp, err := server.Prompt(context.Background(), acp.PromptRequest{
			SessionId: session,
			Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
		})
		if err != nil {
			t.Errorf("Prompt returned error: %v", err)
		}
		answered <- resp
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the prompt never reached the hand-off to the backend")
	}

	// The cancel lands inside the hand-off and must wait for it, so the backend
	// never runs a CancelCurrentTurn while the message is still in flight.
	cancelReturned := make(chan error, 1)
	go func() {
		cancelReturned <- server.Cancel(context.Background(), acp.CancelNotification{SessionId: session})
	}()
	select {
	case err := <-cancelReturned:
		t.Fatalf("Cancel returned %v before the hand-off it must wait for finished", err)
	case <-time.After(50 * time.Millisecond):
	}
	if got := backend.cancelCount(); got != 0 {
		t.Fatalf("CancelCurrentTurn calls = %d, want the cancel to wait for the hand-off", got)
	}

	release()
	select {
	case <-backend.sent:
	case <-time.After(2 * time.Second):
		t.Fatal("the hand-off never recorded the message")
	}
	select {
	case err := <-cancelReturned:
		if err != nil {
			t.Fatalf("Cancel returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not return after the hand-off")
	}
	if got := backend.cancelCount(); got != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want exactly the one cancel that covers the accepted message", got)
	}

	// That cancel lands on the turn the message started, and the waiter settles
	// through the markers that turn emits.
	backend.events <- agent.RequestCycleStartedEvent{TurnID: 1}
	backend.events <- agent.GlobalIdleEvent{}
	select {
	case resp := <-answered:
		if resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonCancelled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not answer after the cancel")
	}
}

// A close that lands while a prompt is being handed to Chord must wait for the
// hand-off: answering first would let the turn start after the session was
// declared closed, with no waiter left for the close to cancel.
func TestCloseSessionWaitsForPromptHandoff(t *testing.T) {
	restore := settleTimeout
	settleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { settleTimeout = restore })

	server, backend := newTestServer(t)
	session := newTestSession(t, server)

	entered, release := backend.holdNextSend()
	answered := make(chan acp.PromptResponse, 1)
	go func() {
		resp, err := server.Prompt(context.Background(), acp.PromptRequest{
			SessionId: session,
			Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
		})
		if err != nil {
			t.Errorf("Prompt returned error: %v", err)
		}
		answered <- resp
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the prompt never reached the hand-off to the backend")
	}

	closed := make(chan error, 1)
	go func() {
		_, err := server.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: session})
		closed <- err
	}()

	// The close must not answer while the hand-off is still in flight.
	select {
	case err := <-closed:
		t.Fatalf("CloseSession answered while the prompt was still being handed to the backend (err=%v)", err)
	case <-time.After(200 * time.Millisecond):
	}

	// The turn is handed over before the close, so the close cancels it; settle
	// it the way an in-flight cancelled turn settles.
	release()
	go func() {
		for backend.cancelCount() == 0 {
			time.Sleep(time.Millisecond)
		}
		backend.events <- agent.StreamTextEvent{Text: "working", TurnID: 1, RequestSeq: 1}
		backend.events <- agent.GlobalIdleEvent{}
		backend.events <- agent.StreamSegmentEndedEvent{TurnID: 1, RequestSeq: 1}
	}()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("CloseSession returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseSession did not answer after the hand-off finished")
	}

	select {
	case resp := <-answered:
		if resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("StopReason = %q, want %q after the session was closed", resp.StopReason, acp.StopReasonCancelled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not answer after the session was closed")
	}
	if got := backend.messageCount(); got != 1 {
		t.Fatalf("messages sent = %d, want 1", got)
	}
}

// A turn that never reports its stream end must still answer: waiting on a
// boundary that may never arrive would hang the client's prompt forever.
func TestPromptSettlesWithoutStreamEnd(t *testing.T) {
	restore := settleTimeout
	settleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { settleTimeout = restore })

	server, backend := newTestServer(t)
	session := newTestSession(t, server)

	answered := make(chan acp.PromptResponse, 1)
	go func() {
		resp, err := server.Prompt(context.Background(), acp.PromptRequest{
			SessionId: session,
			Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
		})
		if err != nil {
			t.Errorf("Prompt returned error: %v", err)
		}
		answered <- resp
	}()

	<-backend.sent
	backend.events <- agent.StreamTextEvent{Text: "working", TurnID: 1, RequestSeq: 1}
	backend.events <- agent.GlobalIdleEvent{}

	select {
	case resp := <-answered:
		if resp.StopReason != acp.StopReasonEndTurn {
			t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not settle after the drain wait expired")
	}
}

// A late boundary from an earlier request must not close the stream of the turn
// running now: the waiter only accepts segment ends it opened.
func TestPromptIgnoresStaleSegmentDrain(t *testing.T) {
	server, backend := newTestServer(t)
	session := newTestSession(t, server)

	answered := make(chan acp.PromptResponse, 1)
	go func() {
		resp, err := server.Prompt(context.Background(), acp.PromptRequest{
			SessionId: session,
			Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
		})
		if err != nil {
			t.Errorf("Prompt returned error: %v", err)
		}
		answered <- resp
	}()

	<-backend.sent
	backend.events <- agent.StreamTextEvent{Text: "working", TurnID: 4, RequestSeq: 2}
	backend.events <- agent.GlobalIdleEvent{}
	backend.events <- agent.StreamSegmentEndedEvent{TurnID: 3, RequestSeq: 5}

	select {
	case resp := <-answered:
		t.Fatalf("Prompt answered %q on a stale segment end", resp.StopReason)
	case <-time.After(50 * time.Millisecond):
	}

	backend.events <- agent.StreamSegmentEndedEvent{TurnID: 4, RequestSeq: 2}

	select {
	case resp := <-answered:
		if resp.StopReason != acp.StopReasonEndTurn {
			t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not answer on its own segment end")
	}
}
