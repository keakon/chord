package lsp

import (
	"context"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
)

// lateDiagnosticsWindow bounds how long after a post-write wait returned a
// publish for the same document version still counts as part of that write.
// gopls publishes its analysis pass (staticcheck and analyzers) about
// diagnosticsDelay plus analysis time after the type-check pass the wait
// returned on; a few seconds on large packages.
const lateDiagnosticsWindow = 15 * time.Second

// LateDiagnosticsTurnEndWindow is how long after a file's post-write wait
// returned a turn that is about to end still waits for that file's analysis
// pass. Measured gopls analysis passes land 1-4s after the type-check pass;
// the wait is counted from the write, so a reply that took longer to write
// than this costs nothing.
const LateDiagnosticsTurnEndWindow = 3 * time.Second

// lateWatchRetention drops watches nobody claimed. A watch is otherwise kept
// until the same path is synchronized again, which replaces it.
const lateWatchRetention = 10 * time.Minute

// lateWatch follows one post-write wait after it returned. Only Chord-synced
// versions are trusted: a publish counts when its server and version match the
// sync the wait verified, so an external change to the file can never be
// reported as fallout from this write.
type lateWatch struct {
	seq      uint64
	versions map[string]int32
	// expectsLate marks a watch whose servers publish a later analysis pass
	// for the same version (gopls); only such watches hold a turn open.
	expectsLate bool
	// baseline holds every diagnostic the path had when the wait returned
	// plus every diagnostic already delivered as late; only identities outside
	// it are reported.
	baseline   map[diagnosticIdentity]struct{}
	registered time.Time
	deadline   time.Time
	// lateGen counts same-version publishes received before deadline. A peek
	// records it and the matching ack clears pending only when no newer
	// publish arrived in between.
	lateGen uint64
	pending bool
}

// lateFindingKey identifies a finding across versions of a file. Position is
// left out: an edit above a finding moves it without making it new.
type lateFindingKey struct {
	severity int
	code     string
	message  string
}

func lateFindingKeyOf(d Diagnostic) lateFindingKey {
	return lateFindingKey{severity: d.Severity, code: d.Code, message: d.Message}
}

// LateDiagnostics carries diagnostics a language server published for a
// written file after the write's tool result was returned, for the same
// document version Chord synced.
type LateDiagnostics struct {
	// Path is the normalized absolute path of the written file.
	Path string
	// DisplayPath is Path relative to the display base directory when inside it.
	DisplayPath string
	// Lines holds the rendered diagnostic lines, selected by the same output
	// policy as the write's tool result.
	Lines string
	// Reviews are the file's current per-server review counts.
	Reviews []message.LSPReview

	seq        uint64
	lateGen    uint64
	identities []diagnosticIdentity
	keys       []lateFindingKey
}

// watchLateDiagnostics starts following path after a successful diagnostics
// wait. The baseline is everything the path reports now, so only diagnostics
// that appear later are delivered.
func (m *Manager) watchLateDiagnostics(path string, serverVersions map[string]int32) {
	if m == nil || len(serverVersions) == 0 {
		return
	}
	path = normalizeWaiterPath(path)
	baseline := make(map[diagnosticIdentity]struct{})
	for _, d := range m.diagnosticsForPaths([]string{path})[path] {
		baseline[diagnosticIdentityKey(d)] = struct{}{}
	}
	versions := make(map[string]int32, len(serverVersions))
	maps.Copy(versions, serverVersions)
	expectsLate := len(m.immediateDiagnosticsServers(serverVersions)) > 0
	now := time.Now()
	m.lateMu.Lock()
	defer m.lateMu.Unlock()
	if m.lateWatches == nil {
		m.lateWatches = make(map[string]*lateWatch)
	}
	for watched, watch := range m.lateWatches {
		if now.Sub(watch.registered) > lateWatchRetention {
			delete(m.lateWatches, watched)
		}
	}
	m.lateSeq++
	m.lateWatches[path] = &lateWatch{
		seq:         m.lateSeq,
		versions:    versions,
		expectsLate: expectsLate,
		baseline:    baseline,
		registered:  now,
		deadline:    now.Add(lateDiagnosticsWindow),
	}
}

