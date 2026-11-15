package mcp

import (
	"context"
	"fmt"
)

func (t *StdioTransport) writeFrame(ctx context.Context, data []byte) (int, error) {
	select {
	case t.writeGate <- struct{}{}:
		defer func() { <-t.writeGate }()
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-t.done:
		return 0, fmt.Errorf("mcp stdio: transport closed")
	}
	// Cancellation and writer availability can become ready together.
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	t.mu.Lock()
	closed := t.closed || t.readerExited
	t.mu.Unlock()
	if closed {
		return 0, fmt.Errorf("mcp stdio: transport closed")
	}
	return t.stdin.Write(data)
}
