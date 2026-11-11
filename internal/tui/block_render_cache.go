package tui

import (
	"encoding/binary"
	"iter"
	"slices"

	"github.com/klauspost/compress/s2"
)

const (
	renderCacheMinBytes   = 256 << 10
	renderCacheChunkLines = 128
	renderCacheHotChunks  = 3
)

// packedRenderCache keeps exact rendered bytes, so revisiting a long card does
// not parse Markdown or reconstruct its layout. Only a small window is decoded.
type packedRenderCache struct {
	width   int
	theme   uint64
	content string
	focused bool
	labelID int
	count   int
	chunks  [][]byte
	hot     [renderCacheHotChunks]renderCacheChunk
	next    int
	// hotDirty marks a decoded hot window whose memory footprint the block's
	// hot-bytes memo has not accounted for yet.
	hotDirty bool
}

type renderCacheChunk struct {
	index int
	lines []string
}

func (b *Block) packedRenderAt(width int) *packedRenderCache {
	cache := b.packedRender
	if cache == nil {
		return nil
	}
	if cache.width != width || cache.theme != appliedThemeVersion ||
		cache.content != b.Content || cache.focused != b.Focused ||
		cache.labelID != b.displayLabelID() || b.Streaming {
		b.packedRender = nil
		b.lineCache = nil
		b.lineCacheWidth = 0
		b.lineCountCache = 0
		b.viewportCache = nil
		b.hotBytesMemoValid = false
		return nil
	}
	return cache
}

func (b *Block) cacheRenderedLines(width int, lines []string) {
	b.lineCacheWidth = width
	b.lineCountCache = len(lines)
	b.hotBytesMemoValid = false
	if !b.Streaming && (b.Type == BlockAssistant || b.Type == BlockThinking) && len(lines) > renderCacheChunkLines {
		bytes := 0
		for _, line := range lines {
			bytes += len(line)
		}
		if bytes >= renderCacheMinBytes {
			cache := &packedRenderCache{
				width: width, theme: appliedThemeVersion, content: b.Content,
				focused: b.Focused, labelID: b.displayLabelID(), count: len(lines),
			}
			var raw, encoded []byte
			for start := 0; start < len(lines); start += renderCacheChunkLines {
				end := min(start+renderCacheChunkLines, len(lines))
				raw = raw[:0]
				for _, line := range lines[start:end] {
					raw = binary.AppendUvarint(raw, uint64(len(line)))
					raw = append(raw, line...)
				}
				encoded = s2.Encode(encoded[:0], raw)
				// Encode reserves the worst-case size; retaining that capacity
				// would keep nearly the entire uncompressed card allocated.
				cache.chunks = append(cache.chunks, slices.Clone(encoded))
			}
			// These caches describe the same card and would otherwise pin the
			// expanded ANSI strings alongside the packed final rows.
			b.mdCache = nil
			b.mdCacheWidth = 0
			b.mdCacheContent = ""
			b.mdCacheSyntheticPrefixWidths = nil
			b.mdCacheSoftWrapContinuations = nil
			b.codeHL = nil
			b.thinkingStreamSettled = nil
			b.lineCache = nil
			b.viewportCache = nil
			b.packedRender = cache
			return
		}
	}
	b.packedRender = nil
	b.lineCache = lines
}

func (c *packedRenderCache) lines(start, end int) []string {
	start = max(0, start)
	end = min(c.count, end)
	if end <= start {
		return nil
	}
	result := make([]string, 0, end-start)
	for first := start; first < end; {
		index := first / renderCacheChunkLines
		lines := c.chunk(index)
		offset := first % renderCacheChunkLines
		n := min(len(lines)-offset, end-first)
		result = append(result, lines[offset:offset+n]...)
		first += n
	}
	return result
}

func (c *packedRenderCache) chunk(index int) []string {
	for _, hot := range c.hot {
		if hot.lines != nil && hot.index == index {
			return hot.lines
		}
	}
	// The payload is produced only by cacheRenderedLines and never crosses an
	// IO boundary. A decoder failure is an internal invariant violation.
	raw, err := s2.Decode(nil, c.chunks[index])
	if err != nil {
		panic(err)
	}
	text := string(raw)
	lines := make([]string, 0, renderCacheChunkLines)
	for offset := 0; offset < len(raw); {
		size, n := binary.Uvarint(raw[offset:])
		offset += n
		lines = append(lines, text[offset:offset+int(size)])
		offset += int(size)
	}
	c.hot[c.next] = renderCacheChunk{index: index, lines: lines}
	c.next = (c.next + 1) % len(c.hot)
	c.hotDirty = true
	return lines
}

func (c *packedRenderCache) bytes() int {
	if c == nil {
		return 0
	}
	total := 0
	for _, chunk := range c.chunks {
		total += cap(chunk)
	}
	for _, hot := range c.hot {
		total += cap(hot.lines) * 16
		for _, line := range hot.lines {
			total += len(line) + binary.MaxVarintLen64
		}
	}
	return total
}

func (b *Block) renderedLineRange(width, start, end int) iter.Seq2[int, string] {
	return func(yield func(int, string) bool) {
		count := b.LineCount(width)
		throughEnd := end >= count
		end = min(end, count)
		for first := max(0, start); first < end; first += renderCacheChunkLines {
			lines := b.RenderRange(width, "", first, min(first+renderCacheChunkLines, end))
			if throughEnd && b.LineCount(width) != end {
				end = b.LineCount(width)
				lines = b.RenderRange(width, "", first, min(first+renderCacheChunkLines, end))
			}
			for i, line := range lines {
				if !yield(first+i, line) {
					return
				}
			}
		}
	}
}