// dropLateDiagnostics forgets path's watch: a new synchronization of the file
// supersedes whatever the previous version would still have reported. When
// the previous version's diagnostics are known to be complete, findings that
// left the file are released so a later reintroduction is reported again.
func (m *Manager) dropLateDiagnostics(path string) {
	if m == nil {
		return
	}
	path = normalizeWaiterPath(path)
	m.lateMu.Lock()
	watch := m.lateWatches[path]
	delete(m.lateWatches, path)
	// gopls skips publishing an analysis pass identical to the type-check
	// pass, so a quiet window also leaves the cache complete.
	complete := watch != nil && (watch.lateGen > 0 || time.Now().After(watch.deadline))
	m.lateMu.Unlock()
	if complete {
		m.pruneLateFindings(path)
	}
}

// pruneLateFindings keeps only the delivered findings path still reports.
// Callers must make sure the cached diagnostics are a complete set.
func (m *Manager) pruneLateFindings(path string) {
	present := make(map[lateFindingKey]struct{})
	for _, d := range m.diagnosticsForPaths([]string{path})[path] {
		present[lateFindingKeyOf(d)] = struct{}{}
	}
	m.lateMu.Lock()
	defer m.lateMu.Unlock()
	reported := m.lateReported[path]
	for key := range reported {
		if _, ok := present[key]; !ok {
			delete(reported, key)
		}
	}
	if len(reported) == 0 {
		delete(m.lateReported, path)
	}
}

// noteLateDiagnosticsPublish marks a watched path pending when its server
// published again for the watched version inside the window. Such a publish
// is the server's final set for the version, so it also prunes the findings
// delivered for the path.
func (m *Manager) noteLateDiagnosticsPublish(path, serverID string, version int32, receivedAt time.Time) {
	if m == nil || version == 0 {
		return
	}
	m.lateMu.Lock()
	watch := m.lateWatches[path]
	matched := watch != nil && !receivedAt.After(watch.deadline)
	if matched {
		want, ok := watch.versions[serverID]
		matched = ok && want == version
	}
	if matched {
		watch.lateGen++
		watch.pending = true
		if m.lateChanged != nil {
			close(m.lateChanged)
			m.lateChanged = nil
		}
	}
	m.lateMu.Unlock()
	if matched {
		m.pruneLateFindings(path)
	}
}

// PendingLateDiagnostics reports late diagnostics for the given written paths
// without consuming them; AckLateDiagnostics consumes what was delivered.
// A path is reported only when a new error or warning appeared; information
// and hint findings ride along with one but never trigger a report. Findings
// already delivered for the file, at any position or version, are left out.
// Paths whose diagnostics went stale (the file changed on disk after Chord
// synced it) are skipped.
func (m *Manager) PendingLateDiagnostics(paths []string, displayBaseDir string) []LateDiagnostics {
	if m == nil || len(paths) == 0 {
		return nil
	}
	type candidate struct {
		path     string
		seq      uint64
		lateGen  uint64
		baseline map[diagnosticIdentity]struct{}
		reported map[lateFindingKey]struct{}
	}
	var candidates []candidate
	seen := make(map[string]struct{}, len(paths))
	m.lateMu.Lock()
	for _, path := range paths {
		path = normalizeWaiterPath(path)
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		watch := m.lateWatches[path]
		if watch == nil || !watch.pending {
			continue
		}
		baseline := make(map[diagnosticIdentity]struct{}, len(watch.baseline))
		for id := range watch.baseline {
			baseline[id] = struct{}{}
		}
		reported := make(map[lateFindingKey]struct{}, len(m.lateReported[path]))
		for key := range m.lateReported[path] {
			reported[key] = struct{}{}
		}
		candidates = append(candidates, candidate{path: path, seq: watch.seq, lateGen: watch.lateGen, baseline: baseline, reported: reported})
	}
	m.lateMu.Unlock()
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })

	var out []LateDiagnostics
	for _, c := range candidates {
		m.diagMu.Lock()
		fresh := m.diagnosticsFreshLocked(c.path)
		m.diagMu.Unlock()
		if !fresh {
			continue
		}
		var added []Diagnostic
		blocking := false
		for _, d := range deduplicateDiagnostics(m.diagnosticsForPaths([]string{c.path})[c.path]) {
			if _, known := c.baseline[diagnosticIdentityKey(d)]; known {
				continue
			}
			if _, delivered := c.reported[lateFindingKeyOf(d)]; delivered {
				continue
			}
			added = append(added, d)
			if d.Severity == 1 || d.Severity == 2 {
				blocking = true
			}
		}
		if !blocking {
			continue
		}
		selected, omitted := selectDiagnosticsByOutput(added, m.DiagnosticOutputConfigForPath(c.path), nil)
		if len(selected) == 0 {
			continue
		}
		lines := strings.Join(formatDiagnosticLines(selected), "\n")
		if omitted > 0 {
			lines += "\n" + diagnosticsOmittedLine(omitted)
		}
		item := LateDiagnostics{
			Path:        c.path,
			DisplayPath: displayDiagnosticPath(c.path, displayBaseDir),
			Lines:       lines,
			Reviews:     m.currentReviewSnapshots(c.path),
			seq:         c.seq,
			lateGen:     c.lateGen,
		}
		for _, d := range selected {
			item.identities = append(item.identities, diagnosticIdentityKey(d))
			item.keys = append(item.keys, lateFindingKeyOf(d))
		}
		out = append(out, item)
	}
	return out
}

