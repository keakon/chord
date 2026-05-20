package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
)

const ruffDiagnosticsTimeout = 5 * time.Second

type pythonDiagnosticBackend string

const (
	pythonDiagnosticBackendNone     pythonDiagnosticBackend = "none"
	pythonDiagnosticBackendSemantic pythonDiagnosticBackend = "semantic"
	pythonDiagnosticBackendQuick    pythonDiagnosticBackend = "quick"
)

type pythonDiagnosticSelection struct {
	Backend             pythonDiagnosticBackend
	Reason              string
	SkipFullTypeCheck   bool
	ComparisonAvailable bool
}

type diagnosticBackendAvailability struct {
	Semantic bool
	Quick    bool
}

type fileMetrics struct {
	Lines int
	Bytes int
}

var (
	lookPath          = exec.LookPath
	runCommandContext = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
)

func isPythonPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".py" || ext == ".pyi"
}

func measureContent(content string) fileMetrics {
	lines := 1
	if content != "" {
		lines = strings.Count(content, "\n") + 1
	}
	return fileMetrics{Lines: lines, Bytes: len(content)}
}

func pythonDiagnosticsLargeFile(cfg config.PythonDiagnosticsConfig, metrics fileMetrics) bool {
	lineThreshold := cfg.LargeFile.LineThreshold
	if lineThreshold <= 0 {
		lineThreshold = 5000
	}
	byteThreshold := cfg.LargeFile.ByteThreshold
	if byteThreshold <= 0 {
		byteThreshold = 250000
	}
	return metrics.Lines > lineThreshold || metrics.Bytes > byteThreshold
}

func selectPythonDiagnosticBackend(cfg config.PythonDiagnosticsConfig, metrics fileMetrics, availability diagnosticBackendAvailability) pythonDiagnosticSelection {
	large := pythonDiagnosticsLargeFile(cfg, metrics)
	if availability.Semantic && availability.Quick {
		if large {
			return pythonDiagnosticSelection{Backend: pythonDiagnosticBackendQuick, Reason: "large-file", SkipFullTypeCheck: true}
		}
		return pythonDiagnosticSelection{Backend: pythonDiagnosticBackendSemantic, Reason: "semantic"}
	}
	if availability.Semantic {
		if large && !cfg.LargeFile.RunSemanticWhenQuickUnavailable {
			return pythonDiagnosticSelection{Backend: pythonDiagnosticBackendNone, Reason: "large-file-quick-unavailable", SkipFullTypeCheck: true}
		}
		if large {
			return pythonDiagnosticSelection{Backend: pythonDiagnosticBackendSemantic, Reason: "large-file-forced-semantic"}
		}
		return pythonDiagnosticSelection{Backend: pythonDiagnosticBackendSemantic, Reason: "semantic"}
	}
	if availability.Quick {
		return pythonDiagnosticSelection{Backend: pythonDiagnosticBackendQuick, Reason: "semantic-unavailable", SkipFullTypeCheck: true}
	}
	return pythonDiagnosticSelection{Backend: pythonDiagnosticBackendNone, Reason: "no-backend", SkipFullTypeCheck: true}
}

func (m *Manager) pythonSemanticBackendAvailable(path string, pyCfg config.PythonDiagnosticsConfig) bool {
	if m == nil || m.cfg == nil || len(m.cfg.LSP) == 0 {
		return false
	}
	backend := pyCfg.SemanticBackend
	if strings.TrimSpace(backend.Type) != "" && backend.Type != config.DiagnosticBackendTypeLSP {
		return false
	}
	serverName := strings.TrimSpace(backend.Server)
	if serverName != "" {
		srvCfg, ok := m.cfg.LSP[serverName]
		return ok && !srvCfg.Disabled && m.handles(srvCfg, path)
	}
	return m.anyServerMatchesPath(path)
}

func (m *Manager) pythonQuickBackendAvailable(pyCfg config.PythonDiagnosticsConfig) bool {
	backend := pyCfg.QuickBackend
	if strings.TrimSpace(backend.Type) != "" && backend.Type != config.DiagnosticBackendTypeCommand {
		return false
	}
	cmd := strings.TrimSpace(backend.Command)
	if cmd == "" {
		cmd = "ruff"
	}
	if m == nil {
		_, err := lookPath(cmd)
		return err == nil
	}
	m.quickBackendMu.Lock()
	defer m.quickBackendMu.Unlock()
	if m.quickBackendCache == nil {
		m.quickBackendCache = make(map[string]bool)
	}
	if available, ok := m.quickBackendCache[cmd]; ok {
		return available
	}
	_, err := lookPath(cmd)
	available := err == nil
	m.quickBackendCache[cmd] = available
	return available
}

func runRuffDiagnostics(ctx context.Context, path string, pyCfg config.PythonDiagnosticsConfig) ([]Diagnostic, error) {
	backend := pyCfg.QuickBackend
	cmd := strings.TrimSpace(backend.Command)
	if cmd == "" {
		cmd = "ruff"
	}
	args := backend.Args
	if len(args) == 0 {
		args = []string{"check", "{file}", "--select", "E9,F821,F822,F823,B,PLE", "--output-format", "json"}
	}
	args = replaceFileArg(args, path)

	runCtx, cancel := context.WithTimeout(ctx, ruffDiagnosticsTimeout)
	defer cancel()
	out, err := runCommandContext(runCtx, cmd, args...)
	if runCtx.Err() != nil {
		return nil, runCtx.Err()
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return nil, err
	}
	return parseRuffDiagnostics(out)
}

func replaceFileArg(args []string, path string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = strings.ReplaceAll(arg, "{file}", path)
	}
	return out
}

type ruffDiagnostic struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Filename string `json:"filename"`
	Location struct {
		Row    int `json:"row"`
		Column int `json:"column"`
	} `json:"location"`
}

func ruffDiagnosticSeverity(code string) int {
	if strings.HasPrefix(code, "B") {
		return 2
	}
	return 1
}

func parseRuffDiagnostics(out []byte) ([]Diagnostic, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	var raw []ruffDiagnostic
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, fmt.Errorf("parse Ruff JSON diagnostics: %w", err)
	}
	diags := make([]Diagnostic, 0, len(raw))
	for _, d := range raw {
		line := max(d.Location.Row-1, 0)
		col := max(d.Location.Column-1, 0)
		diags = append(diags, Diagnostic{Severity: ruffDiagnosticSeverity(d.Code), Line: line, Col: col, Code: d.Code, Message: d.Message, Source: "ruff"})
	}
	return diags, nil
}
