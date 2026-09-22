package acpmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func TestNewSessionServesConcurrentSessions(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	dirA, dirB := t.TempDir(), t.TempDir()

	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sessionA, err := h.conn.NewSession(ctx, newSessionRequest(dirA))
	if err != nil {
		t.Fatalf("session/new A: %v", err)
	}
	sessionB, err := h.conn.NewSession(ctx, newSessionRequest(dirB))
	if err != nil {
		t.Fatalf("session/new B: %v", err)
	}
	if sessionA.SessionId == sessionB.SessionId {
		t.Fatalf("both sessions got the id %v", sessionA.SessionId)
	}
	calls := h.calls()
	if len(calls) != 2 || calls[0].cwd != dirA || calls[1].cwd != dirB {
		t.Fatalf("children must be spawned one per session cwd, got %+v", calls)
	}
	if got := h.agent(0).newRequests(); len(got) != 1 || got[0].Cwd != dirA {
		t.Fatalf("child A saw %+v, want cwd %v", got, dirA)
	}
	if got := h.agent(1).newRequests(); len(got) != 1 || got[0].Cwd != dirB {
		t.Fatalf("child B saw %+v, want cwd %v", got, dirB)
	}

	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, session := range []acp.SessionId{sessionA.SessionId, sessionB.SessionId} {
		wg.Add(1)
		go func(session acp.SessionId) {
			defer wg.Done()
			resp, err := h.conn.Prompt(ctx, textPrompt(session, "hi"))
			if err != nil {
				failures <- fmt.Errorf("prompt %v: %w", session, err)
				return
			}
			if resp.StopReason != acp.StopReasonEndTurn {
				failures <- fmt.Errorf("prompt %v stop reason %v", session, resp.StopReason)
			}
		}(session)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	for _, session := range []acp.SessionId{sessionA.SessionId, sessionB.SessionId} {
		updates := h.client.updatesFor(session)
		if len(updates) != 1 {
			t.Errorf("session %v got %d updates, want 1", session, len(updates))
		}
	}
	// Each child answered under its own session id, so the frontend did rewrite
	// the id in both directions.
	if got := h.agent(0).promptRequests(); len(got) != 1 || got[0].SessionId != "agent-1-session-1" {
		t.Errorf("child A saw prompts %+v", got)
	}
	if got := h.agent(1).promptRequests(); len(got) != 1 || got[0].SessionId != "agent-2-session-1" {
		t.Errorf("child B saw prompts %+v", got)
	}
}

func TestInitializeIsForwardedWithClientCapabilities(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	caps := acp.ClientCapabilities{Fs: acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true}}

	resp, err := h.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientCapabilities: caps,
		ClientInfo:         &acp.Implementation{Name: "zed", Version: "1.0"},
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if resp.AgentInfo == nil || resp.AgentInfo.Name != "chord" {
		t.Errorf("agent info %+v", resp.AgentInfo)
	}
	if resp.AgentCapabilities.SessionCapabilities.Close == nil {
		t.Error("the frontend must advertise session/close: it forwards close to a child that implements it")
	}
	if _, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir())); err != nil {
		t.Fatalf("session/new: %v", err)
	}

	requests := h.agent(0).initRequests()
	if len(requests) != 1 {
		t.Fatalf("child got %d initialize requests, want 1", len(requests))
	}
	got := requests[0]
	if got.ProtocolVersion != acp.ProtocolVersionNumber {
		t.Errorf("child protocol version %v", got.ProtocolVersion)
	}
	if !reflect.DeepEqual(got.ClientCapabilities, caps) {
		t.Errorf("child client capabilities %+v, want %+v", got.ClientCapabilities, caps)
	}
	if got.ClientInfo == nil || got.ClientInfo.Name != "zed" {
		t.Errorf("child client info %+v", got.ClientInfo)
	}
}

func TestNewSessionRequiresInitialize(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})

	_, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	reqErr := requestError(t, err)
	if reqErr.Code != -32602 {
		t.Errorf("error code %d, want invalid params", reqErr.Code)
	}
	if text := errorDataText(t, reqErr); !strings.Contains(text, "initialize") {
		t.Errorf("error data %q must name initialize", text)
	}
	if h.childCount() != 0 {
		t.Error("a session/new before initialize must not start a child")
	}
}

