package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

// This file is the measurement harness for the read-only compaction history
// browser. It turns the acceptance matrix into numbers: the harness generates
// neutral snapshots, drives the real card conversion, installation, search and
// resize paths, and reports what the main loop actually costs.
//
// Nothing here is a functional test of the history layer: every observation is
// about size and time. The harness is opt-in because a full sweep installs
// tens of thousands of cards and must not slow the repository's normal test
// run.
//
// Run it with:
//
//	CHORD_SCALE_MEASURE=1 make test-fast PKGS='./internal/tui -run Scale -v -count=1'
//
// The report goes to the test log and the run writes nothing outside its temp
// dirs.

// scaleMeasureEnv enables the large-snapshot measurement.
const scaleMeasureEnv = "CHORD_SCALE_MEASURE"

// scaleFrameBudgetMs is one frame of a 60 Hz terminal. A main-loop batch that
// stays under it cannot make the reader perceive a stall.
const scaleFrameBudgetMs = 16.0

// scalePerceptibleLagMs is the point where a human reliably notices that a
// keystroke was late.
const scalePerceptibleLagMs = 50.0

// scaleTrendRounds is how many open/close rounds the memory trend records.
const scaleTrendRounds = 3

// scaleAbortTrials is how many cancel-during-load trials the harness runs.
const scaleAbortTrials = 5

// scaleDataset is one neutral snapshot to measure.
type scaleDataset struct {
	name     string
	messages []message.Message
}

// scaleMeasurement is everything the harness observed for one dataset.
type scaleMeasurement struct {
	dataset       string
	messages      int
	snapshotBytes int64
	cards         int
	residentBytes int64
	spilledBlocks int

	scanMs   float64
	decodeMs float64
	// convertMs is the synchronous main-loop step that turns the decoded
	// messages into cards. It is one unbounded batch: it is not chunked.
	convertMs float64

	installChunk    int
	installMs       []float64
	firstChunkMs    float64
	allReadyMs      float64
	progressText    string
	progressVisible bool
	progressCleared bool

	searchChunk   int
	searchMs      []float64
	searchTotalMs float64
	searchHits    int

	cancelHandlerMs  float64
	cancelToDropMs   float64
	switchIssueMs    float64
	switchToDropMs   float64
	switchToResultMs float64

	resizeFirstMs  float64
	resizeSteadyMs float64

	heapBaselineBytes  uint64
	heapPeakBytes      uint64
	heapSurvivingBytes uint64
	memSysBytes        uint64
	fdBefore           int
	fdDuring           int
	fdAfter            int
	spillDirsBefore    int
	spillDirsDuring    int
	spillDirsAfter     int
	trendRounds        int
	trendHeapBytes     []uint64

	abandonedDropped bool
	abandonedDropMs  float64
	abortTrials      int
	abortAborted     int
}

// scaleMeasurementEnabled reports whether the expensive sweep may run.
func scaleMeasurementEnabled() bool {
	return os.Getenv(scaleMeasureEnv) == "1"
}

// scaleMeasurementSkip explains why the sweep did not run.
func scaleMeasurementSkip(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skipf("scale measurement skipped in short mode; run with %s=1 for the full sweep", scaleMeasureEnv)
	}
	if !scaleMeasurementEnabled() {
		t.Skipf("scale measurement skipped; set %s=1 to run the large-snapshot sweep", scaleMeasureEnv)
	}
}

// scaleFillerBody returns neutral prose of a fixed shape. The text carries no
// project content: it only gives the renderer a realistic amount of lines to
// lay out.
func scaleFillerBody(seed int) string {
	lines := make([]string, 0, 6)
	for i := range 6 {
		lines = append(lines, fmt.Sprintf("probe line %d.%d: lorem ipsum dolor sit amet consectetur", seed, i))
	}
	return strings.Join(lines, "\n")
}

