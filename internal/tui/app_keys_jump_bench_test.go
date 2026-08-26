package tui

import "testing"

func BenchmarkCountedStructuralJump(b *testing.B) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	const blockCount = 10_000
	for i := range blockCount {
		blockType := BlockAssistant
		if i%4 == 0 {
			blockType = BlockUser
		}
		m.viewport.AppendBlock(&Block{ID: i, Type: blockType, Content: "message"})
	}
	firstID := m.viewport.blocks[0].ID
	match := blockTypeMatch(BlockUser)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.focusedBlockID = firstID
		_ = m.jumpToMatchingBlock(1, 9999, match)
	}
}
