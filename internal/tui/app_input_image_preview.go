package tui

import (
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tui/composer"
	"github.com/keakon/chord/internal/tui/imagecache"
)

// Identity includes source content and binding. A renamed/reindexed token or a
// different composer cannot finish an earlier object's double click.
type inputImageClickState struct {
	Owner      imageViewerOwner
	Version    uint64
	Token      composer.Token
	Attachment imagecache.Identity
	Time       time.Time
	Dragging   bool
}

func (m *Model) inputImageAt(offset int, hit bool) (composer.Token, Attachment, bool) {
	if !hit {
		return composer.Token{}, Attachment{}, false
	}
	token, ok := m.input.TokenAt(offset)
	if !ok || token.Kind != composer.TokenImage {
		return composer.Token{}, Attachment{}, false
	}
	ordinal, ok := composer.InlineImagePlaceholderIndex(token.RawContent)
	if !ok {
		return composer.Token{}, Attachment{}, false
	}
	index, ok := imageAttachmentIndex(m.attachments, ordinal)
	if !ok {
		return composer.Token{}, Attachment{}, false
	}
	att := m.attachments[index]
	if !strings.HasPrefix(att.MimeType, "image/") {
		return composer.Token{}, Attachment{}, false
	}
	return token, att, true
}

func attachmentPreviewPart(att Attachment) BlockImagePart {
	return BlockImagePart{FileName: att.FileName, MimeType: att.MimeType, Data: att.Data, ImagePath: att.ImagePath}
}

func (m *Model) inputImageSnapshot(target composer.Token) ([]BlockImagePart, int) {
	parts := make([]BlockImagePart, 0, len(m.attachments))
	index := -1
	for _, token := range m.input.InlinePastes() {
		current, att, ok := m.inputImageAt(token.Start, true)
		if !ok {
			continue
		}
		if current == target {
			index = len(parts)
		}
		parts = append(parts, attachmentPreviewPart(att))
	}
	return parts, index
}

func (m *Model) handleInputImageClick(mouse tea.Mouse, offset int, hit bool) (tea.Cmd, bool) {
	token, att, ok := m.inputImageAt(offset, hit)
	if !ok {
		m.inputImageClick = inputImageClickState{}
		return nil, false
	}
	now := time.Now()
	owner := m.imageViewerOwner()
	identity := imagecache.IdentityOf(imageSource(attachmentPreviewPart(att)))
	previous := m.inputImageClick
	same := !previous.Dragging && previous.Owner == owner && previous.Version == m.input.Revision().Interaction && previous.Token == token && previous.Attachment == identity && now.Sub(previous.Time) <= doubleClickThreshold
	m.inputImageClick = inputImageClickState{Owner: owner, Token: token, Attachment: identity, Time: now}
	m.inputClickCount = 0
	m.inputLastClickTime = time.Time{}
	m.inputLastClickX, m.inputLastClickY = mouse.X, mouse.Y
	m.clearMouseSelection()
	// Restore the caret that existed before object selection when closing preview.
	cursor := composer.RuneOffsetFromRowCol(m.input.Value(), m.input.Line(), m.input.Column())
	m.input.SelectRuneRange(token.Start, token.End)
	m.inputImageClick.Version = m.input.Revision().Interaction
	m.inputMouseDown = true
	if !same {
		return nil, true
	}
	m.inputMouseDown = false
	m.inputImageClick = inputImageClickState{}
	parts, index := m.inputImageSnapshot(token)
	if index < 0 {
		return nil, true
	}
	if len(att.Data) == 0 && strings.TrimSpace(att.ImagePath) == "" {
		return m.enqueueToast("Image data is not available yet", "info"), true
	}
	// Copying descriptors keeps admission-owned byte slices immutable and shared.
	cmd := m.openImageViewerItems(parts, index, ModeInsert)
	if m.imageViewer.Open {
		m.imageViewer.Cursor = cursor
	}
	return cmd, true
}

func (m *Model) handleInputImageDrag(mouse tea.Mouse, offset int) bool {
	click := &m.inputImageClick
	if click.Time.IsZero() || click.Owner != m.imageViewerOwner() || click.Version != m.input.Revision().Interaction {
		m.inputImageClick = inputImageClickState{}
		return false
	}
	if !click.Dragging {
		if abs(mouse.X-m.inputLastClickX) <= mouseClickTolerance && abs(mouse.Y-m.inputLastClickY) <= mouseClickTolerance {
			return true
		}
		click.Dragging = true
	}
	// Keep the initially selected object inside the range in either direction,
	// including when the pointer crosses back through it during the same drag.
	switch {
	case offset < click.Token.Start:
		m.input.SelectRuneRange(click.Token.End, offset)
	case offset > click.Token.End:
		m.input.SelectRuneRange(click.Token.Start, offset)
	default:
		m.input.SelectRuneRange(click.Token.Start, click.Token.End)
	}
	click.Version = m.input.Revision().Interaction
	return true
}