// TestSessionLimitReservesBeforeSpawning covers the cap being taken and checked
// under one lock: a session whose child is still starting already holds its
// slot, so a concurrent session/new cannot slip past the limit.
func TestSessionLimitReservesBeforeSpawning(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test", MaxSessions: 1})
	cwd := t.TempDir()
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	gate := make(chan struct{})
	h.blockSpawn(gate)
	first := make(chan error, 1)
	go func() {
		_, err := h.conn.NewSession(ctx, newSessionRequest(cwd))
		first <- err
	}()
	<-h.spawnSeen

	_, err := h.conn.NewSession(ctx, newSessionRequest(cwd))
	reqErr := requestError(t, err)
	if text := errorDataText(t, reqErr); !strings.Contains(text, "session limit") {
		t.Errorf("error data %q must report the session limit", text)
	}
	close(gate)
	if err := <-first; err != nil {
		t.Fatalf("the reserved session must still be created: %v", err)
	}
}

func TestCloseSessionStopsChildAndFreesSlot(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test", MaxSessions: 1})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	session, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	if _, err := h.conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session.SessionId}); err != nil {
		t.Fatalf("session/close: %v", err)
	}
	closes := h.agent(0).closeRequests()
	if len(closes) != 1 || closes[0].SessionId != "agent-1-session-1" {
		t.Fatalf("child saw close requests %+v, want its own session id", closes)
	}
	waitFor(t, "the child to be stopped", func() bool { return h.child(0).stopCount() == 1 })
	waitFor(t, "the session slot to be freed", func() bool { return h.canOpenSession(ctx) })

	_, err = h.conn.Prompt(ctx, textPrompt(session.SessionId, "again"))
	reqErr := requestError(t, err)
	if text := errorDataText(t, reqErr); !strings.Contains(text, "unknown session") {
		t.Errorf("error data %q must report the closed session as unknown", text)
	}
}

// TestCloseSessionReleasesChildPipes covers what a closed session must give
// back: the frontend holds one pipe to each child, and a session that is gone
// has to release its own without touching another's.
func TestCloseSessionReleasesChildPipes(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sessionA, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new A: %v", err)
	}
	sessionB, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new B: %v", err)
	}

	if _, err := h.conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: sessionA.SessionId}); err != nil {
		t.Fatalf("session/close A: %v", err)
	}
	waitFor(t, "child A to be reaped", func() bool { return h.child(0).exitedNow() })
	waitFor(t, "child A's pipes to be released", func() bool { return h.child(0).closeCount() == 1 })
	if _, err := h.child(0).Reader().Read(make([]byte, 1)); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("child A's output pipe is still open: %v", err)
	}
	if got := h.child(1).closeCount(); got != 0 {
		t.Errorf("closing session A released session B's pipes %d times", got)
	}

	// The surviving session keeps working, and closing it releases its own
	// pipes rather than session A's.
	resp, err := h.conn.Prompt(ctx, textPrompt(sessionB.SessionId, "hi"))
	if err != nil {
		t.Fatalf("the surviving session must keep working: %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("surviving prompt stop reason %v", resp.StopReason)
	}
	if _, err := h.conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: sessionB.SessionId}); err != nil {
		t.Fatalf("session/close B: %v", err)
	}
	waitFor(t, "child B's pipes to be released", func() bool { return h.child(1).closeCount() == 1 })
}

// TestShutdownDuringSpawnReapsTheChild covers the window between reserving a
// session and spawning its child. A frontend that starts shutting down in that
// window must not leave a runtime behind: the session is never published, so
// the only path that can reap that child is the one that spawned it.
func TestShutdownDuringSpawnReapsTheChild(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	gate := make(chan struct{})
	h.blockSpawn(gate)
	result := make(chan error, 1)
	go func() {
		_, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
		result <- err
	}()
	<-h.spawnSeen

	shutdownDone := make(chan struct{})
	go func() {
		h.proxy.Shutdown(5 * time.Second)
		close(shutdownDone)
	}()
	waitFor(t, "the shutdown to claim the reserved session", func() bool {
		h.proxy.mu.Lock()
		defer h.proxy.mu.Unlock()
		for _, s := range h.proxy.sessions {
			if s.stopRequested {
				return true
			}
		}
		return false
	})
	close(gate)

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("session/new must fail once the frontend is shutting down")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session/new hung after its frontend started shutting down")
	}
	waitFor(t, "the child spawned during shutdown to be stopped", func() bool {
		return h.childCount() == 1 && h.child(0).stopCount() == 1
	})
	waitFor(t, "the child to be reaped", func() bool { return h.child(0).exitedNow() })
	waitFor(t, "the child's pipes to be released", func() bool { return h.child(0).closeCount() == 1 })
	select {
	case <-shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish once the session child was gone")
	}
}

