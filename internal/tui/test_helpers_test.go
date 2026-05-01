package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
)

// statusBarCurrentAgentLabel is consumed by status-bar agent-label tests; the
// production status bar reads viewingLabel directly off statusBarSnapshot().
func (m *Model) statusBarCurrentAgentLabel() string {
	return m.statusBarSnapshot().viewingLabel
}

// statusBarTotalLabel exposes the elapsed-section label so layout tests can
// assert presence/absence across narrow/wide terminal widths.
func statusBarTotalLabel() string {
	return "Elapsed "
}

// rebuildViewportFromMessages is a no-reason wrapper used by session-restore
// tests; production callers always supply a reason for telemetry.
func (m *Model) rebuildViewportFromMessages() {
	m.rebuildViewportFromMessagesWithReason("unspecified")
}

// prependQueuedDraft inserts a draft at the head of the queue so reclaim-queue
// tests can stage a specific drain order without exercising the full intake.
func (m *Model) prependQueuedDraft(draft queuedDraft) {
	draft.Mirrored = false
	if draft.QueuedAt.IsZero() {
		draft.QueuedAt = time.Now()
	}
	m.queuedDrafts = append([]queuedDraft{draft}, m.queuedDrafts...)
}

// resetImageRuntimeCache clears the package-global image cache so each test
// starts from a known-empty state.
func resetImageRuntimeCache() {
	imageRuntimeCache.mu.Lock()
	imageRuntimeCache.entries = make(map[string]*imageRuntimeCacheEntry)
	imageRuntimeCache.mu.Unlock()
}

// transportPNG is the locked accessor for the cached transport PNG; cache tests
// need to drive ensureTransportPNGUnlocked through the public locking surface.
func (e *imageRuntimeCacheEntry) transportPNG(part BlockImagePart) ([]byte, int, int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ensureTransportPNGUnlocked(part)
}

// imagePartAtLine maps a block-relative line index to the inline image part
// occupying that line; thumb hit tests use it without a column coordinate.
func (b *Block) imagePartAtLine(lineInBlock, width int) (BlockImagePart, bool) {
	if b == nil || b.Type != BlockUser || len(b.ImageParts) == 0 {
		return BlockImagePart{}, false
	}
	_ = b.Render(width, "")
	for idx, part := range b.ImageParts {
		if !imagePartLineHit(part, lineInBlock) {
			continue
		}
		part.Index = idx
		return part, true
	}
	return BlockImagePart{}, false
}

// imageViewerHintText surfaces the inline-image hint string that the image
// viewer tests assert on.
func imageViewerHintText(caps TerminalImageCapabilities, total int) string {
	switch caps.Backend {
	case ImageBackendKitty, ImageBackendITerm2:
		if total > 1 {
			return imageViewerHint
		}
		return imageViewerSingleHint
	default:
		return imageViewerFallbackHint
	}
}

// newCodeHighlighter is the language-inferring constructor used by syntax
// tests; production callers go through newCodeHighlighterWithLanguage to
// honor an explicit language hint.
func newCodeHighlighter(filePath, sample string) *codeHighlighter {
	return newCodeHighlighterWithLanguage(filePath, sample, "")
}

// stripANSILines copies lines with ANSI escapes removed; tests use it to make
// failure output readable when the viewport returns styled text.
func stripANSILines(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = stripANSI(line)
	}
	return out
}

// buildDiagnosticDump wraps buildDiagnosticDumpContent with the default
// non-sanitized path computation so tests don't have to recreate the path
// resolution logic.
func (m *Model) buildDiagnosticDump(now time.Time, trigger string) (string, string, error) {
	baseDir := strings.TrimSpace(m.workingDir)
	if baseDir == "" {
		if _, err := os.Getwd(); err != nil {
			return "", "", fmt.Errorf("resolve working dir: %w", err)
		}
	}
	locator, err := config.DefaultPathLocator()
	if err != nil {
		return "", "", fmt.Errorf("resolve storage paths: %w", err)
	}
	path := filepath.Join(locator.LogsDir, "tui-dumps",
		fmt.Sprintf("tui-dump-%s-%d.log", now.Format("20060102-150405.000"), os.Getpid()))

	m.recordTUIDiagnostic("diagnostic-dump", "%s", trigger)
	content, err := m.buildDiagnosticDumpContent(now, trigger, path, false)
	if err != nil {
		return "", "", err
	}
	return path, content, nil
}