// scaleDatasetManyMessages builds a snapshot of ordinary user and assistant
// messages. The size is the number of messages, not the number of turns.
func scaleDatasetManyMessages(count int) scaleDataset {
	msgs := make([]message.Message, 0, count)
	for i := range count {
		if i%2 == 0 {
			msgs = append(msgs, message.Message{
				Role:    message.RoleUser,
				Content: fmt.Sprintf("probe request %d\n%s", i, scaleFillerBody(i)),
			})
			continue
		}
		msgs = append(msgs, message.Message{
			Role:    message.RoleAssistant,
			Content: fmt.Sprintf("probe reply %d\n%s", i, scaleFillerBody(i)),
		})
	}
	return scaleDataset{name: fmt.Sprintf("A-%d-messages", count), messages: msgs}
}

// scaleDatasetLongToolResult builds a snapshot whose tool result is a single
// huge record. The record is one physical JSONL line, which is exactly the
// long-line case the sealed snapshot reader has to buffer.
func scaleDatasetLongToolResult(payloadBytes int) scaleDataset {
	var body strings.Builder
	body.Grow(payloadBytes + 64)
	for body.Len() < payloadBytes {
		fmt.Fprintf(&body, "probe output line %d: lorem ipsum dolor sit amet consectetur adipiscing elit\n", body.Len())
	}
	content := body.String()
	msgs := []message.Message{
		{
			Role:    message.RoleUser,
			Content: "probe request for one oversized tool result",
		},
		{
			Role:    message.RoleAssistant,
			Content: "probe reply: running the oversized tool",
			ToolCalls: []message.ToolCall{{
				ID:   "probe-tool-call-1",
				Name: "read",
				Args: json.RawMessage(`{"path":"probe/oversized.txt","limit":100000}`),
			}},
		},
		{
			Role:       message.RoleTool,
			ToolCallID: "probe-tool-call-1",
			Content:    content,
			ToolStatus: message.ToolStatusSuccess,
		},
	}
	return scaleDataset{name: fmt.Sprintf("B-1x%dB-tool-result", payloadBytes), messages: msgs}
}

// scaleDatasetAttachmentMetadata builds a snapshot that is mostly attachment
// metadata: delivered-file records and image parts that carry a path and a
// size but no bytes. The harness never reads the referenced files and never
// decodes an image.
func scaleDatasetAttachmentMetadata(groups int) scaleDataset {
	msgs := make([]message.Message, 0, groups*3)
	for i := range groups {
		callID := fmt.Sprintf("probe-attach-call-%d", i)
		msgs = append(msgs, message.Message{
			Role:    message.RoleUser,
			Content: fmt.Sprintf("probe request %d with attachments", i),
			Parts: []message.ContentPart{
				{Type: message.ContentPartImage, FileName: fmt.Sprintf("probe-%d.png", i), ImagePath: filepath.Join("/nonexistent/probe", fmt.Sprintf("probe-%d.png", i)), MimeType: "image/png", DataBytes: 4096},
				{Type: message.ContentPartImage, FileName: fmt.Sprintf("probe-%d-b.png", i), ImagePath: filepath.Join("/nonexistent/probe", fmt.Sprintf("probe-%d-b.png", i)), MimeType: "image/png", DataBytes: 8192},
			},
		})
		msgs = append(msgs, message.Message{
			Role:    message.RoleAssistant,
			Content: fmt.Sprintf("probe reply %d delivering attachments", i),
			ToolCalls: []message.ToolCall{{
				ID:   callID,
				Name: "read",
				Args: json.RawMessage(`{"path":"probe/delivered.txt"}`),
			}},
		})
		files := make([]message.DeliveredFile, 0, 3)
		for f := range 3 {
			files = append(files, message.DeliveredFile{
				ID:          fmt.Sprintf("probe-file-%d-%d", i, f),
				Source:      message.DeliverySource{SessionID: "probe-session", TaskID: "probe-task", ToolCallID: callID, ItemIndex: f},
				Locator:     message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Root: "/nonexistent/probe", Path: fmt.Sprintf("delivered-%d-%d.txt", i, f)}},
				DisplayName: fmt.Sprintf("delivered-%d-%d.txt", i, f),
				MIME:        "text/plain",
				SizeBytes:   1024 * (int64(f) + 1),
			})
		}
		msgs = append(msgs, message.Message{
			Role:           message.RoleTool,
			ToolCallID:     callID,
			Content:        fmt.Sprintf("probe tool result %d", i),
			ToolStatus:     message.ToolStatusSuccess,
			DeliveredFiles: files,
			Parts: []message.ContentPart{
				{Type: message.ContentPartImage, FileName: fmt.Sprintf("probe-tool-%d.png", i), ImagePath: filepath.Join("/nonexistent/probe", fmt.Sprintf("probe-tool-%d.png", i)), MimeType: "image/png", DataBytes: 2048},
			},
		})
	}
	return scaleDataset{name: fmt.Sprintf("C-%d-groups-attachment-metadata", groups), messages: msgs}
}

