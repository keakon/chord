package tui

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// --- helpers ---------------------------------------------------------------

func resetJobsRegistry(t *testing.T) {
	t.Helper()
	reset := tools.ResetJobRegistryForTest()
	t.Cleanup(reset)
}

func startTestJob(t *testing.T, command, description string) string {
	t.Helper()
	id, err := tools.ExecuteJobForTest(context.Background(), command, description, nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest(%q) error: %v", command, err)
	}
	t.Cleanup(func() { tools.StopJobByUser(id, "test cleanup") })
	return id
}

func waitForJobTail(t *testing.T, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if peek, ok := tools.PeekJobForDisplay(id, stopJobPeekTailBytes); ok && strings.Contains(peek.Tail, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never produced output containing %q", id, want)
}

func waitForJobStatus(t *testing.T, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, state := range tools.SnapshotJobs() {
			if state.ID == id && state.Status == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never reached status %q", id, want)
}

func newJobsTestModel(t *testing.T, width, height int) *Model {
	t.Helper()
	resetJobsRegistry(t)
	m := NewModelWithSize(newInfoPanelAgent(), width, height)
	m.updateRightPanelVisible()
	m.recalcViewportSize()
	m.layout = m.generateLayout(m.width, m.height)
	m.infoPanelScrollOffset = 0
	return &m
}

func refreshJobs(m *Model) {
	m.invalidateJobSnapshot()
}

// jobsSectionRawLines returns the JOBS section's content lines with their
// leading indentation intact, so a test can assert the 2-column inset.
func jobsSectionRawLines(m *Model, panelWidth, panelHeight int) []string {
	raw := infoPanelRawLines(m.renderInfoPanel(panelWidth, panelHeight))
	start := -1
	for i, line := range raw {
		if strings.Contains(line, "JOBS") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var section []string
	for j := start + 1; j < len(raw); j++ {
		text := strings.TrimSpace(raw[j])
		if text == "" || strings.HasPrefix(text, "▼") || strings.HasPrefix(text, "▶") {
			break
		}
		section = append(section, raw[j])
	}
	return section
}

func jobHitBox(m *Model, jobID string) (infoPanelSectionHitBox, bool) {
	for _, hit := range m.infoPanelHitBoxes {
		if hit.jobID == jobID {
			return hit, true
		}
	}
	return infoPanelSectionHitBox{}, false
}

// --- info panel display ----------------------------------------------------

func TestInfoPanelJobsShowsRunningJob(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "previous", StartedAt: time.Now().Add(-time.Minute)})
	id := startTestJob(t, "sleep 60", "Run the nightly build")
	refreshJobs(m)

	lines := jobsSectionRawLines(m, 32, 60)
	if len(lines) != 1 {
		t.Fatalf("JOBS section lines = %d, want 1\n%v", len(lines), lines)
	}
	row := lines[0]
	plain := stripANSI(row)
	if !strings.HasPrefix(plain, "  ") {
		t.Fatalf("job row must be indented by 2 columns, got %q", plain)
	}
	if !strings.Contains(plain, statusIndicator("running", false)) {
		t.Fatalf("job row missing the running status dot: %q", plain)
	}
	if !strings.Contains(plain, "Run") || !strings.Contains(plain, "...") {
		t.Fatalf("job row should contain the truncated label, got %q", plain)
	}
	if !strings.Contains(plain, "0s") {
		t.Fatalf("job row missing the elapsed column, got %q", plain)
	}
	if !strings.HasSuffix(strings.TrimRight(plain, " "), jobStopGlyph) {
		t.Fatalf("running job row must end with the stop affordance, got %q", plain)
	}
	if _, ok := jobHitBox(m, id); !ok {
		t.Fatalf("no hit box recorded for job %s", id)
	}

	// The info panel is visible on this width, so the status bar keeps its idle
	// lane and does not add the redundant jobs/agents pill.
	status := stripANSI(m.renderStatusBar())
	if !strings.Contains(status, statusBarIdleLabel()) {
		t.Fatalf("wide status bar should keep the idle lane, got %q", status)
	}
	if m.statusJobs.display != "" {
		t.Fatalf("wide status bar must not render the fallback pill, got %q", m.statusJobs.display)
	}
}

func TestInfoPanelJobsHiddenWithoutRunningJobs(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	refreshJobs(m)
	if lines := jobsSectionRawLines(m, 32, 60); len(lines) != 0 {
		t.Fatalf("JOBS section rendered without running jobs: %v", lines)
	}

	id := startTestJob(t, "true", "finishes right away")
	waitForJobStatus(t, id, "completed")
	refreshJobs(m)
	if lines := jobsSectionRawLines(m, 32, 60); len(lines) != 0 {
		t.Fatalf("completed jobs must not keep the JOBS section alive: %v", lines)
	}
}

func TestInfoPanelJobsShowsSubAgentJob(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	started := time.Now().Add(-2 * time.Minute)
	m.jobsSnapshot = []tools.JobState{{
		ID:          "job-subagent",
		AgentID:     "agent-1",
		Description: "child build",
		Command:     "make",
		Status:      jobStatusRunning,
		StartedAt:   started,
	}}
	m.activeJobsSnapshot = []tools.JobState{m.jobsSnapshot[0]}
	m.jobsSnapshotValid = true
	m.jobsSnapshotFrame = m.renderFrameGeneration

	lines := jobsSectionRawLines(m, 32, 60)
	if len(lines) != 1 || !strings.Contains(stripANSI(lines[0]), "child build") {
		t.Fatalf("a sub-agent job must appear in the JOBS section, got %v", lines)
	}
}

func TestInfoPanelJobsTruncatesLabelButKeepsElapsedAndStop(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	started := time.Now().Add(-12*time.Minute - 3*time.Second)
	m.jobsSnapshot = []tools.JobState{{
		ID:          "job-1",
		Description: strings.Repeat("long-label-segment ", 8),
		Status:      jobStatusRunning,
		StartedAt:   started,
	}}
	m.activeJobsSnapshot = []tools.JobState{m.jobsSnapshot[0]}
	m.jobsSnapshotValid = true
	m.jobsSnapshotFrame = m.renderFrameGeneration

	lines := jobsSectionRawLines(m, 32, 60)
	if len(lines) != 1 {
		t.Fatalf("JOBS section lines = %d, want 1\n%v", len(lines), lines)
	}
	plain := stripANSI(lines[0])
	if !strings.Contains(plain, "12m03s") {
		t.Fatalf("narrow panel dropped the elapsed column: %q", plain)
	}
	if !strings.HasSuffix(strings.TrimRight(plain, " "), jobStopGlyph) {
		t.Fatalf("narrow panel dropped the stop affordance: %q", plain)
	}
	if !strings.Contains(plain, "...") {
		t.Fatalf("long label should be truncated, got %q", plain)
	}
}

func TestInfoPanelJobsKeepsFullLabelWhenElapsedIsShort(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	// A label this long fits only because the row reserves the elapsed's actual
	// width ("0s") instead of a fixed column.
	startTestJob(t, "sleep 60", "build-and-test-now")
	refreshJobs(m)

	lines := jobsSectionRawLines(m, 32, 60)
	if len(lines) != 1 {
		t.Fatalf("JOBS section lines = %d, want 1\n%v", len(lines), lines)
	}
	plain := stripANSI(lines[0])
	if strings.Contains(plain, "...") {
		t.Fatalf("label should keep its space when the elapsed is short, got %q", plain)
	}
	if !strings.Contains(plain, "build-and-test-now") {
		t.Fatalf("job row lost part of the label: %q", plain)
	}
	if !strings.HasSuffix(strings.TrimRight(plain, " "), jobStopGlyph) {
		t.Fatalf("job row must still end with the stop affordance, got %q", plain)
	}
}

func TestInfoPanelStoppingJobDropsStopAffordance(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	m.jobsSnapshot = []tools.JobState{{
		ID:          "job-1",
		Description: "winding down",
		Status:      jobStatusStopping,
		StartedAt:   time.Now().Add(-time.Minute),
	}}
	m.activeJobsSnapshot = []tools.JobState{m.jobsSnapshot[0]}
	m.jobsSnapshotValid = true
	m.jobsSnapshotFrame = m.renderFrameGeneration

	lines := jobsSectionRawLines(m, 32, 60)
	if len(lines) != 1 {
		t.Fatalf("JOBS section lines = %d, want 1\n%v", len(lines), lines)
	}
	plain := strings.TrimRight(stripANSI(lines[0]), " ")
	if strings.HasSuffix(plain, jobStopGlyph) {
		t.Fatalf("a stopping job must not offer the stop affordance: %q", plain)
	}
	if !strings.Contains(plain, statusIndicator("retrying", false)) {
		t.Fatalf("stopping job should use the retrying-family dot: %q", plain)
	}
	hit, ok := jobHitBox(m, "job-1")
	if !ok {
		t.Fatal("a stopping job should still be listed")
	}
	if hit.stopZoneEndX > hit.stopZoneStartX {
		t.Fatalf("a stopping job row must not carry a stop zone: %+v", hit)
	}
}

func TestInfoPanelCollapsedJobsShowsHeaderOnly(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	id := startTestJob(t, "sleep 60", "collapsed job")
	refreshJobs(m)
	m.toggleInfoPanelSection(infoPanelSectionJobs)

	rendered := stripANSI(m.renderInfoPanel(32, 60))
	if !strings.Contains(rendered, "▶ JOBS") || !strings.Contains(rendered, "· 1") {
		t.Fatalf("collapsed JOBS header missing count: %q", rendered)
	}
	if lines := jobsSectionRawLines(m, 32, 60); len(lines) != 0 {
		t.Fatalf("collapsed JOBS section should render no rows, got %v", lines)
	}
	if _, ok := jobHitBox(m, id); ok {
		t.Fatal("collapsed JOBS section should not record row hit boxes")
	}
}

// --- fingerprint -----------------------------------------------------------

func TestInfoPanelFingerprintTracksRunningJobs(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	refreshJobs(m)
	before := m.infoPanelFingerprint(32, 0)

	id := startTestJob(t, "sleep 60", "fingerprint job")
	refreshJobs(m)
	bucket := time.Now().Unix()
	withJob := m.infoPanelFingerprint(32, 0)
	if withJob == before {
		t.Fatal("fingerprint did not change when a job started")
	}
	if again := m.infoPanelFingerprint(32, 0); again != withJob && time.Now().Unix() == bucket {
		t.Fatal("fingerprint changed within the same second bucket")
	}

	tools.StopJobByUser(id, "test")
	waitForJobStatus(t, id, "killed")
	refreshJobs(m)
	killed := m.infoPanelFingerprint(32, 0)
	if killed == withJob {
		t.Fatal("fingerprint did not change when the job was killed")
	}
	if killed != before {
		t.Fatal("fingerprint with no running jobs should match the empty baseline")
	}
}

// --- panel click -> stop confirm ------------------------------------------

func openConfirmFromPanel(t *testing.T, m *Model, jobID string) {
	t.Helper()
	hit, ok := jobHitBox(m, jobID)
	if !ok {
		t.Fatalf("no hit box for job %s", jobID)
	}
	mouse := tea.Mouse{
		X:      m.layout.infoPanel.Min.X + hit.stopZoneStartX + 1,
		Y:      m.layout.infoPanel.Min.Y + hit.startY,
		Button: tea.MouseLeft,
	}
	if cmd := m.handleInfoPanelMouseClick(mouse); cmd != nil {
		_ = cmd
	}
}

func TestPanelStopClickOpensConfirmWithoutStopping(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	id := startTestJob(t, "sleep 60", "stop via click")
	refreshJobs(m)
	jobsSectionRawLines(m, 32, 60) // render to record hit boxes

	openConfirmFromPanel(t, m, id)
	if m.mode != ModeStopJobConfirm {
		t.Fatalf("mode = %v, want ModeStopJobConfirm", m.mode)
	}
	for _, state := range tools.SnapshotJobs() {
		if state.ID == id && state.Status != jobStatusRunning {
			t.Fatalf("clicking the row must not stop the job, status = %q", state.Status)
		}
	}
}

func TestPanelRowBodyClickDoesNothing(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	id := startTestJob(t, "sleep 60", "row body click")
	refreshJobs(m)
	jobsSectionRawLines(m, 32, 60)

	hit, ok := jobHitBox(m, id)
	if !ok {
		t.Fatalf("no hit box for job %s", id)
	}
	beforeMode := m.mode
	mouse := tea.Mouse{X: m.layout.infoPanel.Min.X + 2, Y: m.layout.infoPanel.Min.Y + hit.startY, Button: tea.MouseLeft}
	m.handleInfoPanelMouseClick(mouse)
	if m.mode != beforeMode {
		t.Fatalf("clicking the row body changed mode to %v", m.mode)
	}
}

// --- stop confirm dialog ---------------------------------------------------

func TestStopJobConfirmEnterDoesNotConfirm(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	id := startTestJob(t, "sleep 60", "enter is inert")
	refreshJobs(m)

	m.openStopJobConfirm(id)
	if m.mode != ModeStopJobConfirm {
		t.Fatalf("mode = %v, want ModeStopJobConfirm", m.mode)
	}
	m.handleStopJobConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != ModeStopJobConfirm {
		t.Fatal("enter must not close the stop confirmation")
	}
	for _, state := range tools.SnapshotJobs() {
		if state.ID == id && state.Status != jobStatusRunning {
			t.Fatalf("enter must not stop the job, status = %q", state.Status)
		}
	}
}

func TestStopJobConfirmYStopsJob(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	id := startTestJob(t, "sleep 60", "stop with y")
	m.openStopJobConfirm(id)

	m.handleStopJobConfirmKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if m.mode == ModeStopJobConfirm {
		t.Fatal("y must close the stop confirmation")
	}
	// The stop is recorded synchronously, and the process may reach its
	// terminal state at any moment, so only assert that the job is no longer
	// running. The transient stopping row is covered deterministically by
	// TestInfoPanelStoppingJobDropsStopAffordance.
	for _, state := range tools.SnapshotJobs() {
		if state.ID == id && state.Status == jobStatusRunning {
			t.Fatalf("y must stop the job, status = %q", state.Status)
		}
	}
}

func TestStopJobConfirmCancelKeepsJobRunning(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		tea.KeyPressMsg(tea.Key{Text: "n", Code: 'n'}),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}),
	} {
		m := newJobsTestModel(t, 120, 40)
		id := startTestJob(t, "sleep 60", "cancel stop")
		refreshJobs(m)
		m.openStopJobConfirm(id)

		m.handleStopJobConfirmKey(key)
		if m.mode == ModeStopJobConfirm {
			t.Fatalf("key %v must close the stop confirmation", key)
		}
		for _, state := range tools.SnapshotJobs() {
			if state.ID == id && state.Status != jobStatusRunning {
				t.Fatalf("cancel must not stop the job, status = %q", state.Status)
			}
		}
	}
}