// AckLateDiagnostics consumes delivered late diagnostics: their identities
// join the watch baseline and their findings the file's delivered set, so a
// later publish reports only what is newer. A watch replaced by a new
// synchronization since the peek keeps its own state, but the findings still
// count as delivered for the file.
func (m *Manager) AckLateDiagnostics(delivered []LateDiagnostics) {
	if m == nil || len(delivered) == 0 {
		return
	}
	m.lateMu.Lock()
	defer m.lateMu.Unlock()
	for _, item := range delivered {
		if m.lateReported == nil {
			m.lateReported = make(map[string]map[lateFindingKey]struct{})
		}
		reported := m.lateReported[item.Path]
		if reported == nil {
			reported = make(map[lateFindingKey]struct{}, len(item.keys))
			m.lateReported[item.Path] = reported
		}
		for _, key := range item.keys {
			reported[key] = struct{}{}
		}
		watch := m.lateWatches[item.Path]
		if watch == nil || watch.seq != item.seq {
			continue
		}
		for _, id := range item.identities {
			watch.baseline[id] = struct{}{}
		}
		if watch.lateGen == item.lateGen {
			watch.pending = false
		}
	}
}

// lateAwaitUntilLocked returns when the wait for paths' analysis passes ends:
// window after the latest write whose server publishes a later pass and has
// not yet done so, or zero when no such write remains. m.lateMu must be held.
func (m *Manager) lateAwaitUntilLocked(paths []string, window time.Duration) time.Time {
	var until time.Time
	for _, path := range paths {
		watch := m.lateWatches[path]
		if watch == nil || !watch.expectsLate || watch.lateGen > 0 {
			continue
		}
		if end := watch.registered.Add(window); end.After(until) {
			until = end
		}
	}
	return until
}

func normalizeLatePaths(paths []string) []string {
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		normalized = append(normalized, normalizeWaiterPath(path))
	}
	return normalized
}

// LateDiagnosticsAwaitable reports whether AwaitLateDiagnostics would block
// for paths with the same window.
func (m *Manager) LateDiagnosticsAwaitable(paths []string, window time.Duration) bool {
	if m == nil || len(paths) == 0 {
		return false
	}
	normalized := normalizeLatePaths(paths)
	m.lateMu.Lock()
	defer m.lateMu.Unlock()
	return time.Until(m.lateAwaitUntilLocked(normalized, window)) > 0
}

// AwaitLateDiagnostics blocks until every listed path whose server publishes
// a later analysis pass has received it, or until window after the latest of
// those paths' writes, whichever comes first. Paths without such a watch do
// not wait at all. It returns early when ctx is done.
func (m *Manager) AwaitLateDiagnostics(ctx context.Context, paths []string, window time.Duration) {
	if m == nil || len(paths) == 0 {
		return
	}
	normalized := normalizeLatePaths(paths)
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		m.lateMu.Lock()
		until := m.lateAwaitUntilLocked(normalized, window)
		remaining := time.Until(until)
		if until.IsZero() || remaining <= 0 {
			m.lateMu.Unlock()
			return
		}
		if m.lateChanged == nil {
			m.lateChanged = make(chan struct{})
		}
		changed := m.lateChanged
		m.lateMu.Unlock()

		if timer == nil {
			timer = time.NewTimer(remaining)
		} else {
			timer.Reset(remaining)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-changed:
			if !timer.Stop() {
				<-timer.C
			}
		}
	}
}

// resetLateDiagnostics forgets every watch and delivered finding; a new
// session starts without the previous one's follow-ups.
func (m *Manager) resetLateDiagnostics() {
	m.lateMu.Lock()
	m.lateWatches = nil
	m.lateReported = nil
	m.lateMu.Unlock()
}