// scaleSnapshotDir writes the dataset as a sealed snapshot pair
// (main.pre-compress-1.jsonl plus history-1.status.json) inside dir and
// returns the size of the snapshot file.
func scaleSnapshotDir(t *testing.T, dir string, msgs []message.Message) int64 {
	t.Helper()
	path := filepath.Join(dir, "main.pre-compress-1.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	encoder := json.NewEncoder(file)
	for i := range msgs {
		if err := encoder.Encode(&msgs[i]); err != nil {
			file.Close()
			t.Fatalf("encode snapshot record %d: %v", i, err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close snapshot: %v", err)
	}
	status := `{"status":"applied"}`
	if err := os.WriteFile(filepath.Join(dir, "history-1.status.json"), []byte(status), 0o600); err != nil {
		t.Fatalf("write status: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	return info.Size()
}

// scaleSnapshotSize returns the size of the snapshot file in dir.
func scaleSnapshotSize(t *testing.T, dir string) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, "main.pre-compress-1.jsonl"))
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	return info.Size()
}

// scaleModel builds a model whose agent reports dir as the session directory,
// so the history layer finds the snapshot on disk.
func scaleModel(t *testing.T, dir string) *Model {
	t.Helper()
	ApplyTheme(uitheme.Default())
	backend := &compactionHistorySessionAgent{sessionControlAgent: &sessionControlAgent{}, dir: dir}
	m := NewModelWithSize(backend, 120, 40)
	m.mode = ModeNormal
	m.layout = m.generateLayout(m.width, m.height)
	// The live viewport and the history layer own separate spill stores. Cleanup
	// must close both: closing only the live one leaves one chord-viewport-*
	// directory behind per dataset, which looks like a production leak in the
	// resource table and makes the "before" count drift across datasets.
	t.Cleanup(func() {
		_ = m.viewport.Close()
		if hv := m.compactionHistory.viewport; hv != nil {
			_ = hv.Close()
		}
	})
	return &m
}

// scaleCountFDs counts the open file descriptors of this process.
func scaleCountFDs() int {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// scaleCountSpillDirs counts the viewport spill directories that exist in the
// system temporary directory.
func scaleCountSpillDirs() int {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return -1
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "chord-viewport-") {
			count++
		}
	}
	return count
}

// scaleHeapAlloc returns the current heap allocation in bytes.
func scaleHeapAlloc() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

// scaleHeapSys returns the total memory obtained from the OS, an auxiliary
// metric only: it never shrinks back and cannot show a leak.
func scaleHeapSys() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.Sys
}

// scaleCollect runs the garbage collector until the heap is quiet, so the
// surviving-heap numbers are taken under a fixed condition.
func scaleCollect() {
	runtime.GC()
	runtime.GC()
}

// scalePercentile returns the p-th percentile of the samples in milliseconds.
func scalePercentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	idx := max(int(math.Ceil(p*float64(len(sorted))))-1, 0)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// scaleMax returns the largest sample in milliseconds.
func scaleMax(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	best := samples[0]
	for _, sample := range samples[1:] {
		if sample > best {
			best = sample
		}
	}
	return best
}