func TestStopJobConfirmCancelledFromOverlayReturnsToOverlay(t *testing.T) {
	m := newJobsTestModel(t, 100, 40)
	id := startTestJob(t, "sleep 60", "overlay stop")
	refreshJobs(m)
	if cmd := m.openJobsOverlay(); cmd != nil {
		t.Fatal("opening the jobs overlay should not produce a toast")
	}
	if m.mode != ModeJobsOverlay {
		t.Fatalf("mode = %v, want ModeJobsOverlay", m.mode)
	}
	m.openStopJobConfirm(id)
	if m.mode != ModeStopJobConfirm {
		t.Fatalf("openStopJobConfirm left mode at %v", m.mode)
	}
	m.handleStopJobConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.mode != ModeJobsOverlay {
		t.Fatalf("cancelling the stop confirmation must return to the overlay, mode = %v", m.mode)
	}
}

func TestStopJobConfirmDialogRendersFields(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	m.stopJobConfirm = stopJobConfirmState{
		jobID:        "job-64",
		label:        "Run production build",
		command:      "make build",
		owner:        "agent-1",
		status:       jobStatusRunning,
		startedAt:    time.Now().Add(-65 * time.Second),
		lastOutputAt: time.Now().Add(-3 * time.Second),
		logFile:      "/tmp/job-64.log",
		tail:         "line one\nline two\n",
		prevMode:     ModeNormal,
		openedAt:     time.Now(),
	}
	m.mode = ModeStopJobConfirm

	dialog := stripANSI(m.renderStopJobConfirmDialog())
	for _, want := range []string{
		"⚠ Stop job-64?",
		"Label: Run production build",
		"Command:",
		"make build",
		"Owner: agent-1",
		"Status: running",
		"Elapsed: 1m05s",
		"Last output: ",
		"Recent output (last 12 lines):",
		"line one",
		"line two",
		"Stopping sends SIGTERM, then SIGKILL after a grace period.",
		"[y] Stop",
		"[n/esc] Cancel",
	} {
		if !strings.Contains(dialog, want) {
			t.Fatalf("stop dialog missing %q:\n%s", want, dialog)
		}
	}
	if strings.Contains(dialog, "recent log:") {
		t.Fatalf("no dropped bytes, no truncation: log hint must be hidden:\n%s", dialog)
	}
}