func TestCancelTargetsOneSession(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sessionA, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new A: %v", err)
	}
	sessionB, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new B: %v", err)
	}
	for i := range 2 {
		h.agent(i).setOnPrompt(func(ctx context.Context, _ acp.PromptRequest) (acp.PromptResponse, error) {
			<-ctx.Done()
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		})
	}

	type outcome struct {
		resp acp.PromptResponse
		err  error
	}
	outcomeA := make(chan outcome, 1)
	outcomeB := make(chan outcome, 1)
	go func() {
		resp, err := h.conn.Prompt(ctx, textPrompt(sessionA.SessionId, "a"))
		outcomeA <- outcome{resp, err}
	}()
	go func() {
		resp, err := h.conn.Prompt(ctx, textPrompt(sessionB.SessionId, "b"))
		outcomeB <- outcome{resp, err}
	}()
	waitFor(t, "both prompts to reach their children", func() bool {
		return h.agent(0).promptCount() == 1 && h.agent(1).promptCount() == 1
	})

	if err := h.conn.Cancel(ctx, acp.CancelNotification{SessionId: sessionA.SessionId}); err != nil {
		t.Fatalf("session/cancel: %v", err)
	}
	got := <-outcomeA
	if got.err != nil {
		t.Fatalf("cancelled prompt A: %v", got.err)
	}
	if got.resp.StopReason != acp.StopReasonCancelled {
		t.Errorf("prompt A stop reason %v, want cancelled", got.resp.StopReason)
	}
	if cancels := h.agent(0).cancelRequests(); len(cancels) != 1 || cancels[0].SessionId != "agent-1-session-1" {
		t.Errorf("child A saw cancels %+v", cancels)
	}
	if cancels := h.agent(1).cancelRequests(); len(cancels) != 0 {
		t.Errorf("cancelling one session reached another child: %+v", cancels)
	}
	select {
	case got := <-outcomeB:
		t.Fatalf("cancel of session A ended session B: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	if err := h.conn.Cancel(ctx, acp.CancelNotification{SessionId: sessionB.SessionId}); err != nil {
		t.Fatalf("session/cancel B: %v", err)
	}
	got = <-outcomeB
	if got.err != nil || got.resp.StopReason != acp.StopReasonCancelled {
		t.Fatalf("cancelled prompt B: %+v", got)
	}
}

// TestCancelledPromptForwardFailureReportsCancelled covers the branch where the
// child answers a cancelled turn with an error instead of the cancelled stop
// reason. The client asked to cancel, so it still gets the cancelled stop
// reason rather than an internal error.
func TestCancelledPromptForwardFailureReportsCancelled(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	session, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	h.agent(0).setOnPrompt(func(ctx context.Context, _ acp.PromptRequest) (acp.PromptResponse, error) {
		<-ctx.Done()
		return acp.PromptResponse{}, acp.NewInternalError(map[string]any{"error": "turn failed while cancelling"})
	})

	type outcome struct {
		resp acp.PromptResponse
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := h.conn.Prompt(ctx, textPrompt(session.SessionId, "a"))
		done <- outcome{resp, err}
	}()
	waitFor(t, "the prompt to reach the child", func() bool { return h.agent(0).promptCount() == 1 })

	if err := h.conn.Cancel(ctx, acp.CancelNotification{SessionId: session.SessionId}); err != nil {
		t.Fatalf("session/cancel: %v", err)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("cancelled prompt: %v", got.err)
	}
	if got.resp.StopReason != acp.StopReasonCancelled {
		t.Errorf("stop reason %v, want cancelled", got.resp.StopReason)
	}
}

// TestPromptAlreadyCancelledReportsCancelled covers a prompt whose context was
// cancelled before the forward: the turn must not start at the child, and the
// client still gets the cancelled stop reason.
func TestPromptAlreadyCancelledReportsCancelled(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	session, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err := h.proxy.Prompt(cancelled, textPrompt(session.SessionId, "hi"))
	if err != nil {
		t.Fatalf("prompt with cancelled context: %v", err)
	}
	if resp.StopReason != acp.StopReasonCancelled {
		t.Errorf("stop reason %v, want cancelled", resp.StopReason)
	}
	if got := h.agent(0).promptCount(); got != 0 {
		t.Errorf("child saw %d prompts, want 0", got)
	}
}

