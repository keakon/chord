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

	"github.com/keakon/chord/internal/tui/imagecache"
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
			m, path := fileInlineImageTestModel(t, backend)
			defer m.Close()
			part := m.viewport.blocks[0].ImageParts[0]
			// Cold rendering must leave source inspection to the returned command.
			_ = m.View()
			cmd := m.imageProtocolCmd()
			if _, present := imagePathMetadataSnapshot(part); present || cmd == nil {
				t.Fatal("cold layout published metadata or skipped preparation")
			}
			runInlineImageCommands(t, &m, cmd)
			accepted, present := imagePathMetadataSnapshot(part)
			if !present || accepted.err != nil {
				t.Fatal("background did not publish dimensions")
			}
			beforeKey, err := imageInlineCacheKey(part)
			if err != nil {
				t.Fatal(err)
			}
			beforeCols, beforeRows, err := imageRenderSize(part, 40, m.imageCaps)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			// A missing source is observable only after background metadata acceptance.
			// Foreground layout and identity still use the accepted version.
			_ = m.View()
			_, _ = m.inlineImageRequests()
			_ = m.visibleKittyImageIDs()
			key, err := imageInlineCacheKey(part)
			cols, rows, sizeErr := imageRenderSize(part, 40, m.imageCaps)
			after, present := imagePathMetadataSnapshot(part)
			if err != nil || sizeErr != nil || key != beforeKey || cols != beforeCols || rows != beforeRows || !present || after.key != accepted.key || after.err != nil {
				t.Fatal("foreground inspected the changed source instead of accepted metadata")
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
	publishImagePathMetadata(imagePathMetadataUpdate{ref: imagecache.IdentityOf(imageSource(part)), metadata: metadata})
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
	publishImagePathMetadata(imagePathMetadataUpdate{ref: imagecache.IdentityOf(imageSource(part)), metadata: metadata})
	ids := runInlineImageCommands(t, &m, m.imageProtocolCmd())
	if len(ids) != 1 {
		t.Fatalf("file recovery did not transmit: %v", ids)
	}
}

func TestPathImageMetadataCacheIsBounded(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	for i := range imagePathMetadataMaxEntries + 1 {
		publishImagePathMetadata(imagePathMetadataUpdate{ref: imagecache.IdentityOf(imageSource(BlockImagePart{ImagePath: fmt.Sprintf("/sample/%d", i)})), metadata: imagePathMetadata{key: fmt.Sprint(i)}})
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
	publishImagePathMetadata(imagePathMetadataUpdate{ref: imagecache.IdentityOf(imageSource(part)), metadata: metadata})
	runInlineImageCommands(t, &m, m.imageProtocolCmd())
}