func TestStopJobConfirmDialogNoOutputYet(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	m.stopJobConfirm = stopJobConfirmState{
		jobID:     "job-7",
		command:   "sleep 60",
		status:    jobStatusRunning,
		startedAt: time.Now(),
		prevMode:  ModeNormal,
		openedAt:  time.Now(),
	}
	m.mode = ModeStopJobConfirm
	dialog := stripANSI(m.renderStopJobConfirmDialog())
	if !strings.Contains(dialog, "Last output: (no output yet)") {
		t.Fatalf("zero last-output time should read as no output yet:\n%s", dialog)
	}
	if !strings.Contains(dialog, "(no output yet)") {
		t.Fatalf("empty tail should read as no output yet:\n%s", dialog)
	}
}

func TestStopJobConfirmDialogDroppedBytesNotice(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	m.stopJobConfirm = stopJobConfirmState{
		jobID:        "job-9",
		command:      "make",
		status:       jobStatusRunning,
		startedAt:    time.Now(),
		logFile:      "/tmp/job-9.log",
		tail:         "tail\n",
		droppedBytes: 4096,
		prevMode:     ModeNormal,
		openedAt:     time.Now(),
	}
	m.mode = ModeStopJobConfirm
	dialog := stripANSI(m.renderStopJobConfirmDialog())
	want := "(skipped 4096 bytes of earlier output; recent log: /tmp/job-9.log)"
	if !strings.Contains(dialog, want) {
		t.Fatalf("dropped-output notice missing %q:\n%s", want, dialog)
	}
}

