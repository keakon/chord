package sessionimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

type ImportOptions struct {
	Source        string
	InputPath     string
	SourceID      string
	SourceRoot    string
	ProjectRoot   string
	SessionID     string
	ReasoningMode string
	DryRun        bool
	JSONOutput    bool
	Force         bool
}

type ImportResult struct {
	ProjectRoot string
	SessionID   string
	SessionDir  string
	Messages    int
	Report      ImportReport
}

// Import converts a supported external conversation export into a Chord session.
// Supported sources: OpenCode export JSON, Codex rollout JSONL, and Claude Code
// transcript JSONL. Tool import defaults remain conservative by source.
func Import(ctx context.Context, opts ImportOptions) (*ImportResult, error) {
	_ = ctx

	source := strings.ToLower(strings.TrimSpace(opts.Source))
	if source == "" {
		return nil, fmt.Errorf("import: source is empty")
	}
	lookup, err := resolveImportInputPath(source, opts.InputPath, opts.SourceID, opts.SourceRoot)
	if err != nil {
		return nil, err
	}
	input := strings.TrimSpace(lookup.Path)
	if input == "" {
		return nil, fmt.Errorf("import: input path is empty")
	}
	projectRoot := strings.TrimSpace(opts.ProjectRoot)
	if projectRoot == "" {
		projectRoot = "."
	}

	reasoningMode, err := normalizeReasoningMode(opts.ReasoningMode)
	if err != nil {
		return nil, err
	}

	locator, err := config.DefaultPathLocator()
	if err != nil {
		return nil, err
	}
	pl, err := locator.EnsureProject(projectRoot)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(input)
	if err != nil {
		return nil, fmt.Errorf("read input file: %w", err)
	}

	report := ImportReport{
		Source:           source,
		SourcePath:       input,
		ReasoningMode:    reasoningMode,
		ImportedAt:       time.Now().UTC(),
		ImportedMessages: 0,
	}

	var msgs []message.Message
	switch source {
	case "claude":
		msgs, err = convertClaudeTranscript(data, reasoningMode, &report)
		if err != nil {
			return nil, err
		}
	case "opencode":
		msgs, err = convertOpenCodeExport(data, reasoningMode, &report)
		if err != nil {
			return nil, err
		}
	case "codex":
		msgs, err = convertCodexRollout(data, reasoningMode, &report)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("import: unsupported source %q (supported in this build: claude, opencode, codex)", source)
	}

	report.ImportedMessages = len(msgs)
	if len(msgs) == 0 {
		return nil, fmt.Errorf("import: no messages were imported")
	}

	res := &ImportResult{ProjectRoot: pl.CanonicalRoot, Messages: len(msgs), Report: report}
	if opts.DryRun {
		// No on-disk artifacts; still return the project root and report.
		return res, nil
	}

	resolvedInput := input
	if opts.SourceID != "" {
		resolvedInput = lookup.Path
	}

	sid, sessionDir, err := writeChordSession(pl.ProjectSessionsDir, opts.SessionID, opts.Force, msgs, report, recovery.SessionMeta{ImportedFrom: &recovery.ImportMeta{
		Source:          source,
		SourcePath:      resolvedInput,
		SourceSessionID: report.SourceSessionID,
		ImportedAt:      report.ImportedAt,
		ReasoningMode:   reasoningMode,
		ReportPath:      "import-report.json",
	}})
	if err != nil {
		return nil, err
	}
	res.SessionID = sid
	res.SessionDir = sessionDir

	return res, nil
}

var errSessionIDExists = errors.New("session id already exists")

func validateSessionID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", nil
	}
	// Basic safety: treat path separators as invalid IDs.
	if strings.Contains(id, "/") || strings.Contains(id, "\\") {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	return id, nil
}
