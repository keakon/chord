package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/session"
)

func (a *MainAgent) exportPersistentSessionID() string {
	sid := filepath.Base(a.sessionDir)
	if sid == "" || sid == "." {
		return fmt.Sprintf("%d", time.Now().UnixMilli())
	}
	return sid
}

func (a *MainAgent) handleExportCommand(content string) {
	args := strings.TrimSpace(strings.TrimPrefix(content, "/export"))

	useJSON := strings.Contains(args, "--json")
	if useJSON {
		args = strings.TrimSpace(strings.ReplaceAll(args, "--json", ""))
	}

	pathArg := args
	persistID := a.exportPersistentSessionID()

	if pathArg == "" {
		ext := ".md"
		if useJSON {
			ext = ".json"
		}
		pathArg = filepath.Join(a.sessionArtifactsDir(), fmt.Sprintf("session-%s%s", persistID, ext))
	} else if !useJSON {
		if strings.HasSuffix(pathArg, ".json") {
			useJSON = true
		}
	}

	if err := os.MkdirAll(filepath.Dir(pathArg), 0o755); err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("create export directory: %w", err)})
		a.setIdleAndDrainPending()
		return
	}

	messages := a.ctxMgr.Snapshot()

	var stats *session.SessionStats
	if a.usageTracker != nil {
		as := a.usageTracker.SessionStats()
		stats = &session.SessionStats{
			InputTokens:      as.InputTokens,
			OutputTokens:     as.OutputTokens,
			CacheReadTokens:  as.CacheReadTokens,
			CacheWriteTokens: as.CacheWriteTokens,
			ReasoningTokens:  as.ReasoningTokens,
			LLMCalls:         as.LLMCalls,
			EstimatedCost:    as.EstimatedCost,
		}
	}

	metadata := map[string]string{
		"model":        a.ModelName(),
		"project_path": a.projectRoot,
		"session_id":   persistID,
		"instance_id":  a.instanceID,
	}

	exported, err := session.Export(messages, stats, metadata)
	if err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("export failed: %w", err)})
		a.setIdleAndDrainPending()
		return
	}

	if useJSON {
		err = session.ExportToFile(exported, pathArg)
	} else {
		err = session.ExportMarkdownToFile(exported, pathArg)
	}

	if err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("export failed: %w", err)})
	} else {
		format := "Markdown"
		if useJSON {
			format = "JSON"
		}
		a.emitToTUI(InfoEvent{Message: fmt.Sprintf("Session exported (%s) to %s", format, pathArg)})
	}
	a.setIdleAndDrainPending()
}