func TestStopJobConfirmDialogWrapsLongCommandWithinWidth(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	maxWidth := max(min(m.width-6, 90), 40)
	m.stopJobConfirm = stopJobConfirmState{
		jobID:     "job-1",
		command:   strings.Repeat("--very-long-flag=value ", 20),
		status:    jobStatusRunning,
		startedAt: time.Now(),
		prevMode:  ModeNormal,
		openedAt:  time.Now(),
	}
	m.mode = ModeStopJobConfirm
	dialog := m.renderStopJobConfirmDialog()
	for line := range strings.SplitSeq(dialog, "\n") {
		if w := ansi.StringWidth(line); w > maxWidth {
			t.Fatalf("dialog line overflows its width (%d > %d): %q", w, maxWidth, stripANSI(line))
		}
	}
	if !strings.Contains(stripANSI(dialog), "…") {
		t.Fatalf("an over-long command should end with an ellipsis:\n%s", stripANSI(dialog))
	}
}

func TestStopJobConfirmDialogCapsTailAtTwelveLines(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	id := startTestJob(t, "seq 1 20; sleep 60", "tail cap")
	waitForJobTail(t, id, "\n20")
	refreshJobs(m)
	m.openStopJobConfirm(id)

	dialog := m.renderStopJobConfirmDialog()
	lines := make(map[string]bool)
	for line := range strings.SplitSeq(stripANSI(dialog), "\n") {
		text := strings.TrimSpace(line)
		text = strings.TrimSpace(strings.Trim(text, "│"))
		lines[text] = true
	}
	for n := 9; n <= 20; n++ {
		if !lines[strconv.Itoa(n)] {
			t.Fatalf("tail is missing line %d (last 12 lines expected):\n%s", n, dialog)
		}
	}
	for n := 1; n <= 8; n++ {
		if lines[strconv.Itoa(n)] {
			t.Fatalf("tail must be capped at the last 12 lines, found line %d:\n%s", n, dialog)
		}
	}
}

func TestStopJobPeekDoesNotConsumeOutput(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	ctx := tools.WithAgentID(context.Background(), "agent-1")
	id, err := tools.ExecuteJobForTest(ctx, "printf 'one\\ntwo\\n'; sleep 60", "non-consuming peek", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest error: %v", err)
	}
	t.Cleanup(func() { tools.StopJobByUser(id, "test cleanup") })
	waitForJobTail(t, id, "two")
	refreshJobs(m)
	m.openStopJobConfirm(id)
	_ = m.renderStopJobConfirmDialog()

	read, err := tools.JobOutputTool{}.Execute(ctx, []byte(`{"job_id":"`+id+`"}`))
	if err != nil {
		t.Fatalf("job_output after peeking failed: %v", err)
	}
	if !strings.Contains(read, "one") || !strings.Contains(read, "two") {
		t.Fatalf("peeking for the dialog consumed output; job_output read %q", read)
	}
}

// --- overlay ---------------------------------------------------------------

func TestJobsOverlayEmptyShowsToast(t *testing.T) {
	m := newJobsTestModel(t, 100, 40)
	refreshJobs(m)
	if cmd := m.openJobsOverlay(); cmd == nil {
		t.Fatal("opening the jobs overlay with no jobs should produce a toast")
	}
	if m.mode == ModeJobsOverlay {
		t.Fatal("empty jobs overlay must not open")
	}
}

func TestJobsOverlayOpensFromStatusPill(t *testing.T) {
	m := newJobsTestModel(t, 100, 40)
	startTestJob(t, "sleep 60", "pill click")
	refreshJobs(m)
	m.renderStatusBar()
	if m.statusJobs.display == "" {
		t.Fatal("narrow layout should render the background-activity pill")
	}
	if !strings.Contains(m.statusJobs.display, "job") {
		t.Fatalf("pill = %q, want a job count", m.statusJobs.display)
	}
	cmd, handled := m.handleStatusCopyClick(m.statusJobs.startX+1, m.layout.status.Min.Y)
	if !handled {
		t.Fatal("clicking the pill should be handled")
	}
	_ = cmd
	if m.mode != ModeJobsOverlay {
		t.Fatalf("mode = %v, want ModeJobsOverlay", m.mode)
	}
}

func TestJobsOverlayPillHiddenWhenInfoPanelVisible(t *testing.T) {
	m := newJobsTestModel(t, 160, 40)
	startTestJob(t, "sleep 60", "wide panel")
	refreshJobs(m)
	m.renderStatusBar()
	if m.statusJobs.display != "" {
		t.Fatalf("wide layout should not duplicate the panel counts, pill = %q", m.statusJobs.display)
	}
}

func TestJobsPillCountsStoppingJobsAndFallbackAgents(t *testing.T) {
	m := newJobsTestModel(t, 100, 40)
	m.jobsSnapshot = []tools.JobState{{
		ID:          "job-1",
		Description: "winding down",
		Status:      jobStatusStopping,
		StartedAt:   time.Now(),
	}}
	m.activeJobsSnapshot = []tools.JobState{m.jobsSnapshot[0]}
	m.jobsSnapshotValid = true
	m.jobsSnapshotFrame = m.renderFrameGeneration
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", State: "running"}}, "", "")

	m.renderStatusBar()
	if !strings.Contains(m.statusJobs.display, "1 job") || !strings.Contains(m.statusJobs.display, "1 agent") {
		t.Fatalf("pill = %q, want both the stopping job and the running worker", m.statusJobs.display)
	}
}

