package analytics

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// WalltimePurpose values mark wall-clock time bookkeeping events appended to
// usage.jsonl. They are zero-usage diagnostic events like the compaction
// lifecycle markers in purpose.go, so they stay out of token/cost aggregates
// and the "Calls" count while remaining inspectable in the ledger and
// rebuildable on resume.
const (
	WalltimePurposeModel      = "walltime/llm"
	WalltimePurposeCompaction = "walltime/llm_compaction"
	WalltimePurposeTool       = "walltime/tool"
	WalltimePurposeCooldown   = "walltime/cooldown"
	WalltimePurposeUserWait   = "walltime/user_wait"
)

// WalltimeDiagnosticNsKey is the Diagnostic map key carrying the segment
// duration in integer nanoseconds. Durations are persisted losslessly so a
// session resumed from the ledger shows the same accumulated buckets as the
// in-memory tracker.
const WalltimeDiagnosticNsKey = "ns"

// IsWalltimePurpose reports whether purpose identifies a wall-clock time
// bookkeeping event.
func IsWalltimePurpose(purpose string) bool {
	switch purpose {
	case WalltimePurposeModel,
		WalltimePurposeCompaction,
		WalltimePurposeTool,
		WalltimePurposeCooldown,
		WalltimePurposeUserWait:
		return true
	default:
		return false
	}
}

// WalltimeStats is one agent's cumulative wall-clock time distribution. The
// four display buckets mirror the TIME sidebar section. Compaction is
// tracked as an internal sub-total of Model and is not displayed separately.
type WalltimeStats struct {
	Model      time.Duration `json:"model_ns"`
	Tool       time.Duration `json:"tool_ns"`
	Cooldown   time.Duration `json:"cooldown_ns"`
	UserWait   time.Duration `json:"user_wait_ns"`
	Compaction time.Duration `json:"compaction_ns,omitempty"`
}

// IsZero reports whether every bucket is zero.
func (s WalltimeStats) IsZero() bool {
	return s.Model == 0 && s.Tool == 0 && s.Cooldown == 0 && s.UserWait == 0
}

// Add accumulates another stats value into the receiver.
func (s *WalltimeStats) Add(o WalltimeStats) {
	s.Model += o.Model
	s.Tool += o.Tool
	s.Cooldown += o.Cooldown
	s.UserWait += o.UserWait
	s.Compaction += o.Compaction
}

// WalltimeTracker accumulates wall-clock time segments per agent. All methods
// are goroutine-safe; the recorder goroutines (LLM stream callbacks, tool
// execution, compaction workers, interaction broker) call it concurrently.
type WalltimeTracker struct {
	mu      sync.RWMutex
	byAgent map[string]*WalltimeStats
}

// NewWalltimeTracker creates an empty tracker.
func NewWalltimeTracker() *WalltimeTracker {
	return &WalltimeTracker{byAgent: make(map[string]*WalltimeStats)}
}

// Record adds a segment to the given agent's bucket. The segment is already a
// settled wall-clock duration; recording never starts or ends a timer.
func (t *WalltimeTracker) Record(agentID string, purpose string, d time.Duration) {
	if t == nil || d <= 0 {
		return
	}
	agentID = normalizeAgentID(agentID)
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.byAgent[agentID]
	if s == nil {
		s = &WalltimeStats{}
		t.byAgent[agentID] = s
	}
	addWalltimeDuration(s, purpose, d)
}

func addWalltimeDuration(s *WalltimeStats, purpose string, d time.Duration) {
	if s == nil || d <= 0 {
		return
	}
	switch purpose {
	case WalltimePurposeModel:
		s.Model += d
	case WalltimePurposeCompaction:
		s.Model += d
		s.Compaction += d
	case WalltimePurposeTool:
		s.Tool += d
	case WalltimePurposeCooldown:
		s.Cooldown += d
	case WalltimePurposeUserWait:
		s.UserWait += d
	}
}

// StatsForAgent returns a snapshot for a single agent. Unknown agents yield a
// zero value.
func (t *WalltimeTracker) StatsForAgent(agentID string) WalltimeStats {
	if t == nil {
		return WalltimeStats{}
	}
	agentID = normalizeAgentID(agentID)
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.byAgent[agentID]
	if !ok || s == nil {
		return WalltimeStats{}
	}
	return *s
}

// RestoreStats replaces the per-agent map with the given snapshot. Used when
// switching or resuming sessions so the tracker reflects exactly that
// session's historical data.
func (t *WalltimeTracker) RestoreStats(snapshot map[string]*WalltimeStats) {
	if t == nil {
		return
	}
	restored := make(map[string]*WalltimeStats, len(snapshot))
	for agentID, s := range snapshot {
		if s == nil {
			continue
		}
		cp := *s
		restored[normalizeAgentID(agentID)] = &cp
	}
	t.mu.Lock()
	t.byAgent = restored
	t.mu.Unlock()
}

// parseDiagnosticNs extracts the "ns" diagnostic value, tolerating blank and
// foreign diagnostic maps. ParseUint rejects signs and digit strings too long
// for the range, so a corrupted ledger entry is dropped instead of wrapping
// around into a plausible-looking duration; bitSize 63 keeps the result safe to
// widen into the int64 time.Duration is built on.
func parseDiagnosticNs(diag map[string]string) (int64, bool) {
	if len(diag) == 0 {
		return 0, false
	}
	raw := strings.TrimSpace(diag[WalltimeDiagnosticNsKey])
	if raw == "" {
		return 0, false
	}
	ns, err := strconv.ParseUint(raw, 10, 63)
	if err != nil {
		return 0, false
	}
	return int64(ns), ns > 0
}
