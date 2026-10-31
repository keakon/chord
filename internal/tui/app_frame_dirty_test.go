package tui

import (
	"strings"
	"testing"

	uitheme "github.com/keakon/chord/internal/tui/theme"
)

// A frame where every layer blits its cache unchanged must take the
// dirty-flag fast path: the flag stays clear and the content matches the
// compared fallback byte for byte.
func TestViewCleanFrameKeepsDirtyFlagClearAndContentStable(t *testing.T) {
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(nil, 100, 30)
	t.Cleanup(func() { _ = m.Close() })

	// Cold start takes two dirty frames: the first draws the welcome screen
	// directly without caching cells, the second rebuilds the cell cache.
	_ = m.View()
	_ = m.View()
	first := m.View()
	if !m.cachedFullViewValid {
		t.Fatal("warm-up Views did not populate the full-view cache")
	}
	if m.frameContentDirty {
		t.Fatal("warm Views left the frame dirty with no state change")
	}
	m.frameContentDirty = false
	second := m.View()
	if m.frameContentDirty {
		t.Fatal("clean frame reported dirty content")
	}
	if second.Content != first.Content {
		t.Fatalf("clean frame content changed:\nfirst=%q\nsecond=%q", first.Content, second.Content)
	}
}

// A state change that re-renders a layer must clear the fast path: the next
// View reflects the new state instead of the cached content.
func TestViewContentChangeBypassesCleanFrameCache(t *testing.T) {
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(nil, 100, 30)
	t.Cleanup(func() { _ = m.Close() })

	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "before change"})
	before := m.View()
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "after change"})
	after := m.View()
	if after.Content == before.Content {
		t.Fatal("appending a block did not change the rendered content")
	}
	if !strings.Contains(after.Content, "after change") {
		t.Fatal("rendered content is missing the appended block")
	}
}

// Dismissing a toast must remove it from the next frame even though nothing
// re-rendered: the toast area falls back to the cleared background.
func TestViewToastDisappearanceClearsFastPath(t *testing.T) {
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(nil, 100, 30)
	t.Cleanup(func() { _ = m.Close() })

	m.enqueueToastWithCategory("sample toast message", "info", "")
	withToast := m.View()
	if !strings.Contains(withToast.Content, "sample toast message") {
		t.Fatal("toast did not render")
	}
	m.activeToast = nil
	withoutToast := m.View()
	if strings.Contains(withoutToast.Content, "sample toast message") {
		t.Fatal("dismissed toast is still visible in the next frame")
	}
}

// attachmentBarDisappearance frames asserts the shared disappearance contract:
// the bar vanishes from the next frame, and after the disappearance frame (plus
// the main cell rebuild the layout shift can trigger) the clean-frame path
// comes back instead of staying dirty until the bar reappears.
func assertBarDisappearanceReturnsToCleanFrames(t *testing.T, m *Model, marker string) {
	t.Helper()
	withoutBar := m.View()
	if strings.Contains(withoutBar.Content, marker) {
		t.Fatal("removed bar content is still visible in the next frame")
	}
	dirty := 0
	for range 4 {
		next := m.View()
		if m.frameContentDirty {
			dirty++
			if dirty > 2 {
				t.Fatal("frames after the bar disappearance stay dirty")
			}
			continue
		}
		if next.Content != withoutBar.Content {
			t.Fatal("idle frame content changed after the bar disappearance")
		}
	}
}

// Emptying the attachments removes the bar from the next frame and must clear
// its cache: later frames return to the clean-frame path.
func TestViewAttachmentBarDisappearanceClearsFastPath(t *testing.T) {
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(nil, 100, 30)
	t.Cleanup(func() { _ = m.Close() })

	m.attachments = []Attachment{{FileName: "sample-report.txt", SizeBytes: 2048}}
	withBar := m.View()
	if !strings.Contains(withBar.Content, "sample-report.txt") {
		t.Fatal("attachment bar did not render")
	}
	m.attachments = nil
	assertBarDisappearanceReturnsToCleanFrames(t, &m, "sample-report.txt")
}

// Clearing the queued drafts removes the queue bar from the next frame and
// must clear its cache: later frames return to the clean-frame path.
func TestViewQueueBarDisappearanceClearsFastPath(t *testing.T) {
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(nil, 100, 30)
	t.Cleanup(func() { _ = m.Close() })

	m.queuedDrafts = []queuedDraft{{
		ID: "draft-1", InputID: "input-1", Revision: 1,
		Content: "sample queued text", DisplayContent: "sample queued text",
	}}
	withBar := m.View()
	if !strings.Contains(withBar.Content, "sample queued text") {
		t.Fatal("queue bar did not render")
	}
	m.queuedDrafts = nil
	assertBarDisappearanceReturnsToCleanFrames(t, &m, "sample queued text")
}