func TestJobsOverlayRowStopZoneOpensConfirm(t *testing.T) {
	m := newJobsTestModel(t, 100, 40)
	id := startTestJob(t, "sleep 60", "overlay row stop")
	refreshJobs(m)
	m.openJobsOverlay()

	dialog := m.renderJobsOverlayDialog()
	rect := centeredRect(m.ensureLayout().area, dialog)
	innerWidth := m.jobsOverlayInnerWidth()
	contentLeft := rect.Min.X + DirectoryBorderStyle.GetBorderLeftSize() + DirectoryBorderStyle.GetPaddingLeft()
	x := contentLeft + innerWidth - 2
	y := rect.Min.Y + 3

	m.handleMouseMsg(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.mode != ModeStopJobConfirm {
		t.Fatalf("clicking the overlay row stop zone = %v, want ModeStopJobConfirm", m.mode)
	}
	for _, state := range tools.SnapshotJobs() {
		if state.ID == id && state.Status != jobStatusRunning {
			t.Fatalf("overlay row click must not stop the job, status = %q", state.Status)
		}
	}
}

func TestJobsOverlayFitsNarrowTerminal(t *testing.T) {
	for _, width := range []int{40, 50, 60, 71} {
		m := newJobsTestModel(t, width, 40)
		startTestJob(t, "sleep 60", "narrow overlay")
		refreshJobs(m)
		m.openJobsOverlay()
		dialog := m.renderJobsOverlayDialog()
		if dialog == "" {
			t.Fatalf("width %d: empty dialog", width)
		}
		if got := lipgloss.Width(strings.Split(dialog, "\n")[0]); got > width-1 {
			t.Fatalf("width %d: dialog line width %d exceeds drawable width %d", width, got, width-1)
		}
	}
}

func TestJobsOverlayEscCloses(t *testing.T) {
	m := newJobsTestModel(t, 100, 40)
	startTestJob(t, "sleep 60", "overlay esc")
	refreshJobs(m)
	m.openJobsOverlay()
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.mode == ModeJobsOverlay {
		t.Fatal("esc must close the jobs overlay")
	}
}

func TestJobsOverlayPicksUpNewJobs(t *testing.T) {
	m := newJobsTestModel(t, 100, 80)
	startTestJob(t, "sleep 60", "first job")
	refreshJobs(m)
	m.openJobsOverlay()
	if rows := overlayContentRows(m.renderJobsOverlayDialog()); rows != 1 {
		t.Fatalf("overlay rows = %d, want 1", rows)
	}
	startTestJob(t, "sleep 60", "second job")
	refreshJobs(m)
	if rows := overlayContentRows(m.renderJobsOverlayDialog()); rows != 2 {
		t.Fatalf("overlay must list the newly started job, rows = %d", rows)
	}
}

// overlayContentRows counts the job rows between the overlay's title block and
// its hint line.
func overlayContentRows(dialog string) int {
	lines := strings.Split(stripANSI(dialog), "\n")
	rows := 0
	for _, line := range lines {
		text := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│"))
		if strings.HasPrefix(text, statusIndicator("running", false)) {
			rows++
		}
	}
	return rows
}

// jobOverlayRowLines returns the raw (still ANSI-styled) job rows of the jobs
// overlay, so a test can inspect which surface each one is drawn on.
func jobOverlayRowLines(dialog string) []string {
	var rows []string
	for line := range strings.SplitSeq(dialog, "\n") {
		if strings.Contains(stripANSI(line), statusIndicator("running", false)) {
			rows = append(rows, line)
		}
	}
	return rows
}

// Every segment of a job row paints its own background, so the selected row has
// to be redrawn on the selection surface instead of being wrapped by an outer
// style (whose color the row's own SGR overdraws). Repainting must not move the
// row: the stop affordance is mapped back from fixed columns.
func TestJobsOverlaySelectedRowUsesSelectionSurface(t *testing.T) {
	if currentTheme.SelectedBg == "" || currentTheme.SelectedBg == currentTheme.DialogBg {
		t.Skip("theme does not draw a distinct selection surface")
	}
	want := colorToANSIBgSeq(currentTheme.SelectedBg)
	if want == "" {
		t.Skip("theme selection surface is not expressible as an ANSI background")
	}
	m := newJobsTestModel(t, 120, 40)
	startTestJob(t, "sleep 60", "first job")
	startTestJob(t, "sleep 60", "second job")
	refreshJobs(m)
	m.openJobsOverlay()

	m.jobsOverlay.cursor = 0
	rows := jobOverlayRowLines(m.renderJobsOverlayDialog())
	if len(rows) != 2 {
		t.Fatalf("overlay rows = %d, want 2", len(rows))
	}
	if got := rowCellBackground(rows[0]); got != want {
		t.Fatalf("selected row's first cell background = %q, want %q (row %q)", got, want, rows[0])
	}
	if got := rowCellBackground(rows[1]); got == want {
		t.Fatalf("unselected row is drawn on the selection surface: %q", rows[1])
	}
	plainFirst := stripANSI(rows[0])

	m.jobsOverlay.cursor = 1
	rows = jobOverlayRowLines(m.renderJobsOverlayDialog())
	if len(rows) != 2 {
		t.Fatalf("overlay rows = %d, want 2", len(rows))
	}
	if stripANSI(rows[0]) != plainFirst {
		t.Fatalf("selection changed the row layout: %q vs %q", stripANSI(rows[0]), plainFirst)
	}
	if got := rowCellBackground(rows[0]); got == want {
		t.Fatalf("row 0 kept the selection surface after the cursor moved: %q", rows[0])
	}
	if got := rowCellBackground(rows[1]); got != want {
		t.Fatalf("selection surface did not follow the cursor to row 1: %q", rows[1])
	}
}

// rowCellBackground returns the background the SGR sequences select for the
// first non-space cell after a dialog row's left border, skipping the border
// and its padding. Comparing the background active at the cell, rather than the
// presence of a sequence, is what distinguishes a real selection surface from
// an outer style wrap that the row's own SGR overdraws.
func rowCellBackground(line string) string {
	rest := line
	if i := strings.IndexRune(rest, '│'); i >= 0 {
		rest = rest[i+len("│"):]
	}
	bg := ""
	for i := 0; i < len(rest); {
		if rest[i] == '\x1b' {
			end := skipANSISequence(rest, i)
			if seq, ok := sgrBackground(rest[i:end]); ok {
				bg = seq
			}
			i = end
			continue
		}
		if rest[i] == ' ' {
			i++
			continue
		}
		return bg
	}
	return bg
}

// sgrBackground extracts the background a single SGR sequence selects. The
// second result is false when the sequence leaves the background unchanged.
func sgrBackground(seq string) (string, bool) {
	body, ok := strings.CutPrefix(seq, "\x1b[")
	if !ok {
		return "", false
	}
	body, ok = strings.CutSuffix(body, "m")
	if !ok {
		return "", false
	}
	params := strings.Split(body, ";")
	for i := range len(params) {
		switch params[i] {
		case "0", "", "49":
			return "", true
		case "48":
			if i+2 < len(params) && params[i+1] == "5" {
				return "\x1b[48;5;" + params[i+2] + "m", true
			}
			if i+4 < len(params) && params[i+1] == "2" {
				return "\x1b[48;2;" + params[i+2] + ";" + params[i+3] + ";" + params[i+4] + "m", true
			}
		}
	}
	return "", false
}

// --- dialog priority -------------------------------------------------------

func TestStopJobConfirmQueuesNewConfirmRequests(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	id := startTestJob(t, "sleep 60", "priority")
	refreshJobs(m)
	m.openStopJobConfirm(id)
	if !m.dialogActive() {
		t.Fatal("the stop confirmation must count as an active dialog")
	}
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	if len(m.pendingDialogs) != 1 {
		t.Fatalf("pendingDialogs = %d, want the new confirm queued", len(m.pendingDialogs))
	}
}

func TestStopJobConfirmReplaysQueuedDialogs(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}),
		tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}),
	} {
		m := newJobsTestModel(t, 120, 40)
		id := startTestJob(t, "sleep 60", "replay queued dialog")
		refreshJobs(m)
		m.openStopJobConfirm(id)
		m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
		if len(m.pendingDialogs) != 1 {
			t.Fatalf("setup: pendingDialogs = %d, want 1", len(m.pendingDialogs))
		}

		m.handleStopJobConfirmKey(key)
		if m.mode != ModeConfirm || m.confirm.request == nil {
			t.Fatalf("key %v: queued confirm was not presented, mode = %v", key, m.mode)
		}
		if len(m.pendingDialogs) != 0 {
			t.Fatalf("key %v: pendingDialogs = %d, want 0", key, len(m.pendingDialogs))
		}
	}
}

