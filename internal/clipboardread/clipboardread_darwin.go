//go:build darwin

package clipboardread

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/imageutil"
)

// osascriptPath is the system JXA runner. The absolute path keeps a hijacked
// PATH from standing in for it.
const osascriptPath = "/usr/bin/osascript"

// The reader budget covers osascript startup plus one pasteboard fetch, which
// replaces the in-process probe budget; waitDelay bounds how long a wedged
// reader can hold the pipes open after the deadline. Both are variables so
// tests can shorten them.
var (
	readTimeout = 3500 * time.Millisecond
	waitDelay   = 500 * time.Millisecond
)

// newReaderCommand prepares the JXA reader. It is a variable so tests can run a
// stand-in process.
var newReaderCommand = func(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, osascriptPath, "-l", "JavaScript", "-e", readerScript)
}

// Read reads one clipboard attachment through osascript and normalizes it for a
// provider. It reports ErrNoAttachment when the clipboard holds neither an
// image nor a PDF, and otherwise leaves the clipboard untouched.
func Read() ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	cmd := newReaderCommand(ctx)
	cmd.WaitDelay = waitDelay
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", fmt.Errorf("clipboard reader stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start clipboard reader: %w", err)
	}

	data, mimeType, decodeErr := decodeReaderOutput(stdout)
	waitErr := cmd.Wait()
	if waitErr != nil {
		return nil, "", readerError(ctx, waitErr, stderr.String())
	}
	if decodeErr != nil {
		log.Warnf("clipboard reader payload was rejected error=%v stderr=%q", decodeErr, strings.TrimSpace(stderr.String()))
		return nil, "", decodeErr
	}
	return normalizeAttachment(data, mimeType)
}

// normalizeAttachment applies the limits and conversions the attachment path
// expects: PDFs stay as they are, everything else goes through the shared
// image normalizer, which re-encodes formats a provider does not accept (TIFF
// among them) and enforces the size and pixel limits.
func normalizeAttachment(data []byte, mimeType string) ([]byte, string, error) {
	if mimeType == "application/pdf" {
		if err := imageutil.CheckPDFSize(data); err != nil {
			return nil, "", err
		}
		return data, "application/pdf", nil
	}
	return imageutil.NormalizeImageBytes(data, mimeType)
}

// readerError turns the reader's exit status into the error the user sees. The
// reader explains itself on stderr, which stays in the log: the toast carries a
// product-level sentence, not a diagnostic.
func readerError(ctx context.Context, waitErr error, stderr string) error {
	detail := strings.TrimSpace(stderr)
	if ctx.Err() != nil {
		log.Warnf("clipboard reader timed out after %v error=%v stderr=%q", readTimeout, waitErr, detail)
		return fmt.Errorf("reading the clipboard attachment timed out: %w", ctx.Err())
	}
	log.Warnf("clipboard reader failed error=%v stderr=%q", waitErr, detail)
	return fmt.Errorf("clipboard attachment unavailable: %w", waitErr)
}
