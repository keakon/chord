package agent

import (
	"sync"

	"github.com/keakon/chord/internal/message"
)

type inputCapability interface {
	SupportsInput(modality string) bool
}

type toolResultCapability interface {
	SupportsToolResultModalities(modalities []string) bool
}

type unsupportedPartCounts struct {
	Images int
	PDFs   int
}

func filterUnsupportedBinaryPartsForModel(messages []message.Message, capability inputCapability) ([]message.Message, unsupportedPartCounts) {
	if capability == nil {
		return messages, unsupportedPartCounts{}
	}
	filtered, counts := message.FilterUnsupportedBinaryParts(messages, capability.SupportsInput("image"), capability.SupportsInput("pdf"))
	return filtered, unsupportedPartCounts{Images: counts.Images, PDFs: counts.PDFs}
}

func (c unsupportedPartCounts) any() bool {
	return c.Images > 0 || c.PDFs > 0
}

func (c unsupportedPartCounts) summary() string {
	switch {
	case c.Images > 0 && c.PDFs > 0:
		return "image/PDF"
	case c.Images > 0:
		return "image"
	case c.PDFs > 0:
		return "PDF"
	default:
		return ""
	}
}

// toastGate deduplicates "unsupported input dropped" warnings per modelName so
// a model that lacks image/pdf support only notifies the user once per session
// per category+modality, even though the same historical parts are filtered on
// every LLM request. The key is modelName|category|summary; switching to another
// incapable model still produces a fresh warning for that model.
type toastGate struct {
	mu sync.Mutex
	// seen maps "modelName|category|summary" to true once a toast has been emitted.
	seen map[string]bool
}

// toast categories so user-input drops and tool-result drops stay independent.
const (
	toastCategoryInput      = "input"
	toastCategoryToolResult = "tool-result"
)

// first reports whether a toast for (modelName, category, summary) should be
// emitted, and remembers that it was. An empty modelName falls back to a
// placeholder so the gate still dedupes instead of reverting to per-request
// toasts; an empty summary is never deduped (callers only invoke this with a
// non-empty summary).
func (g *toastGate) first(modelName, category, summary string) bool {
	if summary == "" {
		return true
	}
	if modelName == "" {
		modelName = "unknown"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := modelName + "|" + category + "|" + summary
	if g.seen == nil {
		g.seen = make(map[string]bool)
	}
	if g.seen[key] {
		return false
	}
	g.seen[key] = true
	return true
}

// droppedSummary normalizes a list of dropped modality names ("image"/"pdf")
// into the same summary form used by unsupportedPartCounts.summary, so all
// call sites share one dedup key for the same fact.
func droppedSummary(dropped []string) string {
	counts := unsupportedPartCounts{}
	for _, d := range dropped {
		switch d {
		case "image":
			counts.Images++
		case "pdf":
			counts.PDFs++
		}
	}
	return counts.summary()
}
