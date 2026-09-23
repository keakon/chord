package agent

import (
	"container/list"
	"fmt"
	"os"
	"sync"

	"github.com/keakon/chord/internal/imageutil"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// binaryPartCacheEntry is one cached attachment payload. Data and MIME are one
// inseparable value: the MIME type must always describe the cached bytes,
// because normalization rewrites both together.
type binaryPartCacheEntry struct {
	path string
	data []byte
	mime string
}

// binaryPartReadCache is a bounded LRU of lazily resolved attachment payloads,
// keyed by the persisted file path. Attachment files are write-once (their
// names carry a nanosecond timestamp), so entries never need invalidation;
// the budget only bounds how many recently used blobs stay resident for the
// wire converter, which re-reads the same attachments on every request of a
// turn.
// The recency order is a container/list, so a hit is O(1). A slice of paths
// costs a linear scan per hit, and this cache is keyed by attachment rather
// than by byte budget: a session with many small attachments accumulates
// entries far past what a per-request scan should pay for.
type binaryPartReadCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element // path → element holding *binaryPartCacheEntry
	order   *list.List               // front = least recently used
	bytes   int64
	budget  int64
}

const binaryPartReadCacheBudget = 64 << 20 // 64 MiB

func newBinaryPartReadCache() *binaryPartReadCache {
	return &binaryPartReadCache{
		entries: make(map[string]*list.Element),
		order:   list.New(),
		budget:  binaryPartReadCacheBudget,
	}
}

func (c *binaryPartReadCache) get(path string) (*binaryPartCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[path]
	if !ok {
		return nil, false
	}
	c.order.MoveToBack(elem)
	return elem.Value.(*binaryPartCacheEntry), true
}

func (c *binaryPartReadCache) put(entry *binaryPartCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[entry.path]; ok {
		c.order.MoveToBack(elem)
		return
	}
	if int64(len(entry.data)) > c.budget {
		return
	}
	for c.bytes+int64(len(entry.data)) > c.budget && c.order.Len() > 0 {
		oldest := c.order.Front()
		evicted := oldest.Value.(*binaryPartCacheEntry)
		c.order.Remove(oldest)
		delete(c.entries, evicted.path)
		c.bytes -= int64(len(evicted.data))
	}
	c.entries[entry.path] = c.order.PushBack(entry)
	c.bytes += int64(len(entry.data))
}

// resolveBinaryPart loads and normalizes a persisted attachment for the wire
// converter. The resolver is installed once per process; the paths inside a
// part are absolute session paths, so reads need no session-dir context and
// stay valid across a session switch for messages that still reference the
// previous session.
//
// The returned MIME type always describes the returned bytes: images are
// normalized here so every wire sees a provider-ready PNG/JPEG pair, while
// PDFs pass through unchanged.
func (a *MainAgent) resolveBinaryPart(part message.ContentPart) ([]byte, string, error) {
	path := part.ImagePath
	if path == "" {
		return nil, "", fmt.Errorf("binary part has no image path")
	}
	if a.binaryPartCache != nil {
		if entry, ok := a.binaryPartCache.get(path); ok {
			return entry.data, entry.mime, nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read attachment: %w", err)
	}
	mime := part.MimeType
	if part.Type == message.ContentPartImage {
		data, mime, err = imageutil.NormalizeImageBytes(data, part.MimeType)
		if err != nil {
			return nil, "", fmt.Errorf("normalize attachment: %w", err)
		}
	}
	if a.binaryPartCache != nil {
		a.binaryPartCache.put(&binaryPartCacheEntry{path: path, data: data, mime: mime})
	}
	return data, mime, nil
}

// noteBinaryPartDrop records a binary part the wire layer had to omit so the
// user learns about it. The agent owns counting and toasts; internal/llm only
// reports the drop.
func (a *MainAgent) noteBinaryPartDrop(part message.ContentPart, err error) {
	counts := unsupportedPartCounts{}
	switch part.Type {
	case message.ContentPartImage:
		counts.Images = 1
	case message.ContentPartPDF:
		counts.PDFs = 1
	}
	if !counts.any() {
		return
	}
	a.llmMu.RLock()
	modelName := a.modelName
	a.llmMu.RUnlock()
	if a.unsupportedPartToast.first(modelName, toastCategoryInput, counts.summary()) {
		a.emitToTUI(ToastEvent{
			Message: "An attachment could not be prepared for the current model and was ignored: " + err.Error(),
			Level:   "warn",
		})
	}
}

func (a *MainAgent) installBinaryPartResolver() {
	a.binaryPartCache = newBinaryPartReadCache()
	llm.SetBinaryPartResolver(a.resolveBinaryPart)
	llm.SetBinaryPartDropReporter(a.noteBinaryPartDrop)
}