// TestUpdatePrecedesPromptResponse relies on the ACP client's own guarantee: a
// response is only delivered after the notifications that preceded it on the
// wire have been handled. So if the client has already handled the update when
// the prompt answer arrives, the frontend wrote them in that order.
func TestUpdatePrecedesPromptResponse(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	session, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if _, err := h.conn.Prompt(ctx, textPrompt(session.SessionId, "hi")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if updates := h.client.updatesFor(session.SessionId); len(updates) != 1 {
		t.Fatalf("the client handled %d updates when the prompt answered, want 1", len(updates))
	}
}

func TestChildCrashIsolatesSessions(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sessionA, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new A: %v", err)
	}
	sessionB, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new B: %v", err)
	}
	blocked := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocked) }) }
	t.Cleanup(release)
	h.agent(0).setOnPrompt(func(context.Context, acp.PromptRequest) (acp.PromptResponse, error) {
		<-blocked
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	})

	failed := make(chan error, 1)
	go func() {
		_, err := h.conn.Prompt(ctx, textPrompt(sessionA.SessionId, "a"))
		failed <- err
	}()
	waitFor(t, "the prompt to reach its child", func() bool { return h.agent(0).promptCount() == 1 })

	h.child(0).crash(errors.New("killed"))
	release()
	if err := <-failed; err == nil {
		t.Fatal("a prompt on a dead child must fail")
	}

	resp, err := h.conn.Prompt(ctx, textPrompt(sessionB.SessionId, "b"))
	if err != nil {
		t.Fatalf("the surviving session must keep working: %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("surviving prompt stop reason %v", resp.StopReason)
	}

	_, err = h.conn.Prompt(ctx, textPrompt(sessionA.SessionId, "a again"))
	reqErr := requestError(t, err)
	if text := errorDataText(t, reqErr); !strings.Contains(text, "unknown session") {
		t.Errorf("error data %q must report the crashed session as unknown", text)
	}
	// A crash ends the child without a close, and its pipes have to go with it.
	waitFor(t, "the crashed child's pipes to be released", func() bool { return h.child(0).closeCount() == 1 })
}

func TestShutdownStopsEveryChild(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	for i := range 2 {
		if _, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir())); err != nil {
			t.Fatalf("session/new %d: %v", i, err)
		}
	}

	h.proxy.Shutdown(5 * time.Second)
	for i := range 2 {
		if got := h.child(i).stopCount(); got != 1 {
			t.Errorf("child %d stopped %d times, want 1", i, got)
		}
		if !h.child(i).exitedNow() {
			t.Errorf("child %d is still running", i)
		}
	}
	_, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	reqErr := requestError(t, err)
	if text := errorDataText(t, reqErr); !strings.Contains(text, "shutting down") {
		t.Errorf("error data %q must report the shutdown", text)
	}
}

func TestChildSessionNewFailureReachesClient(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test", MaxSessions: 1})
	var failed sync.Once
	h.setChildInit(func(agent *stubAgent) {
		agent.setOnNew(func(req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
			reject := false
			failed.Do(func() { reject = true })
			if reject {
				return acp.NewSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "model unavailable"})
			}
			return agent.defaultNewSession(req)
		})
	})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	_, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	reqErr := requestError(t, err)
	if reqErr.Code != -32602 {
		t.Errorf("error code %d, want the child's own invalid params", reqErr.Code)
	}
	if text := errorDataText(t, reqErr); !strings.Contains(text, "model unavailable") {
		t.Errorf("error data %q must carry the child's reason", text)
	}
	waitFor(t, "the failed child to be reaped", func() bool { return h.child(0).stopCount() == 1 })
	waitFor(t, "the failed session slot to be freed", func() bool { return h.canOpenSession(ctx) })
}

// TestFailedSessionHoldsSlotUntilChildReaped covers the limit counting a child
// that is still dying: the session stays in the table until watch reaps it, so
// a retry cannot stack a second runtime on top of the first.
func TestFailedSessionHoldsSlotUntilChildReaped(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test", MaxSessions: 1})
	exitGate := make(chan struct{})
	h.blockChildExit(exitGate)
	var failed sync.Once
	h.setChildInit(func(agent *stubAgent) {
		agent.setOnNew(func(req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
			reject := false
			failed.Do(func() { reject = true })
			if reject {
				return acp.NewSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "model unavailable"})
			}
			return agent.defaultNewSession(req)
		})
	})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	_, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	reqErr := requestError(t, err)
	if text := errorDataText(t, reqErr); !strings.Contains(text, "model unavailable") {
		t.Errorf("error data %q must carry the child's reason", text)
	}
	waitFor(t, "the failed child to be asked to stop", func() bool { return h.child(0).stopCount() == 1 })

	// The child has not exited yet, so the only slot is still taken.
	_, err = h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	reqErr = requestError(t, err)
	if text := errorDataText(t, reqErr); !strings.Contains(text, "session limit") {
		t.Errorf("error data %q must report the session limit while the child dies", text)
	}

	close(exitGate)
	waitFor(t, "the failed child to be reaped", func() bool { return h.child(0).exitedNow() })
	waitFor(t, "the failed session slot to be freed", func() bool { return h.canOpenSession(ctx) })
}

