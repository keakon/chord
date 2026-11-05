package tui

import (
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tui/imageprotocol"
)

func (b *Block) prepareHistoryAttachments() {
	for i := range b.ImageParts {
		part := &b.ImageParts[i]
		part.Data = nil
		part.ImagePath = ""
		part.MetadataOnly = true
	}
}

// Terminal images outlive a text repaint. Remove our placements while keeping
// the live viewer's items and prepared layout owned by that viewer.
func (m *Model) suspendHistoryImages() tea.Cmd {
	m.cancelInlineImages()
	v := &m.imageViewer
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	m.imageViewerGeneration++
	v.Loading = false
	v.NeedsRetransmit = true
	switch m.imageCaps.Backend {
	case ImageBackendKitty:
		var seq strings.Builder
		for id := range m.kittyImageCache {
			seq.WriteString(imageprotocol.KittyDeleteImage(id))
		}
		if v.ImageID > 0 && !m.kittyImageTransmitted(v.ImageID) {
			seq.WriteString(imageprotocol.KittyDeleteImage(v.ImageID))
		}
		clear(m.kittyImageCache)
		m.resetKittyPlacements()
		if v.Prepared != nil {
			v.Prepared.Sequence = ""
		}
		if seq.Len() > 0 {
			return tea.Raw(seq.String())
		}
	case ImageBackendITerm2:
		return tea.ClearScreen
	}
	return nil
}

func (m *Model) restoreHistoryImages() tea.Cmd {
	if m.imageViewer.Open && m.mode == ModeImageViewer && m.imageViewer.Prepared == nil && m.imageViewer.Error == "" {
		return m.prepareImageViewer()
	}
	return m.imageProtocolCmdWithReason("history-closed")
}
