package agent

import (
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/message"
)

const (
	compactionInjectedFileMaxBytes  = 12 * 1024
	compactionInjectedFilesMaxBytes = 48 * 1024
)

func (a *MainAgent) latestCompactionSummarySignature(msgs []message.Message) (int, string) {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != "user" || !msg.IsCompactionSummary {
			continue
		}
		raw := strings.TrimSpace(msg.Content)
		if raw == "" {
			continue
		}
		return i, raw
	}
	return -1, ""
}

func (a *MainAgent) resolveCheckpointFilePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if a.projectRoot == "" {
		return path
	}
	return filepath.Join(a.projectRoot, filepath.FromSlash(path))
}

func (a *MainAgent) injectCompactionFileContext(messages []message.Message) []message.Message {
	if len(messages) == 0 || a.projectRoot == "" {
		return messages
	}
	checkpointIdx, signature := a.latestCompactionSummarySignature(messages)
	if checkpointIdx < 0 || signature == "" {
		return messages
	}
	keyFiles := extractCompactionKeyFiles(signature, a.projectRoot)
	if len(keyFiles) == 0 {
		return messages
	}

	a.compactionFileCtxMu.Lock()
	if a.compactionFileCtxSig == signature {
		a.compactionFileCtxMu.Unlock()
		return messages
	}
	a.compactionFileCtxMu.Unlock()

	result := filectx.BuildFilePartsWithOptions(keyFiles, a.resolveCheckpointFilePath, filectx.BuildFilePartsOptions{
		MaxFileBytes:  compactionInjectedFileMaxBytes,
		MaxTotalBytes: compactionInjectedFilesMaxBytes,
	})
	if len(result.Parts) == 0 {
		return messages
	}
	if result.TruncatedFiles > 0 || result.OmittedFiles > 0 {
		slog.Debug("compaction key-file context bounded",
			"loaded_files", result.LoadedFiles,
			"truncated_files", result.TruncatedFiles,
			"omitted_files", result.OmittedFiles,
			"total_bytes", result.TotalBytes,
			"max_file_bytes", compactionInjectedFileMaxBytes,
			"max_total_bytes", compactionInjectedFilesMaxBytes,
		)
	}

	injected := message.Message{
		Role: "user",
		Parts: append([]message.ContentPart{{
			Type: "text",
			Text: "[system] Automatically loaded key files from the latest compaction checkpoint for continuation.\n",
		}}, result.Parts...),
	}

	out := make([]message.Message, 0, len(messages)+1)
	out = append(out, messages[:checkpointIdx+1]...)
	out = append(out, injected)
	out = append(out, messages[checkpointIdx+1:]...)

	a.compactionFileCtxMu.Lock()
	a.compactionFileCtxSig = signature
	a.compactionFileCtxMu.Unlock()
	return out
}
