package tui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

func fileInlineImageTestModel(t *testing.T, backend ImageBackend) (Model, string) {
	t.Helper()
	m := inlineImageTestModel(t, backend)
	path := filepath.Join(t.TempDir(), "sample.jpg")
	part := &m.viewport.blocks[0].ImageParts[0]
	if err := os.WriteFile(path, part.Data, 0600); err != nil {
		t.Fatal(err)
	}
	part.ImagePath, part.Data = path, nil
	m.viewport.blocks[0].InvalidateCache()
	m.viewport.InvalidateBlock(m.viewport.blocks[0].ID)
	return m, path
}

func runInlineImageCommands(t *testing.T, m *Model, cmd tea.Cmd) []int {
	t.Helper()
	var ids []int
	for steps := 0; cmd != nil; steps++ {
		if steps > 8 {
			t.Fatal("image preparation never settled")
		}
		switch msg := cmd().(type) {
		case inlineImagesLoadedMsg:
			ids = append(ids, msg.imageIDs...)
			cmd = m.handleInlineImagesLoaded(msg)
		case tea.RawMsg:
			return ids
		default:
			t.Fatalf("unexpected image command: %T", msg)
		}
	}
	return ids
}

func TestPathImageForegroundUsesOnlyAcceptedMetadata(t *testing.T) {
	for _, backend := range []ImageBackend{ImageBackendKitty, ImageBackendITerm2} {
		t.Run(backend.String(), func(t *testing.T) {
			m, _ := fileInlineImageTestModel(t, backend)
			defer m.Close()
			stat, open := imageCacheStat, imageCacheOpen
			foreground := true
			calls := 0
			imageCacheStat = func(path string) (os.FileInfo, error) {
				if foreground {
					t.Error("file stat ran on foreground")
				}
				calls++
				return stat(path)
			}
			imageCacheOpen = func(path string) (*os.File, error) {
				if foreground {
					t.Error("file header read ran on foreground")
				}
				return open(path)
			}
			defer func() { imageCacheStat, imageCacheOpen = stat, open }()
			_ = m.View()
			cmd := m.imageProtocolCmd()
			if cmd == nil || calls != 0 {
				t.Fatal("cold layout inspected file or skipped preparation")
			}
			for steps := 0; cmd != nil; steps++ {
				if steps > 8 {
					t.Fatal("image never settled")
				}
				foreground = false
				msg := cmd()
				foreground = true
				switch result := msg.(type) {
				case inlineImagesLoadedMsg:
					cmd = m.handleInlineImagesLoaded(result)
				case tea.RawMsg:
					cmd = nil
				default:
					t.Fatalf("unexpected command: %T", result)
				}
			}
			before := calls
			_ = m.View()
			_, _ = m.inlineImageRequests()
			_ = m.visibleKittyImageIDs()
			if calls != before {
				t.Fatal("warm layout inspected file")
			}
			if calls == 0 {
				t.Fatal("background never checked file")
			}
		})
	}
}

func TestPathImageReplacementRefreshesDimensionsAndKittyIdentity(t *testing.T) {
	m, path := fileInlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	ids := runInlineImageCommands(t, &m, m.imageProtocolCmd())
	if len(ids) != 1 {
		t.Fatalf("initial image IDs: %v", ids)
	}
	old := ids[0]
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 10, 40)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	part := m.viewport.blocks[0].ImageParts[0]
	metadata, ok := imagePathMetadataSnapshot(part)
	if !ok {
		t.Fatal("missing accepted metadata")
	}
	metadata.checkedAt = time.Now().Add(-imagePathMetadataRefreshInterval)
	publishImagePathMetadata(imagePathMetadataUpdate{ref: imagePartKeyRefFor(part), metadata: metadata})
	ids = runInlineImageCommands(t, &m, m.imageProtocolCmd())
	metadata, _ = imagePathMetadataSnapshot(part)
	if metadata.config.Width != 10 || metadata.config.Height != 40 || len(ids) != 1 || ids[0] == old {
		t.Fatalf("replacement retained dimensions or terminal identity: config=%+v IDs=%v old=%d", metadata.config, ids, old)
	}
	if rendered := m.viewport.blocks[0].ImageParts[0].RenderImageID; rendered != ids[0] {
		t.Fatalf("placeholder=%d transmitted=%d", rendered, ids[0])
	}
}

func TestPathImageMetadataRejectsStaleOwnerAndRecoversMissingFile(t *testing.T) {
	m, path := fileInlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	cmd := m.imageProtocolCmd()
	msg := cmd().(inlineImagesLoadedMsg)
	m.sessionTranscriptEpoch++
	_ = m.handleInlineImagesLoaded(msg)
	if _, ok := imagePathMetadataSnapshot(m.viewport.blocks[0].ImageParts[0]); ok {
		t.Fatal("old session published metadata")
	}
	m.cancelInlineImages()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	runInlineImageCommands(t, &m, m.imageProtocolCmd())
	part := m.viewport.blocks[0].ImageParts[0]
	metadata, ok := imagePathMetadataSnapshot(part)
	if !ok || metadata.err == nil {
		t.Fatal("missing file was not recorded")
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 20, 10)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	metadata.checkedAt = time.Now().Add(-imagePathMetadataRefreshInterval)
	publishImagePathMetadata(imagePathMetadataUpdate{ref: imagePartKeyRefFor(part), metadata: metadata})
	ids := runInlineImageCommands(t, &m, m.imageProtocolCmd())
	if len(ids) != 1 {
		t.Fatalf("file recovery did not transmit: %v", ids)
	}
}

func TestPathImageMetadataCacheIsBounded(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	for i := range imagePathMetadataMaxEntries + 1 {
		publishImagePathMetadata(imagePathMetadataUpdate{ref: imagePartKeyRefFor(BlockImagePart{ImagePath: fmt.Sprintf("/sample/%d", i)}), metadata: imagePathMetadata{key: fmt.Sprint(i)}})
	}
	imagePathMetadataCache.Lock()
	defer imagePathMetadataCache.Unlock()
	if len(imagePathMetadataCache.entries) != imagePathMetadataMaxEntries {
		t.Fatal("metadata cache exceeded bound")
	}
}

func TestPathImagePaletteMetadataCanBeRefreshed(t *testing.T) {
	m, path := fileInlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, image.NewPaletted(image.Rect(0, 0, 20, 10), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	m.viewport.blocks[0].ImageParts[0].MimeType = "image/gif"
	m.viewport.blocks[0].InvalidateCache()
	ids := runInlineImageCommands(t, &m, m.imageProtocolCmd())
	if len(ids) != 1 {
		t.Fatalf("palette image did not transmit: %v", ids)
	}
	part := m.viewport.blocks[0].ImageParts[0]
	metadata, _ := imagePathMetadataSnapshot(part)
	metadata.checkedAt = time.Now().Add(-imagePathMetadataRefreshInterval)
	publishImagePathMetadata(imagePathMetadataUpdate{ref: imagePartKeyRefFor(part), metadata: metadata})
	runInlineImageCommands(t, &m, m.imageProtocolCmd())
}
