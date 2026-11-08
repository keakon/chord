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
// returned on; a few seconds on large packages. 15s covers typical cases
// without excessive retention overhead.
const lateDiagnosticsWindow = 15 * time.Second

// lateWatchRetention drops watches nobody claimed. A watch is otherwise kept
// until the same path is synchronized again, which replaces it.
const lateWatchRetention = 10 * time.Minute

// latePublishRetention bounds the replay ring kept per path. Only publishes
// that land between a wait returning and its watch being registered can be
// replayed, so a handful is plenty.
const latePublishRetention = 8

// latePublish records a publish so a watch created just after the
// wait consumed an earlier publish can still claim it.
type latePublish struct {
	seq         uint64
	serverID    string
	client      clientKey
	version     int32
	generation  uint64
	receivedAt  time.Time
	diagnostics []Diagnostic
}

// lateWatch follows one post-write wait after it returned. Only Chord-synced
// versions are trusted. Servers that never echo versions use the sync's
// receipt boundary and client generation; claim-time disk checks still apply.
type lateWatch struct {
	seq                uint64
	versions           map[string]int32
	unversionedServers map[string]bool
	// writerAgentID is the agent whose write produced this watch. Follow-ups
	// are only claimed by that agent: a worker's write may appear in the
	// main transcript, but its late diagnostics belong to the worker.
	writerAgentID string
	// generations captures each client's sync before its didChange was sent,
	// not when the wait or watch finishes. A racing sync invalidates its share.
	generations map[clientKey]uint64
	// token is the sync token whose wait registered this watch. A confirmable
	// token lets the claim re-run the freshness confirmation for a wait that
	// timed out: the path stays stale until a pending late publish is about
	// to be delivered. Tokens from partially failed syncs never arrive here.
	token diagnosticSyncToken
	// baseline holds every diagnostic the path had when the wait returned
	// plus every diagnostic already delivered as late; only identities outside
	// it are reported.
	baseline map[diagnosticIdentity]struct{}
	// frozen holds the latest matching publish per server for the synced
	// version. Late follow-ups read this instead of the live cache: a second
	// server or an external edit can replace the cache after the publish that
	// actually belongs to this write.
	frozen     map[string][]Diagnostic
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
	// Reviews are counts from the matching frozen per-server publishes.
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

// watchLateDiagnostics starts following path after its after-write wait
// returned — whether that wait saw a publish or timed out. afterSeq is the
// publish sequence the wait consumed; publishes newer than it were already
// recorded and are replayed here, so a same-version publish that raced this
// registration is not lost. baseline is the set the wait itself observed; the
// current cache may already contain a later publish. token must be
// confirmable: a sync whose notifications partially failed cannot have its
// path claimed later, because no publish can prove which content the missing
// server analyzed. It reports whether a watch was registered.
func (m *Manager) watchLateDiagnostics(path string, serverVersions map[string]int32, generations map[clientKey]uint64, baseline map[diagnosticIdentity]struct{}, afterSeq uint64, writerAgentID string, token diagnosticSyncToken) bool {
	if m == nil || len(serverVersions) == 0 || !token.confirmable {
		return false
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
		seq:                m.lateSeq,
		versions:           versions,
		unversionedServers: m.unversionedDiagnosticsServers(versions),
		writerAgentID:      writerAgentID,
		generations:        maps.Clone(generations),
		token:              token,
		baseline:           baseline,
		frozen:             make(map[string][]Diagnostic, len(versions)),
		registered:         now,
		deadline:           now.Add(lateDiagnosticsWindow),
	}
	m.pruneLateWatchClientsLocked(watch)
	for server := range watch.versions {
		owned := false
		for key := range watch.generations {
			if key.name == server {
				owned = true
				break
			}
		}
		if !owned {
			delete(watch.versions, server)
		}
	}
	if len(watch.versions) == 0 {
		delete(m.lateWatches, path)
		delete(m.latePublishes, path)
		return false
	}
	for _, publish := range m.latePublishes[path] {
		if publish.seq <= afterSeq || publish.receivedAt.After(watch.deadline) {
			continue
		}
		if _, owner := watch.generations[publish.client]; !owner {
			continue
		}
		if !watch.acceptsPublish(publish) {
			continue
		}
		watch.frozen[publish.serverID] = publish.diagnostics
		watch.lateGen++
		watch.pending = true
	}
	m.lateWatches[path] = watch
	// The watch now owns these entries; a later watch is concerned only with
	// publishes after its own wait.
	delete(m.latePublishes, path)
	return true
}

func (watch *lateWatch) acceptsPublish(publish latePublish) bool {
	want, owner := watch.versions[publish.serverID]
	if !owner {
		return false
	}
	if publish.version != 0 {
		return publish.version == want
	}
	return watch.unversionedServers[publish.serverID] &&
		!watch.token.after.IsZero() && !publish.receivedAt.Before(watch.token.after) &&
		publish.generation == watch.generations[publish.client]
}

// batchWatchGenerations replaces each listed client's generation with its
// last sequence produced by the batch, keeping only clients in the map. A
// batch of writes registers its watches only after its last sync, so the
// watches must carry the sequence the client has by then — every earlier
// file's didChange advanced the shared client's sequence.
func batchWatchGenerations(generations, final map[clientKey]uint64) map[clientKey]uint64 {
	if len(generations) == 0 {
		return nil
	}
	out := make(map[clientKey]uint64, len(generations))
	for key := range generations {
		out[key] = final[key]
	}
	return out
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

// noteLateDocumentSync runs before a notification is attempted: a failed send
// may still have reached the server, so its older project state is untrusted.
func (m *Manager) noteLateDocumentSync(key clientKey) uint64 {
	if m == nil {
		return 0
	}
	m.lateMu.Lock()
	if m.lateSyncSeq == nil {
		m.lateSyncSeq = make(map[clientKey]uint64)
	}
	m.lateSyncSeq[key]++
	seq := m.lateSyncSeq[key]
	m.lateMu.Unlock()
	return seq
}

// pruneLateWatchClientsLocked preserves independent clients' findings when a
// workspace sync supersedes only one server's analysis. lateMu must be held.
func (m *Manager) pruneLateWatchClientsLocked(watch *lateWatch) {
	for key, generation := range watch.generations {
		if generation != m.lateSyncSeq[key] {
			delete(watch.generations, key)
			delete(watch.versions, key.name)
			delete(watch.frozen, key.name)
		}
	}
}

// noteLateDiagnosticsPublish records a publish and freezes it for a watched
// path when its server published again for the watched version inside the
// window. The publish contents are not pruned here: gopls publishes the
// type-check pass before its analysis pass for the same version, and the
// first publish must not release findings the second one still reports.
func (m *Manager) noteLateDiagnosticsPublish(path string, key clientKey, version int32, receivedAt time.Time, diagnostics []Diagnostic) uint64 {
	if m == nil {
		return 0
	}
	m.lateMu.Lock()
	watch := m.lateWatches[path]
	if watch != nil {
		m.pruneLateWatchClientsLocked(watch)
	}
	m.latePublishSeq++
	seq := m.latePublishSeq
	publish := latePublish{
		seq:         seq,
		serverID:    key.name,
		client:      key,
		version:     version,
		generation:  m.lateSyncSeq[key],
		receivedAt:  receivedAt,
		diagnostics: diagnostics,
	}
	unversioned := m.cfg != nil && publishesUnversioned(key.name, m.cfg.LSP[key.name])
	if version != 0 || unversioned && publish.generation > 0 {
		if m.latePublishes == nil {
			m.latePublishes = make(map[string][]latePublish)
		}
		publishes := append(m.latePublishes[path], publish)
		if len(publishes) > latePublishRetention {
			publishes = publishes[len(publishes)-latePublishRetention:]
		}
		m.latePublishes[path] = publishes
	}
	if watch == nil {
		m.lateMu.Unlock()
		return seq
	}
	if _, sameClient := watch.generations[key]; !sameClient {
		m.lateMu.Unlock()
		return seq
	}
	dropClient := false
	if _, owner := watch.versions[key.name]; owner {
		switch {
		case !watch.acceptsPublish(publish):
			dropClient = true
		case receivedAt.After(watch.deadline):
			// A late analysis pass stays unclaimed; the watch expires on
			// its own without treating the publish as fallout.
		default:
			watch.frozen[key.name] = diagnostics
			watch.lateGen++
			watch.pending = true
		}
	}
	if dropClient {
		delete(watch.generations, key)
		delete(watch.versions, key.name)
		delete(watch.frozen, key.name)
		watch.pending = len(watch.frozen) > 0
		if len(watch.generations) == 0 {
			delete(m.lateWatches, path)
			delete(m.latePublishes, path)
		}
	}
	if len(watch.versions) == 0 {
		delete(m.lateWatches, path)
		delete(m.latePublishes, path)
	}
	m.lateMu.Unlock()
	return seq
}

// clearLatePending clears pending only when the watch is still the one the
// caller inspected; a publish can arrive between the peek and the clear.
func (m *Manager) clearLatePending(path string, seq, lateGen uint64) {
	if m == nil {
		return
	}
	m.lateMu.Lock()
	watch := m.lateWatches[path]
	if watch != nil && watch.seq == seq && watch.lateGen == lateGen {
		watch.pending = false
	}
	m.lateMu.Unlock()
}

// PendingLateDiagnostics reports late diagnostics for the given written paths
// without consuming them; AckLateDiagnostics consumes what was delivered.
// Diagnostic text is reported only when a new error or warning appeared;
// otherwise a review-only item preserves the matching publish's counts.
// Information and hint findings ride along but never trigger text. Findings
// already delivered for the file, at any position or version, are left out.
// Paths whose diagnostics went stale (the file changed on disk after Chord
// synced it) are skipped.
func (m *Manager) PendingLateDiagnostics(paths []string, displayBaseDir, agentID string) []LateDiagnostics {
	if m == nil || len(paths) == 0 {
		return nil
	}
	type candidate struct {
		path     string
		seq      uint64
		lateGen  uint64
		baseline map[diagnosticIdentity]struct{}
		reported map[lateFindingKey]struct{}
		frozen   map[string][]Diagnostic
		token    diagnosticSyncToken
	}
	var candidates []candidate
	seen := make(map[string]struct{}, len(paths))
	now := time.Now()
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
		m.pruneLateWatchClientsLocked(watch)
		if len(watch.versions) == 0 {
			delete(m.lateWatches, path)
			delete(m.latePublishes, path)
			continue
		}
		if now.Sub(watch.registered) > lateWatchRetention {
			delete(m.lateWatches, path)
			delete(m.latePublishes, path)
			continue
		}
		if watch.writerAgentID != "" && agentID != "" && watch.writerAgentID != agentID {
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
		frozen := maps.Clone(watch.frozen)
		candidates = append(candidates, candidate{path: path, seq: watch.seq, lateGen: watch.lateGen, baseline: baseline, reported: reported, frozen: frozen, token: watch.token})
	}
	m.lateMu.Unlock()
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })

	var out []LateDiagnostics
	for _, c := range candidates {
		// A claim re-runs the sync confirmation before the freshness check.
		// This is what lets a timed-out wait deliver later: its path was left
		// stale by the sync, and only a publish that is about to be claimed —
		// with the file's mtime unchanged and no newer sync — proves the
		// cached content still describes the write. The confirm is idempotent
		// and re-validates generation and mtime, so a new write's sync or an
		// external edit keeps the path stale.
		if c.token.path != "" && c.token.confirmable {
			m.confirmDiagnosticsSync(c.token)
		}
		m.diagMu.Lock()
		fresh := m.diagnosticsFreshLocked(c.path)
		reportedIdentities := maps.Clone(m.reportedByPath[c.path])
		m.diagMu.Unlock()
		if !fresh {
			m.clearLatePending(c.path, c.seq, c.lateGen)
			continue
		}
		servers := make([]string, 0, len(c.frozen))
		for server := range c.frozen {
			servers = append(servers, server)
		}
		sort.Strings(servers)
		var added []Diagnostic
		seenAdded := make(map[diagnosticIdentity]struct{})
		suppliers := make(map[lateFindingKey]map[string]struct{})
		blocking := false
		for _, server := range servers {
			for _, d := range deduplicateDiagnostics(c.frozen[server]) {
				identity := diagnosticIdentityKey(d)
				if _, known := c.baseline[identity]; known {
					continue
				}
				if _, delivered := reportedIdentities[identity]; delivered {
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
			out = append(out, LateDiagnostics{Path: c.path, DisplayPath: displayDiagnosticPath(c.path, displayBaseDir), Reviews: frozenReviewSnapshots(c.path, c.frozen), seq: c.seq, lateGen: c.lateGen})
			continue
		}
		selected, omitted := selectDiagnosticsByOutput(added, m.DiagnosticOutputConfigForPath(c.path), nil)
		if len(selected) == 0 {
			m.clearLatePending(c.path, c.seq, c.lateGen)
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
			Reviews:        frozenReviewSnapshots(c.path, c.frozen),
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
	m.diagMu.Lock()
	for _, item := range delivered {
		if m.reportedByPath == nil {
			m.reportedByPath = make(map[string]map[diagnosticIdentity]struct{})
		}
		if m.reportedByPath[item.Path] == nil {
			m.reportedByPath[item.Path] = make(map[diagnosticIdentity]struct{})
		}
		for _, id := range item.identities {
			m.reportedByPath[item.Path][id] = struct{}{}
		}
	}
	m.diagMu.Unlock()
	m.lateMu.Lock()
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
	m.lateMu.Unlock()
	// The delivered findings are the first review evidence for a write whose
	// diagnostics wait timed out; record them for the sidebar now. The wait
	// itself skipped its snapshot so pre-write counts never posed as a
	// completed review.
	for _, item := range delivered {
		m.recordDeliveredReviews(item.Reviews)
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
