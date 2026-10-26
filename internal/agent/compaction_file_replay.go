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

// Earlier snapshot versions are retained only for a provider/model that has
// already observed cache hits: those hits establish that prefix reuse is
// available. Model pricing is not consulted; byte and version limits bound the
// extra history, without predicting future request counts.
func (a *MainAgent) checkpointFileReplayAllowed(modelRef string) bool {
	if a == nil || a.cacheHitTracker == nil {
		return false
	}
	rate, observed := a.cacheHitTracker.HitRate(modelRef)
	return observed && rate > 0
}

func isCompactionFileSnapshot(m message.Message) bool {
	return m.Kind == message.KindTurnOverlay && len(m.Parts) > 0 && strings.HasPrefix(m.Parts[0].Text, compactionFileCtxPrefix)
}

// Keep the latest complete bundle in its original position; never move an
// overlay into an assistant tool-call/result pair during provider fallback.
func latestCompactionFileSnapshotOnly(messages []message.Message) []message.Message {
	latest, count := -1, 0
	for i, m := range messages {
		if isCompactionFileSnapshot(m) {
			latest, count = i, count+1
		}
	}
	if count <= 1 {
		return messages
	}
	out := make([]message.Message, 0, len(messages)-count+1)
	for i, m := range messages {
		if i == latest || !isCompactionFileSnapshot(m) {
			out = append(out, m)
		}
	}
	return out
}

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

func (r *compactionFileReplay) replay(messages []message.Message, checkpoint int, signature, root string, current message.Message, bytes, budget int) []message.Message {
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
	if reset {
		r.clear()
		r.checkpoint, r.root, r.paths = signature, root, paths
	}
	changed := len(r.versions) == 0 || !reflect.DeepEqual(r.versions[len(r.versions)-1].message.Parts, current.Parts)
	if changed {
		switch {
		case len(r.versions) == 0:
			// Nothing after a fresh checkpoint is cached yet, so the bundle can
			// sit directly behind it without replacing a published item.
			r.versions = append(r.versions, compactionFileVersion{
				before: checkpoint + 1, anchor: compactionFileAnchor(messages[checkpoint]), message: current, bytes: bytes,
			})
		case total+bytes <= budget && len(r.versions) < compactionInjectedFilesMaxVersions:
			// Appending at the tail leaves every earlier byte in place, so the
			// prefix the provider already cached keeps matching.
			r.versions = append(r.versions, compactionFileVersion{
				before: len(messages), anchor: compactionFileAnchor(messages[len(messages)-1]), message: current, bytes: bytes,
			})
		default:
			// The bundle is at its byte or version bound. Refresh the newest
			// published position in place instead of restarting the bundle
			// behind the checkpoint: rewriting that early item invalidates
			// every message after it, while the newest position only costs the
			// messages appended since it was published. Older versions stay
			// byte-identical, so they keep hitting the provider cache, and only
			// trailing versions are dropped as far as the refreshed bundle
			// needs room.
			for len(r.versions) > 1 && total-r.versions[len(r.versions)-1].bytes+bytes > budget {
				total -= r.versions[len(r.versions)-1].bytes
				r.versions = r.versions[:len(r.versions)-1]
			}
			last := &r.versions[len(r.versions)-1]
			last.message, last.bytes = current, bytes
		}
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
	return out
}

// Translate the prepared-history cache boundary to the request with all file
// snapshots inserted. Tail snapshots at the boundary are not part of the prefix.
func compactionFileContextPrefixCount(messages []message.Message, stableLen int) int {
	count, raw := 0, 0
	for _, m := range messages {
		if raw >= stableLen {
			break
		}
		if isCompactionFileSnapshot(m) {
			count++
		} else {
			raw++
		}
	}
	return count
}