func TestStopJobConfirmRestoresPrevModeWithoutQueue(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	m.mode = ModeNormal
	id := startTestJob(t, "sleep 60", "restore previous mode")
	refreshJobs(m)
	m.openStopJobConfirm(id)

	m.handleStopJobConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want the previous ModeNormal", m.mode)
	}
	if len(m.pendingDialogs) != 0 {
		t.Fatalf("pendingDialogs = %d, want none", len(m.pendingDialogs))
	}
}

func TestStopJobConfirmClearsTerminalTitleRequest(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	m.terminalTitleBase = "job title"
	id := startTestJob(t, "sleep 60", "title request")
	refreshJobs(m)
	m.openStopJobConfirm(id)

	// While the dialog is open, any title sync (blur, job event) must show the
	// request icon and leave no spinner ticker running.
	m.syncTerminalTitleState()
	if got := m.currentTitleMode(); got != terminalTitleModeRequest {
		t.Fatalf("title mode while the stop dialog is open = %v, want request", got)
	}
	if m.terminalTitleTickRunning {
		t.Fatal("the request title must not keep the spinner ticker running")
	}

	m.handleStopJobConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if got := m.currentTitleMode(); got != terminalTitleModeSpinner {
		t.Fatalf("title mode after closing the stop dialog = %v, want the spinner", got)
	}
	if m.terminalTitleRequestSeen {
		t.Fatal("terminalTitleRequestSeen must be reset when the stop dialog closes")
	}
	// Closing re-syncs the title, so the spinner ticker runs again instead of the
	// frozen request state surviving until some unrelated sync.
	if !m.terminalTitleTickRunning {
		t.Fatal("closing the stop dialog must re-sync the title and restart the spinner ticker")
	}
}

func TestToolConfirmBlocksPanelStopClick(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	id := startTestJob(t, "sleep 60", "blocked stop")
	refreshJobs(m)
	jobsSectionRawLines(m, 32, 60)

	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: "{}"}
	hit, ok := jobHitBox(m, id)
	if !ok {
		t.Fatalf("no hit box for job %s", id)
	}
	m.handleMouseMsg(tea.MouseClickMsg{
		X:      m.layout.infoPanel.Min.X + hit.stopZoneStartX + 1,
		Y:      m.layout.infoPanel.Min.Y + hit.startY,
		Button: tea.MouseLeft,
	})
	if m.mode != ModeConfirm {
		t.Fatalf("an open tool confirmation must swallow the panel click, mode = %v", m.mode)
	}
}

// --- animation -------------------------------------------------------------

func TestHasActiveAnimationTracksJobsOnly(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	refreshJobs(m)
	if m.hasActiveAnimation() {
		t.Fatal("idle model with no jobs should have no active animation")
	}
	id := startTestJob(t, "sleep 60", "animation job")
	refreshJobs(m)
	if !m.hasActiveAnimation() {
		t.Fatal("a running job should keep the animation active")
	}
	tools.StopJobByUser(id, "test")
	waitForJobStatus(t, id, "killed")
	refreshJobs(m)
	if m.hasActiveAnimation() {
		t.Fatal("animation should stop once the last job finishes")
	}
}

// --- status pill text ------------------------------------------------------

func TestFormatJobsActivityPillLadder(t *testing.T) {
	cases := []struct {
		jobs   int
		agents int
		avail  int
		want   string
	}{
		{jobs: 1, agents: 2, avail: 40, want: "2 agents · 1 job"},
		{jobs: 1, agents: 2, avail: 6, want: "1 job"},
		{jobs: 2, agents: 0, avail: 40, want: "2 jobs"},
		{jobs: 0, agents: 1, avail: 40, want: "1 agent"},
		{jobs: 0, agents: 0, avail: 40, want: ""},
		{jobs: 1, agents: 0, avail: 2, want: ""},
		{jobs: 1, agents: 0, avail: 0, want: ""},
	}
	for _, tc := range cases {
		if got := formatJobsActivityPill(tc.jobs, tc.agents, tc.avail); got != tc.want {
			t.Fatalf("formatJobsActivityPill(%d, %d, %d) = %q, want %q", tc.jobs, tc.agents, tc.avail, got, tc.want)
		}
	}
}

