package lsp

import (
	"context"
	"crypto/sha256"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"
)

// recordSyncedContent remembers the content hash Chord last sent for path. It
// runs on the success path of every full-document notification, so a later
// resync can tell an unchanged document from an externally modified one.
func (c *Client) recordSyncedContent(path, content string) {
	sum := sha256.Sum256([]byte(content))
	c.openFilesMu.Lock()
	if c.syncedDigest == nil {
		c.syncedDigest = make(map[string][sha256.Size]byte)
	}
	c.syncedDigest[path] = sum
	c.openFilesMu.Unlock()
}

// ResyncFileIfChanged sends content as a whole-document didChange when this
// client already has path open and the last content Chord sent differs from
// content. Documents the client never opened are left alone: a server without
// the file has no stale copy to correct, and opening documents is reserved for
// Chord's own write paths. The bool reports whether a notification was sent;
// an unchanged document is a silent no-op.
func (c *Client) ResyncFileIfChanged(ctx context.Context, path, content string) (bool, error) {
	if c == nil || c.client == nil {
		return false, nil
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.closed {
		return false, nil
	}
	c.openFilesMu.Lock()
	_, open := c.openFiles[path]
	c.openFilesMu.Unlock()
	if !open {
		return false, nil
	}
	sum := sha256.Sum256([]byte(content))
	c.openFilesMu.Lock()
	previous, known := c.syncedDigest[path]
	version, stillOpen := c.openFiles[path]
	if !stillOpen || (known && previous == sum) {
		c.openFilesMu.Unlock()
		return false, nil
	}
	version++
	c.openFiles[path] = version
	c.openFilesMu.Unlock()

	changes := []protocol.TextDocumentContentChangeEvent{
		{Value: protocol.TextDocumentContentChangeWholeDocument{Text: content}},
	}
	if err := c.observeTransportError(c.client.NotifyDidChangeTextDocument(ctx, c.pathToURI(path), int(version), changes)); err != nil {
		return false, err
	}
	c.recordSyncedContent(path, content)
	return true, nil
}
