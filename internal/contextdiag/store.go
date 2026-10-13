package contextdiag

import (
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	MaxMessages   = 4096
	MaxFragments  = 16384
	Dispatched    = "dispatched"
	Completed     = "completed"
	Failed        = "failed"
	Cancelled     = "cancelled"
	Superseded    = "superseded"
	Missing       = "missing"
	Reported      = "reported"
	Partial       = "partial"
	Observed      = "observed"
	Disabled      = "disabled"
	Unavailable   = "unavailable"
	Estimated     = "estimated"
	ProviderInput = "provider_input"
	UTF8BytesDiv3 = "utf8_bytes_div3"
	First         = "first"
	Comparable    = "comparable"
	DomainChanged = "domain_changed"
	Same          = "same"
	Append        = "append"
	Truncate      = "truncate"
	Rewrite       = "rewrite"
	Unknown       = "unknown"
	ReadObserved  = "read_observed"
	WriteOnly     = "write_only"
	NoReport      = "no_report"
	HTTP          = "http"
	WebSocket     = "websocket"
	Custom        = "custom"
	System        = "system"
	Tools         = "tools"
	Instructions  = "instructions"
	Conversation  = "conversation"
	ToolResults   = "tool_results"
	Runtime       = "runtime"
	Attachments   = "attachments"
	Messages      = "messages"
	Tool          = "tool"
	Skill         = "skill"
)

// Store retains only the most recently dispatched attempt. All returned slices
// are owned by the caller; invalidation never revives an older binding.
type Store struct {
	mu                     sync.Mutex
	binding                Binding
	bound, invalid         bool
	seq, attempt, revision uint64
	latest                 Snapshot
	hasLatest              bool
}

func (s *Store) BeginRequest(binding Binding, generation uint64) RequestID {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.bound {
		s.binding, s.bound = binding, true
	}
	if s.invalid || s.binding != binding {
		return RequestID{}
	}
	s.seq++
	s.attempt = 0
	return RequestID{Binding: binding, Seq: s.seq}
}

func (s *Store) Publish(snapshot Snapshot) bool {
	snapshot = Clone(snapshot)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalid || !s.bound || snapshot.ID.Binding != s.binding || snapshot.ID.Seq != s.seq || snapshot.ID.Attempt <= s.attempt {
		return false
	}
	s.attempt = snapshot.ID.Attempt
	s.revision++
	snapshot.Revision = s.revision
	s.latest, s.hasLatest = snapshot, true
	return true
}

func (s *Store) Finish(id RequestID, terminal string, usage Usage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalid || !s.hasLatest || id != s.latest.ID || id.Seq != s.seq || s.latest.State != Dispatched {
		return false
	}
	switch terminal {
	case Completed, Failed, Cancelled, Superseded:
	default:
		return false
	}
	s.revision++
	s.latest.State, s.latest.Usage, s.latest.FinishedAt, s.latest.Revision = terminal, usage, time.Now(), s.revision
	s.latest.Cache.Explanation = NoReport
	if usage.CacheRead > 0 {
		s.latest.Cache.Explanation = ReadObserved
	} else if usage.CacheWrite > 0 {
		s.latest.Cache.Explanation = WriteOnly
	}
	return true
}

func (s *Store) Latest() (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Clone(s.latest), s.hasLatest
}

func (s *Store) Invalidate(binding Binding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.bound || s.binding == binding {
		s.invalid = true
	}
}

func Clone(s Snapshot) Snapshot {
	s.Categories = slices.Clone(s.Categories)
	for i := range s.Categories {
		s.Categories[i].Sources = slices.Clone(s.Categories[i].Sources)
	}
	s.Limitations = slices.Clone(s.Limitations)
	s.Reduction = CloneReduction(s.Reduction)
	s.Cache.LocalChanges = slices.Clone(s.Cache.LocalChanges)
	return s
}

func CloneReduction(r Reduction) Reduction {
	r.Reasons = slices.Clone(r.Reasons)
	r.Rules = slices.Clone(r.Rules)
	return r
}

// DisplayLabel bounds display-only labels without retaining the source string.
func DisplayLabel(s string) string {
	var b strings.Builder
	n, scanned := 0, 0
	for _, r := range s {
		scanned++
		if n == 80 || scanned > 160 {
			break
		}
		if !unicode.IsControl(r) {
			b.WriteRune(r)
			n++
		}
	}
	return b.String()
}
