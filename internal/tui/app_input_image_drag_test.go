package tui

import (
	"testing"

	tea "github.com/keakon/bubbletea/v2"
)

func inputImageMouse(m *Model, offset int) tea.Mouse {
	return tea.Mouse{X: m.layout.input.Min.X + inputPromptWidth + offset, Y: m.layout.input.Min.Y + 1, Button: tea.MouseLeft}
}

func TestInputImageDragKeepsInitialObject(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendKitty)
	token := m.input.InlinePastes()[0]
	m.handleInputZoneMouseClick(inputImageMouse(m, token.Start+1), mouseHitZones{inInputZone: true})
	for _, span := range [][3]int{
		{token.Start - 3, token.Start - 3, token.End},
		{token.End + 3, token.Start, token.End + 3},
		{token.Start + 1, token.Start, token.End},
		{token.Start - 4, token.Start - 4, token.End},
	} {
		m.handleMouseMotion(inputImageMouse(m, span[0]), mouseHitZones{inInputZone: true})
		start, end, selected := m.input.SelectionRange()
		if !selected || start != span[1] || end != span[2] {
			t.Fatalf("pointer=%d range=[%d,%d), want=[%d,%d)", span[0], start, end, span[1], span[2])
		}
	}
	m.handleMouseRelease(inputImageMouse(m, token.Start+1))
	m.handleInputZoneMouseClick(inputImageMouse(m, token.Start+1), mouseHitZones{inInputZone: true})
	if m.imageViewer.Open {
		t.Fatal("drag completed a double click")
	}
}

func TestInputImageDoubleClickToleratesSmallMotion(t *testing.T) {
	for _, delta := range []int{0, 1, mouseClickTolerance} {
		m := newInputImagePreviewModel(t, ImageBackendKitty)
		token := m.input.InlinePastes()[0]
		mouse := inputImageMouse(m, token.Start+3)
		m.handleInputZoneMouseClick(mouse, mouseHitZones{inInputZone: true})
		version := m.input.interactionVersion
		mouse.X += delta
		m.handleMouseMotion(mouse, mouseHitZones{inInputZone: true})
		if m.input.interactionVersion != version || m.input.SelectionText() != token.DisplayText {
			t.Fatalf("motion=%d changed object selection", delta)
		}
		m.handleMouseRelease(mouse)
		m.handleInputZoneMouseClick(mouse, mouseHitZones{inInputZone: true})
		if !m.imageViewer.Open {
			t.Fatalf("motion=%d rejected double click", delta)
		}
		m.dismissImageViewer()
	}
}