// scaleMillis converts a duration to milliseconds.
func scaleMillis(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// scaleDriveOpen walks the real open path: directory scan, sealed snapshot
// read, card conversion and the chunked installation of the cards. It returns
// the timing of every main-loop batch. onBatch, when not nil, is called after
// every installation batch so the caller can sample the heap.
func scaleDriveOpen(m *Model, onBatch func()) (scaleMeasurement, error) {
	var out scaleMeasurement

	openStart := time.Now()
	scanCmd := m.openCompactionHistory()
	if scanCmd == nil {
		return out, errors.New("openCompactionHistory returned no command")
	}

	scanStart := time.Now()
	scanMsg := scanCmd()
	out.scanMs = scaleMillis(time.Since(scanStart))

	loadCmd, ok := m.handleCompactionHistoryMessage(scanMsg)
	if !ok || loadCmd == nil {
		return out, errors.New("the scan did not start a snapshot load")
	}

	decodeStart := time.Now()
	loadMsg := loadCmd()
	out.decodeMs = scaleMillis(time.Since(decodeStart))

	loaded, ok := loadMsg.(compactionHistoryLoadedMsg)
	if !ok {
		return out, fmt.Errorf("unexpected load message %T", loadMsg)
	}
	if loaded.err != nil {
		return out, loaded.err
	}

	convertStart := time.Now()
	installCmd, ok := m.handleCompactionHistoryMessage(loaded)
	out.convertMs = scaleMillis(time.Since(convertStart))
	if !ok || installCmd == nil {
		return out, errors.New("the loaded snapshot did not schedule an installation")
	}

	out.installChunk = compactionHistoryInstallChunk
	for {
		batchStart := time.Now()
		installMsg := installCmd()
		next, handled := m.handleCompactionHistoryMessage(installMsg)
		out.installMs = append(out.installMs, scaleMillis(time.Since(batchStart)))
		if len(out.installMs) == 1 {
			out.firstChunkMs = scaleMillis(time.Since(openStart))
			// While cards are still being installed the layer shows the
			// progress text instead of the cards themselves.
			out.progressText = m.compactionHistory.progress
			out.progressVisible = out.progressText != ""
		}
		if onBatch != nil {
			onBatch()
		}
		if !handled {
			break
		}
		if next == nil {
			break
		}
		installCmd = next
	}
	out.allReadyMs = scaleMillis(time.Since(openStart))
	out.progressCleared = m.compactionHistory.progress == ""

	h := &m.compactionHistory
	if h.viewport != nil {
		out.cards = len(h.viewport.blocks)
		out.residentBytes = h.viewport.ResidentBytes()
		for _, block := range h.viewport.blocks {
			if block.spillCold {
				out.spilledBlocks++
			}
		}
	}
	return out, nil
}

// scaleDriveSearch runs a search to completion and returns the per-batch
// timings and the number of hits.
func scaleDriveSearch(m *Model, query string) (samples []float64, total time.Duration, hits int, err error) {
	h := &m.compactionHistory
	if !h.active {
		return nil, 0, 0, errors.New("the history layer is not open")
	}
	searchCmd := m.startCompactionHistorySearch(query)
	if searchCmd == nil {
		return nil, 0, 0, errors.New("the search did not schedule a step")
	}
	start := time.Now()
	for {
		batchStart := time.Now()
		stepMsg := searchCmd()
		next, handled := m.handleCompactionHistoryMessage(stepMsg)
		samples = append(samples, scaleMillis(time.Since(batchStart)))
		if !handled {
			break
		}
		if next == nil {
			break
		}
		searchCmd = next
	}
	total = time.Since(start)
	hits = len(m.compactionHistory.search.Matches)
	return samples, total, hits, nil
}

// scaleMeasureTiming measures the open, search, cancel, switch and resize
// paths of one dataset. The snapshot must already exist in dir.
func scaleMeasureTiming(t *testing.T, ds scaleDataset, dir string) scaleMeasurement {
	t.Helper()
	m := scaleModel(t, dir)
	// Release this dataset's history viewport before the next dataset starts,
	// otherwise the resource table counts leaked fixture directories as live
	// state and its "before" column drifts upward across the sweep.
	defer func() { _ = m.closeCompactionHistory() }()

	out, err := scaleDriveOpen(m, nil)
	if err != nil {
		t.Fatalf("open %s: %v", ds.name, err)
	}
	out.dataset = ds.name
	out.messages = len(ds.messages)
	out.snapshotBytes = scaleSnapshotSize(t, dir)

	// The search scans the installed cards, so it can only run once the
	// installation finished.
	out.searchChunk = compactionHistorySearchChunk
	samples, total, hits, err := scaleDriveSearch(m, "probe")
	if err != nil {
		t.Fatalf("search %s: %v", ds.name, err)
	}
	out.searchMs = samples
	out.searchTotalMs = scaleMillis(total)
	out.searchHits = hits

	// Cancelling a search: the cost of the Esc keypress itself, and the time
	// until the in-flight search step is discarded.
	pending := m.startCompactionHistorySearch("probe")
	cancelStart := time.Now()
	m.cancelCompactionHistorySearch()
	out.cancelHandlerMs = scaleMillis(time.Since(cancelStart))
	if pending != nil {
		stale := pending()
		m.handleCompactionHistoryMessage(stale)
	}
	out.cancelToDropMs = scaleMillis(time.Since(cancelStart))

	// Replacing a query: the old in-flight step must be dropped and the new
	// query must produce its own result.
	oldCmd := m.startCompactionHistorySearch("probe request")
	switchStart := time.Now()
	newCmd := m.startCompactionHistorySearch("probe reply")
	out.switchIssueMs = scaleMillis(time.Since(switchStart))
	if oldCmd != nil {
		stale := oldCmd()
		m.handleCompactionHistoryMessage(stale)
	}
	out.switchToDropMs = scaleMillis(time.Since(switchStart))
	if newCmd != nil {
		for {
			stepMsg := newCmd()
			next, handled := m.handleCompactionHistoryMessage(stepMsg)
			if !handled || next == nil {
				break
			}
			newCmd = next
		}
	}
	out.switchToResultMs = scaleMillis(time.Since(switchStart))

	// Resizing: a real WindowSizeMsg updates m.width and the resize branch of
	// Update starts a reflow. Time the first visible frame after that event,
	// then the steady frame, and finally drive the bounded reflow batches to
	// completion so the next dataset starts from a consistent model.
	m.renderCompactionHistory(m.compactionHistoryArea())
	resizeStart := time.Now()
	m.width -= 20
	reflowCmd := m.startCompactionHistoryReflow()
	m.renderCompactionHistory(m.compactionHistoryArea())
	out.resizeFirstMs = scaleMillis(time.Since(resizeStart))
	resizeSteadyStart := time.Now()
	m.renderCompactionHistory(m.compactionHistoryArea())
	out.resizeSteadyMs = scaleMillis(time.Since(resizeSteadyStart))
	for i := 0; reflowCmd != nil && i < 100000; i++ {
		msg := reflowCmd()
		next, handled := m.handleCompactionHistoryMessage(msg)
		if !handled {
			break
		}
		reflowCmd = next
	}
	if m.compactionHistory.reflowActive {
		t.Fatalf("resize reflow did not finish for %s", ds.name)
	}

	return out
}

// scaleMeasureMemory measures the heap, the file descriptors and the spill
// directories of one dataset, including the trend across repeated
// open/close rounds. The snapshot must already exist in dir.
func scaleMeasureMemory(t *testing.T, ds scaleDataset, dir string) scaleMeasurement {
	t.Helper()
	m := scaleModel(t, dir)

	out := scaleMeasurement{dataset: ds.name, messages: len(ds.messages)}
	scaleCollect()
	out.heapBaselineBytes = scaleHeapAlloc()
	out.fdBefore = scaleCountFDs()
	out.spillDirsBefore = scaleCountSpillDirs()

	peak := out.heapBaselineBytes
	sample := func() {
		if current := scaleHeapAlloc(); current > peak {
			peak = current
		}
	}
	open, err := scaleDriveOpen(m, sample)
	if err != nil {
		t.Fatalf("open %s for memory: %v", ds.name, err)
	}
	out.cards = open.cards
	out.residentBytes = open.residentBytes
	out.spilledBlocks = open.spilledBlocks
	out.heapPeakBytes = peak

	out.fdDuring = scaleCountFDs()
	out.spillDirsDuring = scaleCountSpillDirs()

	m.closeCompactionHistory()
	scaleCollect()
	out.heapSurvivingBytes = scaleHeapAlloc()
	out.memSysBytes = scaleHeapSys()
	out.fdAfter = scaleCountFDs()
	out.spillDirsAfter = scaleCountSpillDirs()

	// The trend: repeated open/close rounds under the same measurement
	// condition. A growing line means the layer keeps its cards alive.
	out.trendRounds = scaleTrendRounds
	for round := range scaleTrendRounds {
		if _, err := scaleDriveOpen(m, nil); err != nil {
			t.Fatalf("open round %d of %s: %v", round, ds.name, err)
		}
		m.closeCompactionHistory()
		scaleCollect()
		out.trendHeapBytes = append(out.trendHeapBytes, scaleHeapAlloc())
	}
	return out
}

// scaleMeasureAbandoned checks that a load that finishes after the layer was
// closed installs nothing, and that a cancelled load ends with a context
// error instead of hanging.
func scaleMeasureAbandoned(t *testing.T, ds scaleDataset, dir string) scaleMeasurement {
	t.Helper()
	out := scaleMeasurement{dataset: ds.name, messages: len(ds.messages)}

	m := scaleModel(t, dir)
	scanCmd := m.openCompactionHistory()
	if scanCmd == nil {
		t.Fatal("openCompactionHistory returned no command")
	}
	loadCmd, ok := m.handleCompactionHistoryMessage(scanCmd())
	if !ok || loadCmd == nil {
		t.Fatal("the scan did not start a snapshot load")
	}
	generation := m.compactionHistory.generation
	m.closeCompactionHistory()

	// The late result arrives after the layer was closed.
	dropStart := time.Now()
	late := compactionHistoryLoadedMsg{generation: generation, messages: ds.messages}
	cmd, handled := m.handleCompactionHistoryMessage(late)
	out.abandonedDropMs = scaleMillis(time.Since(dropStart))
	out.abandonedDropped = handled && cmd == nil && !m.compactionHistory.active && len(m.compactionHistory.pendingBlocks) == 0

	// A cancelled load must end instead of running to completion.
	for trial := range scaleAbortTrials {
		abortModel := scaleModel(t, dir)
		abortScan := abortModel.openCompactionHistory()
		if abortScan == nil {
			t.Fatal("openCompactionHistory returned no command")
		}
		abortLoad, ok := abortModel.handleCompactionHistoryMessage(abortScan())
		if !ok || abortLoad == nil {
			t.Fatal("the scan did not start a snapshot load")
		}
		cancel := abortModel.compactionHistory.cancel
		if cancel == nil {
			t.Fatal("the history layer has no cancellation function")
		}
		type result struct {
			msg tea.Msg
		}
		results := make(chan result, 1)
		go func() {
			results <- result{msg: abortLoad()}
		}()
		time.Sleep(200 * time.Microsecond)
		cancel()
		var got result
		select {
		case got = <-results:
		case <-time.After(5 * time.Second):
			t.Fatalf("cancelled load did not return for %s (trial %d)", ds.name, trial+1)
		}
		out.abortTrials++
		loaded, ok := got.msg.(compactionHistoryLoadedMsg)
		if ok && errors.Is(loaded.err, context.Canceled) {
			out.abortAborted++
		}
		abortModel.closeCompactionHistory()
	}
	return out
}

// scaleMerge copies the resource fields of a memory measurement into a
// timing measurement. The timing fields are kept: the memory pass measures
// the same dataset with the same generator, so the two runs describe one
// dataset rather than two.
func scaleMerge(dst scaleMeasurement, src scaleMeasurement) scaleMeasurement {
	dst.heapBaselineBytes = src.heapBaselineBytes
	dst.heapPeakBytes = src.heapPeakBytes
	dst.heapSurvivingBytes = src.heapSurvivingBytes
	dst.memSysBytes = src.memSysBytes
	dst.fdBefore = src.fdBefore
	dst.fdDuring = src.fdDuring
	dst.fdAfter = src.fdAfter
	dst.spillDirsBefore = src.spillDirsBefore
	dst.spillDirsDuring = src.spillDirsDuring
	dst.spillDirsAfter = src.spillDirsAfter
	dst.trendRounds = src.trendRounds
	dst.trendHeapBytes = src.trendHeapBytes
	return dst
}

// scaleThreshold is one acceptance limit with its observed worst case. The
// limit is an external reference line, not a multiple of the candidate's own
// measurement: deriving a pass line from the thing under test would make the
// limit drift with every run and could never fail.
type scaleThreshold struct {
	name       string
	valueMs    float64 // fixed acceptance line
	observedMs float64 // worst observation across the datasets
	reason     string
}

func (s scaleThreshold) pass() bool { return s.observedMs <= s.valueMs }

// scaleDeriveThresholds pairs the fixed reference lines with the worst observed
// value of each path. "First screen ready" and "query switch to result" are
// deliberately absent: they are cumulative totals across many main-loop
// batches, during which the loop still serves input, so a single-frame line
// would be the wrong yardstick. They stay observation-only in the timing table.
func scaleDeriveThresholds(measurements []scaleMeasurement) []scaleThreshold {
	var installMax, searchMax, cancelMax, resizeMax, convertMax float64
	for _, m := range measurements {
		installMax = math.Max(installMax, scaleMax(m.installMs))
		searchMax = math.Max(searchMax, scaleMax(m.searchMs))
		cancelMax = math.Max(cancelMax, math.Max(m.cancelHandlerMs, m.cancelToDropMs))
		cancelMax = math.Max(cancelMax, math.Max(m.switchIssueMs, m.switchToDropMs))
		resizeMax = math.Max(resizeMax, m.resizeFirstMs)
		convertMax = math.Max(convertMax, m.convertMs)
	}
	frame := scaleFrameBudgetMs
	lag := scalePerceptibleLagMs
	return []scaleThreshold{
		{
			name: "installation batch", valueMs: frame, observedMs: installMax,
			reason: "one main-loop batch must return within a 60 Hz frame",
		},
		{
			name: "search batch", valueMs: frame, observedMs: searchMax,
			reason: "a search batch must return within a 60 Hz frame",
		},
		{
			name: "search cancel / query switch", valueMs: frame, observedMs: cancelMax,
			reason: "input must take effect within one frame; stale generations are discarded",
		},
		{
			name: "first resized frame", valueMs: lag, observedMs: resizeMax,
			reason: "the resized frame must appear within the perceptible-lag limit",
		},
		{
			name: "synchronous card conversion", valueMs: frame, observedMs: convertMax,
			reason: "unchunked card conversion must fit in one frame",
		},
	}
}

// scaleLogTable prints the measured numbers to the test log.
func scaleLogTable(t *testing.T, measurements []scaleMeasurement) {
	t.Helper()
	t.Logf("environment: %s %s/%s CPUs=%d GOMAXPROCS=%d time=%s",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), time.Now().Format(time.RFC3339))
	t.Logf("%-34s %8s %10s %8s %10s %10s %9s %9s %10s",
		"dataset", "msgs", "snapshotB", "cards", "convertMs", "visibleMs", "instMax", "srchMax", "residB")
	for _, m := range measurements {
		t.Logf("%-34s %8d %10d %8d %10.2f %10.2f %9.2f %9.2f %10d",
			m.dataset, m.messages, m.snapshotBytes, m.cards, m.convertMs, m.allReadyMs,
			scaleMax(m.installMs), scaleMax(m.searchMs), m.residentBytes)
		t.Logf("  load(ms): scan=%.2f decode=%.2f first=%.2f installP95=%.2f installChunk=%d progress=%q visible=%t cleared=%t",
			m.scanMs, m.decodeMs, m.firstChunkMs, scalePercentile(m.installMs, 0.95),
			m.installChunk, m.progressText, m.progressVisible, m.progressCleared)
		t.Logf("  search(ms): total=%.2f chunk=%d hits=%d cancel=%.2f cancelDrop=%.2f switch=%.2f switchDrop=%.2f switchResult=%.2f",
			m.searchTotalMs, m.searchChunk, m.searchHits, m.cancelHandlerMs, m.cancelToDropMs,
			m.switchIssueMs, m.switchToDropMs, m.switchToResultMs)
		t.Logf("  resize(ms): first=%.2f steady=%.2f", m.resizeFirstMs, m.resizeSteadyMs)
		t.Logf("  memory(bytes): baseline=%d peak=%d surviving=%d sys=%d trendRounds=%d trend=%v",
			m.heapBaselineBytes, m.heapPeakBytes, m.heapSurvivingBytes, m.memSysBytes, m.trendRounds, m.trendHeapBytes)
		t.Logf("  resources(before/during/after): FD=%d/%d/%d spillDirs=%d/%d/%d spilledBlocks=%d",
			m.fdBefore, m.fdDuring, m.fdAfter, m.spillDirsBefore, m.spillDirsDuring, m.spillDirsAfter, m.spilledBlocks)
		t.Logf("  abandoned: dropped=%t dropMs=%.2f aborted=%d/%d",
			m.abandonedDropped, m.abandonedDropMs, m.abortAborted, m.abortTrials)
		if len(m.searchMs) > 0 {
			worst, worstAt := 0.0, 0
			for i, v := range m.searchMs {
				if v > worst {
					worst, worstAt = v, i
				}
			}
			t.Logf("%-34s slowest search batch %d/%d = %.2f ms (P95 %.2f ms)",
				"", worstAt+1, len(m.searchMs), worst, scalePercentile(m.searchMs, 0.95))
		}
	}
	t.Logf("%-34s %12s %12s %8s", "independent thresholds", "budget(ms)", "observed(ms)", "verdict")
	for _, threshold := range scaleDeriveThresholds(measurements) {
		verdict := "PASS"
		if !threshold.pass() {
			verdict = "FAIL"
		}
		t.Logf("%-34s %12.0f %12.2f %8s (%s)", threshold.name, threshold.valueMs, threshold.observedMs, verdict, threshold.reason)
	}
}

