package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/message"
)

// compactionFileReplay keeps request-only snapshots at their original history
// boundaries. It is a cache, never a recovery source: a new session or checkpoint
// starts from fresh permission-checked disk reads. The caller holds mu while
// reading files and assembling a request, so reset cannot race with publication.
type compactionFileReplay struct {
	mu         sync.Mutex
	checkpoint string
	root       string
	paths      []string
	versions   []compactionFileVersion
}

type compactionFileVersion struct {
	before  int
	anchor  string
	message message.Message
	bytes   int
}

func (r *compactionFileReplay) clear() {
	r.checkpoint, r.root = "", ""
	r.paths, r.versions = nil, nil
}

func (r *compactionFileReplay) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clear()
}

// Use durable tool/request identity where possible: request reduction may change
// a tool result's text without moving its boundary. For unkeyed user messages,
// a content change must invalidate the cached position. The hash covers only
// the model-visible identity: binary parts contribute their location and size,
// never their bytes, and pointer-valued metadata (whose addresses differ between
// copies of the same message) is left out.
func compactionFileAnchor(m message.Message) string {
	if m.Role == message.RoleTool && m.ToolCallID != "" {
		return "tool:" + m.ToolCallID
	}
	if m.Role == message.RoleAssistant && m.RequestBatch != 0 {
		return fmt.Sprintf("assistant:%d", m.RequestBatch)
	}
	h := sha256.New()
	field := func(s string) { fmt.Fprintf(h, "%d:%s;", len(s), s) }
	field(string(m.Role))
	field(m.Kind)
	field(m.Content)
	field(m.ToolCallID)
	for _, tc := range m.ToolCalls {
		field(tc.ID)
	}
	for _, p := range m.Parts {
		field(string(p.Type))
		if p.IsBinary() {
			field(p.MimeType)
			field(p.ImagePath)
			fmt.Fprintf(h, "%d;", p.PayloadBytes())
			continue
		}
		field(p.Text)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r *compactionFileReplay) replay(messages []message.Message, checkpoint int, signature, root string, current message.Message, bytes, budget int) ([]message.Message, int) {
	var paths []string
	for _, p := range current.Parts {
		paths = append(paths, message.FileRefPaths(p.Text)...)
	}
	// Dropped paths may reflect permissions, deletion, a symlink escape or a
	// tighter budget. Never replay their previously cached contents.
	reset := r.checkpoint != signature || r.root != root || !slices.Equal(r.paths, paths)
	total := 0
	for _, v := range r.versions {
		if v.before > len(messages) || v.before <= checkpoint || compactionFileAnchor(messages[v.before-1]) != v.anchor {
			reset = true
			break
		}
		total += v.bytes
	}
	changed := len(r.versions) == 0 || !reflect.DeepEqual(r.versions[len(r.versions)-1].message.Parts, current.Parts)
	// Bound both retained content and per-version metadata. At the bound a
	// fresh snapshot takes precedence over cache reuse; no stale copy survives.
	if total > budget || changed && (total+bytes > budget || len(r.versions) >= 8) {
		reset = true
	}
	if reset {
		r.clear()
		r.checkpoint, r.root, r.paths = signature, root, paths
	}
	if len(r.versions) == 0 || changed {
		before := len(messages)
		if len(r.versions) == 0 {
			before = checkpoint + 1
		}
		r.versions = append(r.versions, compactionFileVersion{
			before: before, anchor: compactionFileAnchor(messages[before-1]), message: current, bytes: bytes,
		})
	}
	out := make([]message.Message, 0, len(messages)+len(r.versions))
	start := 0
	for _, v := range r.versions {
		out = append(out, messages[start:v.before]...)
		snapshot := v.message
		snapshot.Parts = slices.Clone(v.message.Parts)
		out = append(out, snapshot)
		start = v.before
	}
	out = append(out, messages[start:]...)
	return out, r.versions[0].before
}

// Translate the prepared-history cache boundary to the request with all file
// snapshots inserted. Tail snapshots at the boundary are not part of the prefix.
func compactionFileContextPrefixCount(messages []message.Message, stableLen int) int {
	count, raw := 0, 0
	for _, m := range messages {
		if raw >= stableLen {
			break
		}
		if m.Kind == message.KindTurnOverlay && len(m.Parts) > 0 && strings.HasPrefix(m.Parts[0].Text, compactionFileCtxPrefix) {
			count++
		} else {
			raw++
		}
	}
	return count
}
