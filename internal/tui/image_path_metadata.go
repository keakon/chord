package tui

import (
	"container/list"
	"fmt"
	"image"
	"strings"
	"sync"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

const imagePathMetadataMaxEntries = 256
const imagePathMetadataRefreshInterval = 2 * time.Second

// Layout reads only accepted metadata. Filesystem work runs in image commands,
// and the main loop publishes results after checking their viewport owner.
type imagePathMetadata struct {
	key       string
	config    image.Config
	err       error
	checkedAt time.Time
}

type imagePathMetadataUpdate struct {
	ref      imagePartKeyRef
	metadata imagePathMetadata
}

var imagePathMetadataCache = struct {
	sync.Mutex
	entries map[imagePartKeyRef]*list.Element
	order   list.List
}{entries: make(map[imagePartKeyRef]*list.Element)}

func imagePathMetadataSnapshot(part BlockImagePart) (imagePathMetadata, bool) {
	ref := imagePartKeyRefFor(part)
	imagePathMetadataCache.Lock()
	defer imagePathMetadataCache.Unlock()
	if e := imagePathMetadataCache.entries[ref]; e != nil {
		imagePathMetadataCache.order.MoveToBack(e)
		return e.Value.(imagePathMetadataUpdate).metadata, true
	}
	return imagePathMetadata{}, false
}

func publishImagePathMetadata(update imagePathMetadataUpdate) {
	imagePathMetadataCache.Lock()
	defer imagePathMetadataCache.Unlock()
	if e := imagePathMetadataCache.entries[update.ref]; e != nil {
		e.Value = update
		imagePathMetadataCache.order.MoveToBack(e)
		return
	}
	if len(imagePathMetadataCache.entries) >= imagePathMetadataMaxEntries {
		e := imagePathMetadataCache.order.Front()
		delete(imagePathMetadataCache.entries, e.Value.(imagePathMetadataUpdate).ref)
		imagePathMetadataCache.order.Remove(e)
	}
	imagePathMetadataCache.entries[update.ref] = imagePathMetadataCache.order.PushBack(update)
}

func loadImagePathMetadata(part BlockImagePart) imagePathMetadata {
	metadata := imagePathMetadata{checkedAt: time.Now()}
	metadata.key, metadata.err = imageRuntimeCacheKey(part)
	if metadata.err == nil {
		part.cacheKey = metadata.key
		var entry *imageRuntimeCacheEntry
		entry, metadata.err = imageRuntimeEntryForPart(part)
		if metadata.err == nil {
			metadata.config, _, metadata.err = entry.decodeConfig(part)
		}
	}
	return metadata
}

func imagePathMetadataChanged(old imagePathMetadata, present bool, next imagePathMetadata) bool {
	if !present || old.key != next.key || old.config.Width != next.config.Width || old.config.Height != next.config.Height {
		return true
	}
	if old.err == nil || next.err == nil {
		return (old.err == nil) != (next.err == nil)
	}
	return old.err.Error() != next.err.Error()
}

// imageInlineCacheKey never inspects a path on the render or update loop.
func imageInlineCacheKey(part BlockImagePart) (string, error) {
	if len(part.Data) > 0 {
		return imageRuntimeCacheKeyCached(part)
	}
	if strings.TrimSpace(part.ImagePath) == "" {
		return "", fmt.Errorf("image data unavailable")
	}
	if metadata, ok := imagePathMetadataSnapshot(part); ok {
		return metadata.key, nil
	}
	return "pending:" + part.ImagePath + ":" + part.MimeType, nil
}

func (m *Model) refreshImagePathMetadata() tea.Cmd {
	if m.mode == ModeImageViewer {
		return nil
	}
	requests, _ := m.inlineImageRequests()
	for _, req := range requests {
		if len(req.part.Data) == 0 && (!req.metadataPresent || time.Since(req.metadata.checkedAt) >= imagePathMetadataRefreshInterval) {
			return m.imageProtocolCmdWithReason("image-file-refresh")
		}
	}
	return nil
}
