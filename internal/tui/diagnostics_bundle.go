package tui

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/config"
)

const diagnosticLogTailLines = 240
const diagnosticLogTailBytes = 256 << 10

type diagnosticsBundleMsg struct {
	path string
	err  error
}

type diagnosticsBundleData struct {
	bundlePath string
	metadata   string
	tuiDump    string
	logName    string
	logTail    string
}

var (
	diagBearerRE     = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+\-/=]+`)
	diagAuthHeaderRE = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)([^\s,;]+)`)
	diagAPIKeyRE     = regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|passwd|refresh[_-]?token|access[_-]?token)\s*[:=]\s*)([^\s,;]+)`)
	diagOpenAIKeyRE  = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]+\b`)
	diagAnthropicRE  = regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]+\b`)
	diagSessionIDRE  = regexp.MustCompile(`\b\d{3,}\b`)
)

func parseDiagnosticsBundleCommand(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	if lower == "/diagnostics" || strings.HasPrefix(lower, "/diagnostics ") {
		return trimmed, true
	}
	return "", false
}

func writeDiagnosticsBundleCmd(data diagnosticsBundleData) tea.Cmd {
	return func() tea.Msg {
		if err := os.MkdirAll(filepath.Dir(data.bundlePath), 0o700); err != nil {
			return diagnosticsBundleMsg{err: fmt.Errorf("create diagnostics dir: %w", err)}
		}
		f, err := os.OpenFile(data.bundlePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return diagnosticsBundleMsg{err: fmt.Errorf("create diagnostics bundle: %w", err)}
		}
		defer f.Close()

		zw := zip.NewWriter(f)
		if err := writeDiagnosticsZipFile(zw, "metadata.txt", data.metadata); err != nil {
			_ = zw.Close()
			return diagnosticsBundleMsg{err: err}
		}
		if err := writeDiagnosticsZipFile(zw, "tui-dump.txt", data.tuiDump); err != nil {
			_ = zw.Close()
			return diagnosticsBundleMsg{err: err}
		}
		if strings.TrimSpace(data.logTail) != "" {
			name := data.logName
			if strings.TrimSpace(name) == "" {
				name = "runtime-log-tail.txt"
			}
			if err := writeDiagnosticsZipFile(zw, name, data.logTail); err != nil {
				_ = zw.Close()
				return diagnosticsBundleMsg{err: err}
			}
		}
		if err := zw.Close(); err != nil {
			return diagnosticsBundleMsg{err: fmt.Errorf("finalize diagnostics bundle: %w", err)}
		}
		return diagnosticsBundleMsg{path: data.bundlePath}
	}
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
	now := time.Now()
	data, err := m.buildDiagnosticsBundle(now, trigger)
	if err != nil {
		return func() tea.Msg { return diagnosticsBundleMsg{err: err} }
	}
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
	projectLocator, err := locator.EnsureProject(baseDir)
	if err != nil {
		return diagnosticsBundleData{}, fmt.Errorf("resolve project export dir: %w", err)
	}
	bundlePath := filepath.Join(projectLocator.ProjectExportsDir,
		fmt.Sprintf("diagnostics-%s-%d.zip", now.Format("20060102-150405.000"), os.Getpid()))

	m.recordTUIDiagnostic("diagnostics-bundle", "%s", trigger)
	tuiDump, err := m.buildDiagnosticDumpContent(now, trigger, bundlePath, true)
	if err != nil {
		return diagnosticsBundleData{}, err
	}
	logName, logTail := collectDiagnosticLogTail(locator.LogsDir, baseDir, os.Getpid())
	meta := m.buildDiagnosticsMetadata(now, trigger, baseDir, bundlePath, logName)
	return diagnosticsBundleData{
		bundlePath: bundlePath,
		metadata:   meta,
		tuiDump:    tuiDump,
		logName:    logName,
		logTail:    logTail,
	}, nil
}

func (m *Model) buildDiagnosticsMetadata(now time.Time, trigger, baseDir, bundlePath, logName string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Chord diagnostics bundle\n")
	fmt.Fprintf(&sb, "generated_at: %s\n", now.Format(time.RFC3339Nano))
	fmt.Fprintf(&sb, "trigger: %s\n", strings.TrimSpace(trigger))
	fmt.Fprintf(&sb, "bundle_path: %s\n", sanitizeDiagnosticText(bundlePath, baseDir))
	fmt.Fprintf(&sb, "working_dir: %s\n", sanitizeDiagnosticText(baseDir, baseDir))
	fmt.Fprintf(&sb, "goos: %s\n", runtime.GOOS)
	fmt.Fprintf(&sb, "goarch: %s\n", runtime.GOARCH)
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
	if strings.TrimSpace(logName) != "" {
		fmt.Fprintf(&sb, "runtime_log_tail: %s\n", sanitizeDiagnosticText(logName, baseDir))
	} else {
		fmt.Fprintf(&sb, "runtime_log_tail: (not found)\n")
	}
	return sb.String()
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

func collectDiagnosticLogTail(logsDir, baseDir string, pid int) (string, string) {
	if strings.TrimSpace(logsDir) == "" || pid <= 0 {
		return "", ""
	}
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return "", ""
	}
	type candidate struct {
		path    string
		name    string
		modTime time.Time
	}
	cands := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !isRuntimeLogFile(name) {
			continue
		}
		path := filepath.Join(logsDir, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		cands = append(cands, candidate{path: path, name: name, modTime: info.ModTime()})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].modTime.After(cands[j].modTime) })
	for _, cand := range cands {
		tail, ok := readSanitizedLogTail(cand.path, baseDir, pid)
		if !ok || strings.TrimSpace(tail) == "" {
			continue
		}
		return cand.name, tail
	}
	return "", ""
}

func isRuntimeLogFile(name string) bool {
	if name == "chord.log" {
		return true
	}
	if !strings.HasPrefix(name, "chord.log.") {
		return false
	}
	suffix := strings.TrimPrefix(name, "chord.log.")
	if suffix == "" {
		return false
	}
	_, err := strconv.Atoi(suffix)
	return err == nil
}

func readSanitizedLogTail(path, baseDir string, pid int) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false
	}
	start := int64(0)
	if info.Size() > diagnosticLogTailBytes {
		start = info.Size() - diagnosticLogTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", false
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, f); err != nil {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(buf.String(), "\r\n", "\n"), "\n")
	if len(lines) > diagnosticLogTailLines {
		lines = lines[len(lines)-diagnosticLogTailLines:]
	}
	pidMarker := ""
	if pid > 0 {
		pidMarker = "pid=" + strconv.Itoa(pid)
	}
	filtered := make([]string, 0, len(lines))
	captureStderr := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if captureStderr && len(filtered) > 0 {
				filtered = append(filtered, "")
			}
			continue
		}
		if pidMarker != "" && containsLogField(line, pidMarker) {
			filtered = append(filtered, sanitizeDiagnosticText(line, baseDir))
			captureStderr = true
			continue
		}
		if captureStderr && !containsLogFieldPrefix(line, "pid=") {
			filtered = append(filtered, sanitizeDiagnosticText(line, baseDir))
			continue
		}
		captureStderr = false
	}
	for len(filtered) > 0 && strings.TrimSpace(filtered[0]) == "" {
		filtered = filtered[1:]
	}
	for len(filtered) > 0 && strings.TrimSpace(filtered[len(filtered)-1]) == "" {
		filtered = filtered[:len(filtered)-1]
	}
	if len(filtered) == 0 {
		return "", false
	}
	return strings.Join(filtered, "\n") + "\n", true
}

func containsLogField(line, field string) bool {
	for start := 0; ; {
		idx := strings.Index(line[start:], field)
		if idx < 0 {
			return false
		}
		pos := start + idx
		if isLogFieldStart(line, pos) && isLogFieldEnd(line, pos+len(field)) {
			return true
		}
		start = pos + len(field)
		if start >= len(line) {
			return false
		}
	}
}

func containsLogFieldPrefix(line, prefix string) bool {
	for start := 0; ; {
		idx := strings.Index(line[start:], prefix)
		if idx < 0 {
			return false
		}
		pos := start + idx
		if isLogFieldStart(line, pos) {
			return true
		}
		start = pos + len(prefix)
		if start >= len(line) {
			return false
		}
	}
}

func isLogFieldStart(line string, pos int) bool {
	return pos == 0 || line[pos-1] == ' ' || line[pos-1] == '['
}

func isLogFieldEnd(line string, pos int) bool {
	return pos >= len(line) || line[pos] == ' ' || line[pos] == ']'
}

func sanitizeDiagnosticText(s, baseDir string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
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
