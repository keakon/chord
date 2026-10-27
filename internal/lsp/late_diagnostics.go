package lsp

import (
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

// lateWatchRetention drops watches nobody claimed. A watch is otherwise kept
// until the same path is synchronized again, which replaces it.
const lateWatchRetention = 10 * time.Minute

// latePublishRetention bounds the replay ring kept per path. Only publishes
// that land between a wait returning and its watch being registered can be
// replayed, so a handful is plenty.
const latePublishRetention = 8

// latePublish records one versioned publish so a watch created just after the
// wait consumed an earlier publish can still claim it.
type latePublish struct {
	seq        uint64
	serverID   string
	version    int32
	receivedAt time.Time
}

// lateWatch follows one post-write wait after it returned. Only Chord-synced
// versions are trusted: a publish counts when its server and version match the
// sync the wait verified, so an external change to the file can never be
// reported as fallout from this write.
type lateWatch struct {
	seq      uint64
	versions map[string]int32
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

	seq            uint64
	lateGen        uint64
	identities     []diagnosticIdentity
	keys           []lateFindingKey
	findingServers map[lateFindingKey]map[string]struct{}
}

func diagnosticIdentitySet(diags []Diagnostic) map[diagnosticIdentity]struct{} {
	set := make(map[diagnosticIdentity]struct{}, len(diags))
	for _, d := range diags {
		set[diagnosticIdentityKey(d)] = struct{}{}
	}
	return set
}

func flattenDiagnosticBaseline(byServer map[string]map[diagnosticIdentity]struct{}) map[diagnosticIdentity]struct{} {
	if len(byServer) == 0 {
		return nil
	}
	baseline := make(map[diagnosticIdentity]struct{})
	for _, set := range byServer {
		for id := range set {
			baseline[id] = struct{}{}
		}
	}
	return baseline
}

// diagnosticBaselineByServer snapshots the diagnostics each synced server
// already reports before a wait starts. Servers that publish during the wait
// replace their entry with the set the wait observed.
func (m *Manager) diagnosticBaselineByServer(path string, serverVersions map[string]int32) map[string]map[diagnosticIdentity]struct{} {
	byServer := m.diagnosticsForPathsByServer([]string{path})[path]
	baseline := make(map[string]map[diagnosticIdentity]struct{}, len(serverVersions))
	for server := range serverVersions {
		baseline[server] = diagnosticIdentitySet(byServer[server])
	}
	return baseline
}

// watchLateDiagnostics starts following path after a successful diagnostics
// wait. afterSeq is the publish sequence the wait consumed; publishes newer
// than it were already recorded and are replayed here, so a same-version
// publish that raced this registration is not lost. baseline is the set the
// wait itself observed; the current cache may already contain a later publish.
func (m *Manager) watchLateDiagnostics(path string, serverVersions map[string]int32, baseline map[diagnosticIdentity]struct{}, afterSeq uint64) {
	if m == nil || len(serverVersions) == 0 {
		return
	}
	path = normalizeWaiterPath(path)
	if baseline == nil {
		baseline = make(map[diagnosticIdentity]struct{})
	}
	versions := make(map[string]int32, len(serverVersions))
	maps.Copy(versions, serverVersions)
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
	for publishedPath, publishes := range m.latePublishes {
		if len(publishes) == 0 || now.Sub(publishes[len(publishes)-1].receivedAt) > lateWatchRetention {
			delete(m.latePublishes, publishedPath)
		}
	}
	m.lateSeq++
	watch := &lateWatch{
		seq:        m.lateSeq,
		versions:   versions,
		baseline:   baseline,
		registered: now,
		deadline:   now.Add(lateDiagnosticsWindow),
	}
	for _, publish := range m.latePublishes[path] {
		if publish.seq <= afterSeq || publish.receivedAt.After(watch.deadline) {
			continue
		}
		if want, ok := versions[publish.serverID]; !ok || want != publish.version {
			continue
		}
		watch.lateGen++
		watch.pending = true
	}
	m.lateWatches[path] = watch
	// The watch now owns these entries; a later watch is concerned only with
	// publishes after its own wait.
	delete(m.latePublishes, path)
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
	delete(m.latePublishes, path)
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

// pruneLateFindingsForServer releases delivered findings the publishing server
// no longer reports. A finding another server still reports stays suppressed:
// only the union of all current publishes makes it safe to report again.
func (m *Manager) pruneLateFindingsForServer(path, serverID string) {
	present := make(map[lateFindingKey]struct{})
	for _, d := range m.diagnosticsForPathsByServer([]string{path})[path][serverID] {
		present[lateFindingKeyOf(d)] = struct{}{}
	}
	m.lateMu.Lock()
	defer m.lateMu.Unlock()
	reported := m.lateReported[path]
	for key, suppliers := range reported {
		if _, supplied := suppliers[serverID]; !supplied {
			continue
		}
		if _, ok := present[key]; ok {
			continue
		}
		delete(suppliers, serverID)
		if len(suppliers) == 0 {
			delete(reported, key)
		}
	}
	if len(reported) == 0 {
		delete(m.lateReported, path)
	}
}

// noteLateDiagnosticsPublish records a publish, marks a watched path pending
// when its server published again for the watched version inside the window,
// and returns the publish sequence. Such a publish is the server's final set
// for the version, so it also prunes the findings delivered from that server.
func (m *Manager) noteLateDiagnosticsPublish(path, serverID string, version int32, receivedAt time.Time) uint64 {
	if m == nil {
		return 0
	}
	m.lateMu.Lock()
	m.latePublishSeq++
	seq := m.latePublishSeq
	if version != 0 {
		if m.latePublishes == nil {
			m.latePublishes = make(map[string][]latePublish)
		}
		publishes := append(m.latePublishes[path], latePublish{seq: seq, serverID: serverID, version: version, receivedAt: receivedAt})
		if len(publishes) > latePublishRetention {
			publishes = publishes[len(publishes)-latePublishRetention:]
		}
		m.latePublishes[path] = publishes
	}
	watch := m.lateWatches[path]
	matched := version != 0 && watch != nil && !receivedAt.After(watch.deadline)
	if matched {
		want, ok := watch.versions[serverID]
		matched = ok && want == version
	}
	if matched {
		watch.lateGen++
		watch.pending = true
	}
	m.lateMu.Unlock()
	if matched {
		m.pruneLateFindingsForServer(path, serverID)
	}
	return seq
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
		byServer := m.diagnosticsForPathsByServer([]string{c.path})[c.path]
		servers := make([]string, 0, len(byServer))
		for server := range byServer {
			servers = append(servers, server)
		}
		sort.Strings(servers)
		var added []Diagnostic
		seenAdded := make(map[diagnosticIdentity]struct{})
		suppliers := make(map[lateFindingKey]map[string]struct{})
		blocking := false
		for _, server := range servers {
			for _, d := range deduplicateDiagnostics(byServer[server]) {
				identity := diagnosticIdentityKey(d)
				if _, known := c.baseline[identity]; known {
					continue
				}
				key := lateFindingKeyOf(d)
				if _, delivered := c.reported[key]; delivered {
					continue
				}
				if _, seen := seenAdded[identity]; !seen {
					seenAdded[identity] = struct{}{}
					added = append(added, d)
				}
				if suppliers[key] == nil {
					suppliers[key] = make(map[string]struct{})
				}
				suppliers[key][server] = struct{}{}
				if d.Severity == 1 || d.Severity == 2 {
					blocking = true
				}
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
			Path:           c.path,
			DisplayPath:    displayDiagnosticPath(c.path, displayBaseDir),
			Lines:          lines,
			Reviews:        m.currentReviewSnapshots(c.path),
			seq:            c.seq,
			lateGen:        c.lateGen,
			findingServers: make(map[lateFindingKey]map[string]struct{}, len(selected)),
		}
		for _, d := range selected {
			item.identities = append(item.identities, diagnosticIdentityKey(d))
			key := lateFindingKeyOf(d)
			item.keys = append(item.keys, key)
			for server := range suppliers[key] {
				if item.findingServers[key] == nil {
					item.findingServers[key] = make(map[string]struct{})
				}
				item.findingServers[key][server] = struct{}{}
			}
		}
		out = append(out, item)
	}
	return out
}

// AckLateDiagnostics consumes delivered late diagnostics: their identities
// join the watch baseline and their findings the file's delivered set with the
// servers currently publishing them. A watch replaced by a new synchronization
// since the peek keeps its own state, but the findings still count as delivered
// for the file.
func (m *Manager) AckLateDiagnostics(delivered []LateDiagnostics) {
	if m == nil || len(delivered) == 0 {
		return
	}
	m.lateMu.Lock()
	defer m.lateMu.Unlock()
	for _, item := range delivered {
		if m.lateReported == nil {
			m.lateReported = make(map[string]map[lateFindingKey]map[string]struct{})
		}
		reported := m.lateReported[item.Path]
		if reported == nil {
			reported = make(map[lateFindingKey]map[string]struct{}, len(item.keys))
			m.lateReported[item.Path] = reported
		}
		for _, key := range item.keys {
			suppliers := reported[key]
			if suppliers == nil {
				suppliers = make(map[string]struct{}, len(item.findingServers[key]))
				reported[key] = suppliers
			}
			for server := range item.findingServers[key] {
				suppliers[server] = struct{}{}
			}
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

// resetLateDiagnostics forgets every watch and delivered finding; a new
// session starts without the previous one's follow-ups.
func (m *Manager) resetLateDiagnostics() {
	m.lateMu.Lock()
	m.lateWatches = nil
	m.latePublishes = nil
	m.lateReported = nil
	m.lateMu.Unlock()
}