func TestStopJobTruncationNotice(t *testing.T) {
	if got := stopJobTruncationNotice(0, false, ""); got != "" {
		t.Fatalf("no truncation should produce no notice, got %q", got)
	}
	if got := stopJobTruncationNotice(1024, false, "/tmp/a.log"); got != "(skipped 1024 bytes of earlier output; recent log: /tmp/a.log)" {
		t.Fatalf("dropped notice = %q", got)
	}
	if got := stopJobTruncationNotice(0, true, "/tmp/a.log"); !strings.Contains(got, "/tmp/a.log") {
		t.Fatalf("truncated notice should keep the log path, got %q", got)
	}
}

// --- review follow-ups -----------------------------------------------------

func TestStopJobConfirmDialogTruncatesLongLogPath(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	maxWidth := max(min(m.width-6, 90), 40)
	m.stopJobConfirm = stopJobConfirmState{
		jobID:        "job-1",
		command:      "make",
		status:       jobStatusRunning,
		startedAt:    time.Now(),
		logFile:      "/tmp/" + strings.Repeat("segment/", 40) + "job.log",
		tail:         "tail\n",
		droppedBytes: 4096,
		prevMode:     ModeNormal,
		openedAt:     time.Now(),
	}
	m.mode = ModeStopJobConfirm

	dialog := m.renderStopJobConfirmDialog()
	for line := range strings.SplitSeq(dialog, "\n") {
		if w := ansi.StringWidth(line); w > maxWidth {
			t.Fatalf("dialog line overflows its width (%d > %d): %q", w, maxWidth, stripANSI(line))
		}
	}
	plain := stripANSI(dialog)
	if !strings.Contains(plain, "(skipped 4096 bytes of earlier output") {
		t.Fatalf("expected the dropped-output notice:\n%s", plain)
	}
	if !strings.Contains(plain, "...") {
		t.Fatalf("the over-long log path should be truncated:\n%s", plain)
	}
}

func TestBackgroundJobKeepsTerminalTitleSpinnerAlive(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.terminalTitleBase = "background job"
	startTestJob(t, "sleep 60", "title spinner")
	refreshJobs(m)

	desired := m.deriveTerminalTitleState()
	if desired.mode != terminalTitleModeSpinner {
		t.Fatalf("title mode = %v, want spinner", desired.mode)
	}
	if desired.tickerDelay <= 0 {
		t.Fatalf("title ticker delay = %v, want a positive delay while a job runs", desired.tickerDelay)
	}
	if desired.tickerDelay != backgroundTitleSpinnerCadence {
		t.Fatalf("title ticker delay = %v, want the background-active cadence %v", desired.tickerDelay, backgroundTitleSpinnerCadence)
	}
	if cmd := m.syncTerminalTitleState(); cmd == nil {
		t.Fatal("syncing the title should schedule a tick")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("the title ticker should be running")
	}
	// A tick must re-schedule at a positive delay instead of stopping the ticker.
	next := m.handleTerminalTitleTick(terminalTitleTickMsg{generation: m.terminalTitleTickGeneration})
	if next == nil {
		t.Fatal("a background-job spinner tick must re-schedule, not stop")
	}
}

func TestJobRowClampsLongElapsedKeepingStopZone(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	started := time.Now().Add(-1000 * time.Hour)
	m.jobsSnapshot = []tools.JobState{{
		ID:          "job-1",
		Description: "long runner",
		Status:      jobStatusRunning,
		StartedAt:   started,
	}}
	m.activeJobsSnapshot = []tools.JobState{m.jobsSnapshot[0]}
	m.jobsSnapshotValid = true
	m.jobsSnapshotFrame = m.renderFrameGeneration

	if raw := tools.FormatElapsed(time.Since(started)); ansi.StringWidth(raw) <= jobElapsedColumnWidth {
		t.Fatalf("setup: %q should be wider than the elapsed column", raw)
	}
	lines := jobsSectionRawLines(m, 32, 60)
	if len(lines) != 1 {
		t.Fatalf("JOBS section lines = %d, want 1\n%v", len(lines), lines)
	}
	plain := strings.TrimRight(stripANSI(lines[0]), " ")
	if !strings.HasSuffix(plain, jobStopGlyph) {
		t.Fatalf("clamped row lost the stop affordance: %q", plain)
	}
	// The glyph must stay inside the recorded 4-column stop zone: an unclamped
	// elapsed would widen the row and push it past the zone's right edge.
	hit, ok := jobHitBox(m, "job-1")
	if !ok {
		t.Fatal("no hit box recorded for the job")
	}
	glyphCol := ansi.StringWidth(plain) - 1
	if glyphCol < hit.stopZoneStartX || glyphCol >= hit.stopZoneEndX {
		t.Fatalf("stop glyph at panel column %d is outside its hit zone [%d,%d): %q",
			glyphCol, hit.stopZoneStartX, hit.stopZoneEndX, plain)
	}
}

func TestOpenStopJobConfirmRejectsFinishedJob(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	id := startTestJob(t, "true", "already finished")
	waitForJobStatus(t, id, "completed")
	refreshJobs(m)

	cmd := m.openStopJobConfirm(id)
	if cmd == nil {
		t.Fatal("a finished job should produce an info toast")
	}
	if m.mode == ModeStopJobConfirm {
		t.Fatal("a finished job must not open the stop confirmation")
	}
}