// TestCompactionHistoryScaleMeasurement measures the read-only compaction
// history layer on neutral large snapshots. It is opt-in because it installs
// tens of thousands of cards. The report only goes to the test log.
//
//	CHORD_SCALE_MEASURE=1 make test-fast PKGS='./internal/tui -run Scale -v -count=1'
func TestCompactionHistoryScaleMeasurement(t *testing.T) {
	scaleMeasurementSkip(t)
	stubTUITicks(t)
	t.Setenv("CHORD_IMAGE_BACKEND", "none")

	datasets := []scaleDataset{
		scaleDatasetManyMessages(500),
		scaleDatasetManyMessages(2000),
		scaleDatasetManyMessages(8000),
		scaleDatasetLongToolResult(1 << 20),
		scaleDatasetAttachmentMetadata(1000),
	}

	measurements := make([]scaleMeasurement, 0, len(datasets))
	for _, ds := range datasets {
		dir := t.TempDir()
		size := scaleSnapshotDir(t, dir, ds.messages)

		timing := scaleMeasureTiming(t, ds, dir)
		timing.snapshotBytes = size
		memory := scaleMeasureMemory(t, ds, dir)
		abandoned := scaleMeasureAbandoned(t, ds, dir)

		merged := scaleMerge(timing, memory)
		merged.abandonedDropped = abandoned.abandonedDropped
		merged.abandonedDropMs = abandoned.abandonedDropMs
		merged.abortTrials = abandoned.abortTrials
		merged.abortAborted = abandoned.abortAborted
		measurements = append(measurements, merged)
	}
	scaleLogTable(t, measurements)
}

// TestCompactionHistoryScaleSnapshotShape checks that the neutral generator
// really produces the sealed snapshot pair the history layer reads, so the
// measurement cannot silently measure an empty snapshot.
func TestCompactionHistoryScaleSnapshotShape(t *testing.T) {
	dir := t.TempDir()
	ds := scaleDatasetManyMessages(4)
	size := scaleSnapshotDir(t, dir, ds.messages)
	if size <= 0 {
		t.Fatal("the snapshot file is empty")
	}

	snapshots, issues, err := recovery.ScanCompactionHistory(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("scan reported issues: %v", issues)
	}
	if len(snapshots) != 1 || snapshots[0].Index != 1 {
		t.Fatalf("scanned snapshots = %v, want one snapshot with index 1", snapshots)
	}

	msgs, err := recovery.LoadAppliedCompactionSnapshot(context.Background(), "", dir, 1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(msgs) != len(ds.messages) {
		t.Fatalf("loaded %d messages, want %d", len(msgs), len(ds.messages))
	}
}