func TestSpawnFailureReachesClient(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	h.failSpawn(errors.New("exec: no such file"))
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	_, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	reqErr := requestError(t, err)
	if text := errorDataText(t, reqErr); !strings.Contains(text, "no such file") {
		t.Errorf("error data %q must carry the spawn failure", text)
	}
}

func TestChildDeathDuringNewSessionFails(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	blocked := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocked) }) }
	t.Cleanup(release)
	h.setChildInit(func(agent *stubAgent) {
		agent.setOnNew(func(acp.NewSessionRequest) (acp.NewSessionResponse, error) {
			<-blocked
			return acp.NewSessionResponse{SessionId: "child-1"}, nil
		})
	})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
		result <- err
	}()
	waitFor(t, "session/new to reach the child", func() bool {
		a := h.agentIfAny(0)
		return a != nil && a.newCount() == 1
	})

	h.child(0).crash(errors.New("killed"))
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("session/new must fail when its child dies")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session/new hung after its child died")
	}
}

func TestPermissionRequestIsForwardedToClient(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	answer := make(chan error, 1)
	h.setChildInit(func(agent *stubAgent) {
		agent.setOnPrompt(func(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
			resp, err := agent.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
				SessionId: req.SessionId,
				ToolCall:  acp.ToolCallUpdate{ToolCallId: "call-1"},
				Options: []acp.PermissionOption{{
					OptionId: "allow",
					Name:     "Allow",
					Kind:     acp.PermissionOptionKindAllowOnce,
				}},
			})
			if err != nil {
				answer <- err
				return acp.PromptResponse{}, err
			}
			if resp.Outcome.Selected == nil {
				answer <- errors.New("client did not select an option")
			} else {
				answer <- nil
			}
			return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
		})
	})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	session, err := h.conn.NewSession(ctx, newSessionRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	if _, err := h.conn.Prompt(ctx, textPrompt(session.SessionId, "hi")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	requests := h.client.permissionRequests()
	if len(requests) != 1 {
		t.Fatalf("client saw %d permission requests, want 1", len(requests))
	}
	if requests[0].SessionId != session.SessionId {
		t.Errorf("client saw session %v, want its own %v", requests[0].SessionId, session.SessionId)
	}
	if err := <-answer; err != nil {
		t.Errorf("the child did not get the client's answer: %v", err)
	}
}

func TestUnknownSessionIsRejected(t *testing.T) {
	ctx := newTestContext(t)
	h := newHarness(t, Options{Version: "test"})
	if _, err := h.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	unknown := acp.SessionId("chord-0-999")

	_, err := h.conn.Prompt(ctx, textPrompt(unknown, "hi"))
	if reqErr := requestError(t, err); !strings.Contains(errorDataText(t, reqErr), "unknown session") {
		t.Errorf("prompt error %v", reqErr)
	}
	if _, err := h.conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: unknown}); err == nil {
		t.Error("closing an unknown session must fail")
	}
	// Cancel is a notification: an unknown session is ignored, not an error.
	if err := h.conn.Cancel(ctx, acp.CancelNotification{SessionId: unknown}); err != nil {
		t.Errorf("cancel of an unknown session: %v", err)
	}
}

// requestError extracts the JSON-RPC error the client received.
func requestError(t *testing.T, err error) *acp.RequestError {
	t.Helper()
	reqErr, ok := errors.AsType[*acp.RequestError](err)
	if !ok {
		t.Fatalf("want a JSON-RPC error, got %v", err)
	}
	return reqErr
}

// errorDataText is the "error" field of a forwarded error's data, which is
// where the SDK and Chord put the human-readable detail.
func errorDataText(t *testing.T, reqErr *acp.RequestError) string {
	t.Helper()
	data, ok := reqErr.Data.(map[string]any)
	if !ok {
		return ""
	}
	text, _ := data["error"].(string)
	return text
}

// canOpenSession reports whether a session/new is accepted right now, which is
// how a freed slot shows itself.
func (h *harness) canOpenSession(ctx context.Context) bool {
	_, err := h.conn.NewSession(ctx, newSessionRequest(h.t.TempDir()))
	return err == nil
}
