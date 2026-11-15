package mcp

import (
	"context"
	"fmt"
	"io"
)

func (t *StdioTransport) writeFrame(ctx context.Context, data []byte) error {
	select {
	case t.writeGate <- struct{}{}:
	case <-ctx.Done():
		return newCallFailure(callNotSent, ctx.Err())
	case <-t.done:
		return newCallFailure(callNotSent, fmt.Errorf("mcp stdio: transport closed"))
	}
	// Cancellation and writer availability can become ready together.
	if err := ctx.Err(); err != nil {
		<-t.writeGate
		return newCallFailure(callNotSent, err)
	}
	t.mu.Lock()
	if t.closed || t.readerExited || t.writeErr != nil {
		err := t.writeErr
		if err == nil {
			err = fmt.Errorf("mcp stdio: transport closed")
		}
		t.mu.Unlock()
		<-t.writeGate
		return newCallFailure(callNotSent, err)
	}
	finished := make(chan struct{})
	t.writerDone = finished
	t.mu.Unlock()
	result := make(chan error, 1)
	// The transport owns an admitted frame until Write finishes. A caller
	// cancellation must not release the gate or splice another frame into it.
	go func() {
		n, err := t.stdin.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			t.mu.Lock()
			t.writeErr = err
			t.mu.Unlock()
			outcome := callOutcomeUnknown
			if n == 0 {
				outcome = callNotSent
			}
			err = newCallFailure(outcome, err)
		}
		<-t.writeGate
		result <- err
		close(finished)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		select {
		case err := <-result:
			return err
		default:
		}
		return newCallFailure(callOutcomeUnknown, ctx.Err())
	case <-t.done:
		return newCallFailure(callOutcomeUnknown, fmt.Errorf("mcp stdio: transport closed during write"))
	}
}