func TestInfoPanelJobsElapsedAdvancesPerSecond(t *testing.T) {
	m := newJobsTestModel(t, 160, 60)
	started := time.Now().Add(-90 * time.Second)
	m.jobsSnapshot = []tools.JobState{{ID: "job-1", Description: "ticking", Status: jobStatusRunning, StartedAt: started}}
	m.activeJobsSnapshot = []tools.JobState{m.jobsSnapshot[0]}
	m.jobsSnapshotValid = true
	m.jobsSnapshotFrame = m.renderFrameGeneration

	t0 := time.Now()
	first := m.infoPanelFingerprint(32, 0)
	firstRendered := stripANSI(m.renderInfoPanel(32, 60))

	// Inside one second bucket the key is stable, so the panel is reused instead
	// of being rebuilt on every frame.
	if again := m.infoPanelFingerprint(32, 0); again != first && time.Now().Unix() == t0.Unix() {
		t.Fatal("JOBS fingerprint changed inside one second bucket")
	}

	// Bounded wait (<1.2s) for the next second bucket; the displayed elapsed
	// must then advance.
	deadline := t0.Add(1200 * time.Millisecond)
	for time.Since(t0) < 1100*time.Millisecond && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if elapsed := time.Since(t0); elapsed < 1050*time.Millisecond {
		t.Fatalf("setup: bounded wait only reached %v", elapsed)
	}
	second := m.infoPanelFingerprint(32, 0)
	if second == first {
		t.Fatal("JOBS fingerprint should advance across second buckets")
	}
	secondRendered := stripANSI(m.renderInfoPanel(32, 60))
	if secondRendered == firstRendered {
		t.Fatalf("panel should re-render the elapsed value once the second advances:\n%s", firstRendered)
	}
}

func TestRenderFreezeYieldsToRunningJob(t *testing.T) {
	m := newJobsTestModel(t, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.terminalTitleBase = "background job"
	m.backgroundIdleSince = time.Now().Add(-11 * time.Second)
	m.cachedFullView = tea.View{Content: "frozen"}
	m.cachedFullViewValid = true

	id := startTestJob(t, "sleep 60", "freeze guard")
	refreshJobs(m)
	m.syncTerminalTitleState()
	if !m.terminalTitleTickRunning {
		t.Fatal("a running background job should keep the terminal title ticker running")
	}

	if m.tryEnterRenderFreeze("test") {
		t.Fatal("a running background job must not enter render freeze")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("a refused freeze must not stop the terminal title ticker")
	}

	// Once the job ends the freeze guard must release: it only blocks while a
	// job is alive, it does not disable render freeze.
	tools.StopJobByUser(id, "test")
	waitForJobStatus(t, id, "killed")
	refreshJobs(m)
	if !m.tryEnterRenderFreeze("test") {
		t.Fatal("render freeze should be allowed once the job has ended")
	}
}

// The jobs overlay is the only mouse-free way to reach a background job: a
// terminal without mouse reporting (plain SSH, tmux with mouse off) can neither
// click the status-bar pill nor the info panel's stop affordance.
func TestJobsOverlayOpensFromNormalModeKey(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	id := startTestJob(t, "sleep 60", "keyboard reachable")
	refreshJobs(m)
	m.mode = ModeNormal

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'j', Mod: tea.ModCtrl}))
	if cmd != nil {
		t.Fatal("ctrl+j with a running job should open the overlay, not toast")
	}
	if m.mode != ModeJobsOverlay {
		t.Fatalf("ctrl+j mode = %v, want ModeJobsOverlay", m.mode)
	}
	// The first row is selected, so Enter acts on a job without any mouse.
	if got, ok := m.jobsOverlayCursorJobID(); !ok || got != id {
		t.Fatalf("cursor job = %q ok=%t, want %q", got, ok, id)
	}
}

func TestJobsOverlayEnterOpensStopConfirmForCursor(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	first := startTestJob(t, "sleep 60", "first job")
	second := startTestJob(t, "sleep 61", "second job")
	refreshJobs(m)
	m.openJobsOverlay()
	if m.mode != ModeJobsOverlay {
		t.Fatalf("mode = %v, want ModeJobsOverlay", m.mode)
	}

	// j moves the selection, and Enter stops the row it landed on — not the
	// first row, and not whatever the mouse last pointed at.
	m.handleJobsOverlayKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'}))
	if got, _ := m.jobsOverlayCursorJobID(); got != second && got != first {
		t.Fatalf("cursor job = %q, want one of the running jobs", got)
	}
	want, _ := m.jobsOverlayCursorJobID()
	m.handleJobsOverlayKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != ModeStopJobConfirm {
		t.Fatalf("enter mode = %v, want ModeStopJobConfirm", m.mode)
	}
	if m.stopJobConfirm.jobID != want {
		t.Fatalf("confirm job = %q, want the selected %q", m.stopJobConfirm.jobID, want)
	}
	// Stopping is still a separate confirmation: Enter never kills a job.
	for _, state := range tools.SnapshotJobs() {
		if state.ID == want && state.Status != jobStatusRunning {
			t.Fatalf("enter must not stop the job directly, status = %q", state.Status)
		}
	}
}

func TestJobsOverlayCursorClampsWhenJobsFinish(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	first := startTestJob(t, "sleep 60", "first job")
	second := startTestJob(t, "sleep 61", "second job")
	refreshJobs(m)
	m.openJobsOverlay()
	m.handleJobsOverlayKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'}))
	if got, _ := m.jobsOverlayCursorJobID(); got != second && got != first {
		t.Fatalf("cursor job = %q, want a running job", got)
	}

	// Jobs finish on their own; the cursor must not keep pointing past the
	// end of the list, which would leave Enter acting on nothing.
	tools.StopJobByUser(second, "test")
	waitForJobStatus(t, second, "killed")
	tools.StopJobByUser(first, "test")
	waitForJobStatus(t, first, "killed")
	refreshJobs(m)
	m.clampJobsOverlayScroll()
	if got, ok := m.jobsOverlayCursorJobID(); ok {
		t.Fatalf("cursor job = %q, want no selection once every job ended", got)
	}
}

func TestJobsOverlayCursorFollowsWheel(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	ids := []string{
		startTestJob(t, "sleep 60", "job one"),
		startTestJob(t, "sleep 61", "job two"),
		startTestJob(t, "sleep 62", "job three"),
	}
	refreshJobs(m)
	m.openJobsOverlay()
	// The wheel moves the selection instead of only the window, so the row
	// Enter acts on is always the row on screen.
	m.moveJobsOverlayCursor(mouseWheelScrollStep)
	got, ok := m.jobsOverlayCursorJobID()
	if !ok {
		t.Fatal("a running job should stay selected after wheeling")
	}
	if !slices.Contains(ids, got) {
		t.Fatalf("cursor job = %q, want one of %v", got, ids)
	}
}
