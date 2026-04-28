package tui

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const defaultViewportHotBytes int64 = 32 << 20

type BlockSpillRef struct {
	File   string
	Offset int64
	Length int64
}

type ViewportSpillStore struct {
	mu         sync.Mutex
	file       *os.File
	path       string
	cleanupDir string
}

func newViewportSpillStore() (*ViewportSpillStore, error) {
	dir, err := os.MkdirTemp("", "chord-viewport-")
	if err != nil {
		return nil, err
	}
	return newViewportSpillStoreAt(filepath.Join(dir, "spill.log"), dir)
}

func newViewportSpillStoreAt(path string, cleanupDir string) (*ViewportSpillStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("spill path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &ViewportSpillStore{file: f, path: path, cleanupDir: cleanupDir}, nil
}

func (s *ViewportSpillStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	file := s.file
	cleanupDir := s.cleanupDir
	s.file = nil
	s.cleanupDir = ""
	s.mu.Unlock()

	var firstErr error
	if file != nil {
		if err := file.Close(); err != nil {
			firstErr = err
		}
	}
	if cleanupDir != "" {
		if err := os.RemoveAll(cleanupDir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *ViewportSpillStore) Append(block *Block) (*BlockSpillRef, error) {
	if s == nil || s.file == nil || block == nil {
		return nil, fmt.Errorf("spill store unavailable")
	}

	payloadBlock := *block
	payloadBlock.spillRef = nil
	payloadBlock.spillStore = nil
	payloadBlock.spillSummary = ""
	payloadBlock.spillLineCounts = nil
	payloadBlock.spillCold = false
	payloadBlock.lastAccess = 0
	payloadBlock.mdCache = nil
	payloadBlock.mdCacheWidth = 0
	payloadBlock.lineCache = nil
	payloadBlock.lineCacheWidth = 0
	payloadBlock.lineCountCache = 0
	payloadBlock.viewportCache = nil
	payloadBlock.viewportCacheWidth = 0
	payloadBlock.diffHL = nil

	payload, err := json.Marshal(&payloadBlock)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	offset, err := s.file.Seek(0, 2)
	if err != nil {
		return nil, err
	}

	var header [8]byte
	binary.LittleEndian.PutUint64(header[:], uint64(len(payload)))
	if _, err := s.file.Write(header[:]); err != nil {
		return nil, err
	}
	if _, err := s.file.Write(payload); err != nil {
		return nil, err
	}
	return &BlockSpillRef{
		File:   s.path,
		Offset: offset + 8,
		Length: int64(len(payload)),
	}, nil
}

func (s *ViewportSpillStore) Load(ref *BlockSpillRef) (*Block, error) {
	if s == nil || ref == nil {
		return nil, fmt.Errorf("spill ref unavailable")
	}
	data := make([]byte, ref.Length)

	s.mu.Lock()
	defer s.mu.Unlock()

	f := s.file
	if f == nil || s.path != ref.File {
		var err error
		f, err = os.Open(ref.File)
		if err != nil {
			return nil, err
		}
		defer f.Close()
	}

	if _, err := f.ReadAt(data, ref.Offset); err != nil {
		return nil, err
	}

	var block Block
	if err := json.Unmarshal(data, &block); err != nil {
		return nil, err
	}
	return &block, nil
}

func (b *Block) ensureMaterialized() error {
	if b == nil || !b.spillCold || b.spillStore == nil || b.spillRef == nil {
		return nil
	}

	loaded, err := b.spillStore.Load(b.spillRef)
	if err != nil {
		if b.tryRecoverFromSpillFailure() {
			return nil
		}
		if b.spillSummary == "" {
			b.spillSummary = b.fallbackSummary()
		}
		b.Content = b.spillSummary + "\n(content unavailable)"
		b.spillCold = false
		return err
	}

	preserveMutableBlockState(b, loaded)
	loaded.spillRef = b.spillRef
	loaded.spillStore = b.spillStore
	loaded.spillSummary = b.spillSummary
	loaded.spillLineCounts = b.spillLineCounts
	loaded.spillRecover = b.spillRecover
	loaded.spillCold = false
	loaded.lastAccess = b.lastAccess
	*b = *loaded
	return nil
}

func (b *Block) inspectionBlock() (*Block, bool) {
	if b == nil || !b.spillCold || b.spillStore == nil || b.spillRef == nil {
		return b, false
	}
	loaded, err := b.spillStore.Load(b.spillRef)
	if err != nil {
		if b.tryRecoverFromSpillFailure() {
			return b, false
		}
		return b, false
	}
	preserveMutableBlockState(b, loaded)
	loaded.spillStore = b.spillStore
	loaded.spillSummary = b.spillSummary
	loaded.spillLineCounts = b.spillLineCounts
	loaded.spillRecover = b.spillRecover
	return loaded, true
}

func (b *Block) tryRecoverFromSpillFailure() bool {
	if b == nil || b.spillRecover == nil {
		return false
	}
	recovered := b.spillRecover(b.ID)
	if recovered == nil {
		return false
	}
	*b = *recovered
	return true
}

func preserveMutableBlockState(src, dst *Block) {
	dst.Focused = src.Focused
	dst.Collapsed = src.Collapsed
	dst.ReadContentExpanded = src.ReadContentExpanded
	dst.ToolCallDetailExpanded = src.ToolCallDetailExpanded
	dst.ThinkingCollapsed = src.ThinkingCollapsed
	dst.Streaming = src.Streaming
	dst.UserLocalShellPending = src.UserLocalShellPending
	dst.UserLocalShellFailed = src.UserLocalShellFailed
	dst.StartedAt = src.StartedAt
	dst.SettledAt = src.SettledAt
	dst.CompactionSummaryRaw = src.CompactionSummaryRaw
	dst.CompactionPreviewLines = src.CompactionPreviewLines
}

func (b *Block) estimatedHotBytes() int64 {
	if b == nil || b.spillCold {
		return 0
	}
	var total int
	total += len(b.Content)
	total += len(b.ResultContent)
	total += len(b.Diff)
	total += len(b.UserLocalShellCmd)
	total += len(b.UserLocalShellResult)
	total += len(b.DoneSummary)
	for _, part := range b.ThinkingParts {
		total += len(part)
	}
	for _, img := range b.ImageParts {
		total += len(img.FileName)
		total += len(img.ImagePath)
		total += len(img.MimeType)
		total += len(img.Data)
	}
	for _, ref := range b.FileRefs {
		total += len(ref)
	}
	for _, line := range b.mdCache {
		total += len(line)
	}
	for _, line := range b.lineCache {
		total += len(line)
	}
	if b.lineCountCache > 0 {
		total += b.lineCountCache * 4
	}
	for _, line := range b.viewportCache {
		total += len(line)
	}
	if total < 512 {
		total = 512
	}
	return int64(total)
}

func (b *Block) fallbackSummary() string {
	if b == nil {
		return ""
	}
	if b.spillSummary != "" {
		return b.spillSummary
	}
	switch {
	case b.ToolName != "":
		return "Tool: " + b.ToolName
	case b.DoneSummary != "":
		return b.DoneSummary
	case b.Content != "":
		return truncateOneLine(strings.TrimSpace(b.Content), 60)
	default:
		return "(empty block)"
	}
}

func (v *Viewport) spillBlock(block *Block) bool {
	if v.spill == nil || block == nil || block.spillCold {
		return false
	}
	if block.spillSummary == "" {
		block.spillSummary = block.Summary()
	}
	if block.spillLineCounts == nil {
		block.spillLineCounts = make(map[int]int)
	}
	if block.lineCacheWidth > 0 && len(block.lineCache) > 0 {
		block.spillLineCounts[block.lineCacheWidth] = block.lineCountCache
	}
	if v.width > 0 {
		block.spillLineCounts[v.width] = block.LineCount(v.width)
	}

	size := block.estimatedHotBytes()
	ref, err := v.spill.Append(block)
	if err != nil {
		return false
	}
	block.spillRef = ref
	block.spillCold = true
	block.Content = ""
	block.ResultContent = ""
	block.Diff = ""
	block.UserLocalShellResult = ""
	block.ThinkingParts = nil
	block.mdCache = nil
	block.mdCacheWidth = 0
	block.lineCache = nil
	block.lineCacheWidth = 0
	block.lineCountCache = 0
	block.viewportCache = nil
	block.viewportCacheWidth = 0
	block.diffHL = nil
	v.hotBytes -= size
	if v.hotBytes < 0 {
		v.hotBytes = 0
	}
	return true
}