// Leaving an overlay mode must drop the dialog from the next frame: the
// exposed cells show the base layers again.
func TestViewOverlayDisappearanceClearsFastPath(t *testing.T) {
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(nil, 100, 30)
	t.Cleanup(func() { _ = m.Close() })

	m.mode = ModeMemoryPanel
	withOverlay := m.View()
	if withOverlay.Content == "" {
		t.Fatal("overlay mode rendered nothing")
	}
	overlayDrawn := m.overlayDrawnThisFrame
	if !overlayDrawn {
		t.Fatal("overlay frame did not record an overlay draw")
	}
	m.mode = ModeInsert
	withoutOverlay := m.View()
	if m.overlayDrawnThisFrame {
		t.Fatal("plain insert mode recorded an overlay draw")
	}
	if withoutOverlay.Content == withOverlay.Content {
		t.Fatal("leaving the overlay mode did not change the rendered content")
	}
	if strings.Contains(withoutOverlay.Content, strings.TrimSpace(overlayMarkerText(t, &m))) && overlayMarkerText(t, &m) != "" {
		t.Fatal("overlay content is still visible after leaving the mode")
	}
	// Only the first frame after leaving the overlay is dirty from the
	// disappearance; the next frame may rebuild the main cell cache after the
	// direct draw (the same two-frame pattern as cold start). From then on the
	// frames must be clean and stable instead of staying dirty forever.
	clean := withoutOverlay.Content
	dirtyFrames := 0
	for range 5 {
		next := m.View()
		if m.frameContentDirty {
			dirtyFrames++
			if dirtyFrames > 2 {
				t.Fatal("frames after the overlay disappearance stay dirty")
			}
			continue
		}
		if next.Content != clean {
			t.Fatal("idle frame content changed after the overlay disappearance")
		}
	}
	if dirtyFrames == 0 {
		t.Fatal("overlay disappearance did not dirty any frame")
	}
}

// overlayMarkerText returns the current memory panel dialog body, or "" when
// the mode renders nothing; used only to assert overlay removal above.
func overlayMarkerText(t *testing.T, m *Model) string {
	t.Helper()
	return m.renderMemoryPanel()
}

// The fast path must never win over a real content change: a dirty frame whose
// rebuilt screen equals the previous one still returns the same content, and a
// frame whose screen differs must return the new bytes.
func TestViewDirtyFrameWithUnchangedScreenMatchesFallback(t *testing.T) {
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(nil, 100, 30)
	t.Cleanup(func() { _ = m.Close() })

	first := m.View()
	// Force the compared fallback: a pending resize flag starts the next frame
	// dirty although the screen size and every layer stay identical.
	m.screenResized = true
	second := m.View()
	if m.screenResized {
		t.Fatal("drawFrame did not consume the pending resize flag")
	}
	if second.Content != first.Content {
		t.Fatalf("forced-dirty frame changed the content:\nfirst=%q\nsecond=%q", first.Content, second.Content)
	}
}

// An open status panel must not invalidate its own cache every frame: a
// second identical frame stays on the clean-frame path.
func TestStatusPanelSteadyFrameStaysClean(t *testing.T) {
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(newInfoPanelAgent(), 120, 30)
	t.Cleanup(func() { _ = m.Close() })
	m.openStatusPanel()
	// The main layer draws directly on the frame its content changes and
	// fills its cell cache on the next one; the third frame is steady.
	for range 3 {
		m.View()
	}
	if m.frameContentDirty {
		t.Fatal("steady status-panel frame was marked dirty")
	}
}

// The queue-bar fingerprint pins everything the bar renders: identity,
// delivery status, revision, and scalar shape. A revision bump (content
// change) must change the key even when the text length matches.
func TestQueuedDraftsFingerprintTracksRenderedInputs(t *testing.T) {
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(nil, 100, 30)
	t.Cleanup(func() { _ = m.Close() })

	m.queuedDrafts = []queuedDraft{{
		ID: "draft-1", InputID: "input-1", Revision: 1,
		Content: "sample queued text one", DisplayContent: "sample queued text one",
	}}
	base := m.queuedDraftsFingerprint(80, 4)

	// Same shape, new revision: the rendered text may differ, so the key must.
	m.queuedDrafts[0].Revision = 2
	m.queuedDrafts[0].Content = "sample queued text TWO"
	m.queuedDrafts[0].DisplayContent = "sample queued text TWO"
	if got := m.queuedDraftsFingerprint(80, 4); got == base {
		t.Fatal("revision bump did not change the queue fingerprint")
	}

	// Status changes render a prefixed line, so the key must track it.
	m.queuedDrafts[0].Revision = 2
	m.queuedDrafts[0].DeliveryStatus = "waiting"
	if got := m.queuedDraftsFingerprint(80, 4); got == base {
		t.Fatal("delivery status change did not change the queue fingerprint")
	}

	// A new draft changes membership and order.
	m.queuedDrafts = append(m.queuedDrafts, queuedDraft{
		ID: "draft-2", InputID: "input-2", Revision: 1,
		Content: "second", DisplayContent: "second",
	})
	if got := m.queuedDraftsFingerprint(80, 4); got == base {
		t.Fatal("queue membership change did not change the fingerprint")
	}
}
