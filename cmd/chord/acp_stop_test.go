package main

import (
	"context"
	"io"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// acpConnectionOverPipe returns an agent-side connection reading from a pipe,
// plus the client end of it. Closing the client end is what the frontend does
// to stop a session child: its stdin reaches EOF.
//
// No peer ever writes to the pipe, so the connection's agent is never called.
func acpConnectionOverPipe(t *testing.T) (*acp.AgentSideConnection, io.Closer) {
	t.Helper()
	peerRead, peerWrite := io.Pipe()
	t.Cleanup(func() { _ = peerWrite.Close() })
	return acp.NewAgentSideConnection(nil, io.Discard, peerRead), peerWrite
}

// TestWaitForACPStopWaitsForTheClientToReleaseTheConnection pins the session
// child's stop rule: session/close alone must not tear the runtime down, since
// the ACP connection writes the close answer only after its handler returns.
// The frontend stops the child once it has that answer, which ends the
// connection, so waiting for the connection is what lets the answer reach the
// pipe first.
func TestWaitForACPStopWaitsForTheClientToReleaseTheConnection(t *testing.T) {
	conn, release := acpConnectionOverPipe(t)
	sessionClosed := make(chan struct{})
	done := make(chan string, 1)
	go func() { done <- waitForACPStop(context.Background(), conn, sessionClosed) }()

	close(sessionClosed)
	// Wait past the fixed flush window the child used to sleep through: a stop
	// that fired on a timer would return here, while waiting for the client to
	// release the connection cannot.
	select {
	case reason := <-done:
		t.Fatalf("waitForACPStop returned %q on session/close, want it to wait for the client to release the connection", reason)
	case <-time.After(400 * time.Millisecond):
	}

	if err := release.Close(); err != nil {
		t.Fatalf("release the connection: %v", err)
	}
	select {
	case reason := <-done:
		if reason != "session-closed" {
			t.Fatalf("stop reason = %q, want session-closed", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForACPStop did not return after the client released the connection")
	}
}

// TestWaitForACPStopReturnsOnContextCancel pins that waiting for the connection
// does not make the child deaf to shutdown.
func TestWaitForACPStopReturnsOnContextCancel(t *testing.T) {
	conn, _ := acpConnectionOverPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- waitForACPStop(ctx, conn, make(chan struct{})) }()

	cancel()
	select {
	case reason := <-done:
		if reason != "shutdown" {
			t.Fatalf("stop reason = %q, want shutdown", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForACPStop ignored context cancellation")
	}
}
