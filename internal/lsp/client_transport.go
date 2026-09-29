package lsp

import (
	"errors"
	"io"
	"io/fs"
	"net"

	"github.com/sourcegraph/jsonrpc2"
)

// A failed protocol connection is terminal for this client instance. Transport
// errors are authoritative even when the process wrapper still reports running.
// Cancellation and protocol/application errors do not imply a dead connection.
func (c *Client) observeTransportError(err error) error {
	if isTransportFailure(err) {
		c.transportFailed.Store(true)
	}
	return err
}

// isTransportFailure reports whether err means the connection to the server is
// gone. Besides jsonrpc2's own closed error it covers what a write to the
// server's stdio pipe returns before jsonrpc2 notices: the platform's broken
// pipe error (wrapped in an *fs.PathError) once the server stopped reading,
// and fs.ErrClosed once the pipe itself was closed.
func isTransportFailure(err error) bool {
	return errors.Is(err, jsonrpc2.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, fs.ErrClosed) ||
		isBrokenPipe(err)
}
