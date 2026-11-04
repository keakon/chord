package tui

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/buildinfo"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/textclean"
)

const diagnosticLogTailLines = 240
const diagnosticLogTailBytes = 256 << 10
const diagnosticLLMTraceTailLines = 200
const diagnosticLLMTraceTailBytes = 256 << 10

type diagnosticsBundleMsg struct {
	path string
	err  error
}

type diagnosticsBundleData struct {
	contextJSON []byte
	baseDir     string
	sessionID   string
	filename    string
	locator     config.PathLocator
	metadata    string
	tuiDump     string
}

var (
	diagBearerRE     = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+\-/=]+`)
	diagAuthHeaderRE = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)([^\s,;]+)`)
	diagAPIKeyRE     = regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|passwd|refresh[_-]?token|access[_-]?token)\s*[:=]\s*)([^\s,;]+)`)
	diagOpenAIKeyRE  = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]+\b`)
	diagAnthropicRE  = regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]+\b`)
	diagSessionIDRE  = regexp.MustCompile(`\b\d{3,}\b`)
)

func writeDiagnosticsBundleCmd(data diagnosticsBundleData) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		path, err := writeDiagnosticsBundle(ctx, data)
		return diagnosticsBundleMsg{path: path, err: err}
	}
}

func writeDiagnosticsBundle(ctx context.Context, data diagnosticsBundleData) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("collect diagnostics: %w", err)
	}
	project, err := data.locator.LocateProject(data.baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve project export dir: %w", err)
	}
	heap, memory, err := collectDiagnosticHeap(data.baseDir)
	if err != nil {
		return "", err
	}
	logs := collectDiagnosticLogTail(ctx, data.locator.LogsDir, data.baseDir, os.Getpid())
	traceName, trace := collectDiagnosticLLMTrace(project, data.sessionID)
	meta := data.metadata + memory + logs.metadata(data.baseDir)
	if traceName != "" {
		meta += fmt.Sprintf("llm_trace_tail: %s\nllm_trace_collected_at: %s\n", traceName, time.Now().Format(time.RFC3339Nano))
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("collect diagnostics: %w", err)
	}
	if err := os.MkdirAll(project.ProjectExportsDir, 0o700); err != nil {
		return "", fmt.Errorf("create diagnostics dir: %w", err)
	}
	f, err := os.CreateTemp(project.ProjectExportsDir, ".diagnostics-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create diagnostics bundle: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	for _, entry := range []struct{ name, content string }{
		{"context.json", string(data.contextJSON)},
		{"metadata.txt", meta},
		{"tui-dump.txt", data.tuiDump},
		{"runtime-log-tail.txt", logs.tail},
		{traceName, trace},
	} {
		if entry.content == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("write diagnostics bundle: %w", err)
		}
		if err := writeDiagnosticsZipFile(zw, entry.name, entry.content); err != nil {
			return "", err
		}
	}
	w, err := zw.CreateHeader(&zip.FileHeader{Name: diagnosticHeapFilename, Method: zip.Store})
	if err != nil {
		return "", fmt.Errorf("create heap profile entry: %w", err)
	}
	if _, err := w.Write(heap); err != nil {
		return "", fmt.Errorf("write heap profile: %w", err)
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("finalize diagnostics bundle: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close diagnostics bundle: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("publish diagnostics bundle: %w", err)
	}
	path := filepath.Join(project.ProjectExportsDir, data.filename)
	if err := os.Rename(f.Name(), path); err != nil {
		return "", fmt.Errorf("publish diagnostics bundle: %w", err)
	}
	return path, nil
}

func writeDiagnosticsZipFile(zw *zip.Writer, name, content string) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	if _, err := io.Copy(w, strings.NewReader(content)); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func (m *Model) exportDiagnosticsBundleNow(trigger string) tea.Cmd {
	if m.diagnosticsPending {
		return m.enqueueToast("Diagnostics export is already running", "info")
	}
	now := time.Now()
	data, err := m.buildDiagnosticsBundle(now, trigger)
	if err != nil {
		return func() tea.Msg { return diagnosticsBundleMsg{err: err} }
	}
	m.diagnosticsPending = true
	return writeDiagnosticsBundleCmd(data)
}

func (m *Model) buildDiagnosticsBundle(now time.Time, trigger string) (diagnosticsBundleData, error) {
	baseDir := strings.TrimSpace(m.workingDir)
	if baseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return diagnosticsBundleData{}, fmt.Errorf("resolve working dir: %w", err)
		}
		baseDir = cwd
	}
	locator, err := config.DefaultPathLocator()
	if err != nil {
		return diagnosticsBundleData{}, fmt.Errorf("resolve storage paths: %w", err)
	}
	filename := fmt.Sprintf("diagnostics-%s-%d.zip", now.Format("20060102-150405.000000000"), os.Getpid())

	m.recordTUIDiagnostic("diagnostics-bundle", "%s", trigger)
	tuiDump, err := m.buildDiagnosticDumpContent(now, trigger, filename, true)
	if err != nil {
		return diagnosticsBundleData{}, err
	}
	var sessionID string
	if m.agent != nil {
		if summary := m.agent.GetSessionSummary(); summary != nil {
			sessionID = summary.ID
		}
	}
	meta := m.buildDiagnosticsMetadata(now, trigger, baseDir, filename)
	return diagnosticsBundleData{
		contextJSON: m.exportFocusedContextDiagnostics(),
		baseDir:     baseDir,
		sessionID:   sessionID,
		filename:    filename,
		locator:     *locator,
		metadata:    meta,
		tuiDump:     tuiDump,
	}, nil
}

func (m *Model) buildDiagnosticsMetadata(now time.Time, trigger, baseDir, bundlePath string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Chord diagnostics bundle\n")
	fmt.Fprintf(&sb, "generated_at: %s\n", now.Format(time.RFC3339Nano))
	writeBuildInfoDiagnostics(&sb, buildinfo.Current(), baseDir)
	fmt.Fprintf(&sb, "trigger: %s\n", strings.TrimSpace(trigger))
	fmt.Fprintf(&sb, "bundle_path: %s\n", sanitizeDiagnosticText(bundlePath, baseDir))
	fmt.Fprintf(&sb, "working_dir: %s\n", sanitizeDiagnosticText(baseDir, baseDir))
	fmt.Fprintf(&sb, "term: %s\n", sanitizeDiagnosticText(os.Getenv("TERM"), baseDir))
	fmt.Fprintf(&sb, "term_program: %s\n", sanitizeDiagnosticText(os.Getenv("TERM_PROGRAM"), baseDir))
	fmt.Fprintf(&sb, "term_program_version: %s\n", sanitizeDiagnosticText(os.Getenv("TERM_PROGRAM_VERSION"), baseDir))
	fmt.Fprintf(&sb, "inside_tmux: %t\n", strings.TrimSpace(os.Getenv("TMUX")) != "")
	fmt.Fprintf(&sb, "inside_cmux: %t\n", strings.TrimSpace(os.Getenv("CMUX_SOCKET")) != "" || strings.TrimSpace(os.Getenv("CMUX_SOCKET_PATH")) != "")
	if summary := m.sessionSummaryString(); summary != "" {
		fmt.Fprintf(&sb, "session: %s\n", summary)
	}
	if strings.TrimSpace(m.instanceID) != "" {
		fmt.Fprintf(&sb, "process_instance_id: %s\n", sanitizeDiagnosticText(m.instanceID, baseDir))
	}
	if provider, ok := m.agent.(interface {
		OrchestrationStats() agent.OrchestrationStats
		OrchestrationTaskDiagnostics() []agent.TaskDiagnosticSnapshot
	}); ok {
		writeOrchestrationDiagnostics(&sb, provider.OrchestrationStats(), provider.OrchestrationTaskDiagnostics())
	}
	return sb.String()
}

func writeOrchestrationDiagnostics(sb *strings.Builder, stats agent.OrchestrationStats, tasks []agent.TaskDiagnosticSnapshot) {
	fmt.Fprintf(sb, "orchestration_event_queue: overflow=%d overflow_peak=%d coalesced=%d backpressure=%d\n",
		stats.EventQueue.OverflowCurrent, stats.EventQueue.OverflowPeak,
		stats.EventQueue.Coalesced, stats.EventQueue.Backpressure)
	fmt.Fprintf(sb, "orchestration_runtimes: normal=%d/%d borrowed=%d/%d bypass_active=%d bypass_peak=%d bypass_grants=%d bypass_rejected=%d\n",
		stats.SemaphoreInUse, stats.SemaphoreCapacity, stats.BorrowedInUse, stats.BorrowedCapacity,
		stats.RuntimeBypassActive, stats.RuntimeBypassPeak, stats.RuntimeBypassGrants, stats.RuntimeBypassRejected)
	fmt.Fprintf(sb, "orchestration_llm_requests: active=%d capacity=%d queued=%d\n",
		stats.LLMRequestsActive, stats.LLMRequestCapacity, stats.LLMRequestsQueued)
	fmt.Fprintf(sb, "orchestration_workspace_leases: active=%d queued=%d\n",
		stats.WorkspaceLeasesActive, stats.WorkspaceLeasesQueued)
	fmt.Fprintf(sb, "orchestration_tasks: total=%d rehydrates=%d parks=%d queue_rejected=%d\n",
		stats.TasksTotal, stats.Rehydrates, stats.Parks, stats.SubAgentQueueRejected)
	fmt.Fprintf(sb, "orchestration_state_transitions: rejected=%d terminal=%d unknown=%d other=%d\n",
		stats.StateTransitionsRejected,
		stats.StateTransitionRejections["terminal"],
		stats.StateTransitionRejections["unknown"],
		stats.StateTransitionRejections["other"])
	for _, task := range tasks {
		fmt.Fprintf(sb, "orchestration_task: id=%s owner=%s state=%s instance=%s attempt=%d revision=%d durable=%t closed=%s mailbox_backlog=%d\n",
			task.TaskID, task.OwnerTaskID, task.State, task.LatestInstanceID,
			task.Attempt, task.LifecycleRevision, task.SettlementDurable, task.ClosedReason, task.MailboxBacklog)
	}
	fmt.Fprintf(sb, "orchestration_mailbox: deliveries=%d delivery_avg=%s acks=%d ack_avg=%s spool_queued=%d spool_rehydrated=%d tracking_evictions=%d\n",
		stats.MailboxDeliveries, stats.MailboxDeliveryLatencyAverage, stats.MailboxAcks, stats.MailboxAckLatencyAverage,
		stats.MailboxSpoolQueued, stats.MailboxSpoolRehydrated, stats.MailboxTrackingEvictions)
}

func writeBuildInfoDiagnostics(sb *strings.Builder, info buildinfo.Info, baseDir string) {
	for _, field := range info.Fields() {
		fmt.Fprintf(sb, "%s: %s\n", field.Key, sanitizeDiagnosticText(field.Value, baseDir))
	}
}

func (m *Model) sessionSummaryString() string {
	if m == nil || m.agent == nil {
		return ""
	}
	summary := m.agent.GetSessionSummary()
	if summary == nil {
		return ""
	}
	parts := []string{fmt.Sprintf("id=%s", sanitizeDiagnosticSessionID(summary.ID))}
	if strings.TrimSpace(summary.ForkedFrom) != "" {
		parts = append(parts, fmt.Sprintf("forked_from=%s", sanitizeDiagnosticSessionID(summary.ForkedFrom)))
	}
	parts = append(parts, fmt.Sprintf("locked=%t", summary.Locked))
	return strings.Join(parts, " ")
}

func collectDiagnosticLLMTrace(projectLocator *config.ProjectLocator, sessionID string) (string, string) {
	if projectLocator == nil || strings.TrimSpace(sessionID) == "" {
		return "", ""
	}
	path := filepath.Join(projectLocator.ProjectSessionsDir, sessionID, "traces", llm.LLMTraceFileName())
	tail, ok := readTextTail(path, diagnosticLLMTraceTailBytes, diagnosticLLMTraceTailLines)
	if !ok || strings.TrimSpace(tail) == "" {
		return "", ""
	}
	return llm.LLMTraceFileName(), tail
}

func readTextTail(path string, maxBytes int, maxLines int) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return "", false
	}
	start := int64(0)
	if maxBytes > 0 && info.Size() > int64(maxBytes) {
		// Include the preceding byte to distinguish a line boundary from a
		// partial first line without reading the rest of the trace.
		start = info.Size() - int64(maxBytes) - 1
	}
	data := make([]byte, info.Size()-start)
	n, err := f.ReadAt(data, start)
	if err != nil && err != io.EOF {
		return "", false
	}
	data = data[:n]
	if len(data) == 0 {
		return "", false
	}
	if maxBytes > 0 && info.Size() > int64(maxBytes) {
		atLineStart := data[0] == '\n'
		data = data[1:]
		if !atLineStart {
			if idx := bytes.IndexByte(data, '\n'); idx >= 0 && idx+1 < len(data) {
				data = data[idx+1:]
			}
		}
	}
	if maxLines > 0 {
		end := len(data)
		if end > 0 && data[end-1] == '\n' {
			end--
		}
		for range maxLines {
			idx := bytes.LastIndexByte(data[:end], '\n')
			if idx < 0 {
				end = -1
				break
			}
			end = idx
		}
		if end >= 0 {
			data = data[end+1:]
		}
	}
	return string(data), true
}

func sanitizeDiagnosticText(s, baseDir string) string {
	s = textclean.NormalizeNewlines(s)
	if strings.TrimSpace(baseDir) != "" {
		s = strings.ReplaceAll(s, filepath.Clean(baseDir), "<project-root>")
		s = strings.ReplaceAll(s, filepath.ToSlash(filepath.Clean(baseDir)), "<project-root>")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		home = filepath.Clean(home)
		s = strings.ReplaceAll(s, home, "<home>")
		s = strings.ReplaceAll(s, filepath.ToSlash(home), "<home>")
	}
	s = diagBearerRE.ReplaceAllString(s, "$1 <redacted>")
	s = diagAuthHeaderRE.ReplaceAllString(s, "$1<redacted>")
	s = diagAPIKeyRE.ReplaceAllString(s, "$1<redacted>")
	s = diagOpenAIKeyRE.ReplaceAllString(s, "<redacted-openai-key>")
	s = diagAnthropicRE.ReplaceAllString(s, "<redacted-anthropic-key>")
	return s
}

func sanitizeDiagnosticSessionID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return diagSessionIDRE.ReplaceAllStringFunc(s, func(match string) string {
		if len(match) <= 4 {
			return match
		}
		return strings.Repeat("x", len(match)-4) + match[len(match)-4:]
	})
}
