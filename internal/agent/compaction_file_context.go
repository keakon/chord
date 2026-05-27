package agent

import (
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/message"
)

const (
	compactionInjectedFileMaxBytes  = 12 * 1024
	compactionInjectedFilesMaxBytes = 48 * 1024

	// compactionFileCtxPrefix opens the synthesized user message that re-loads
	// key files identified by the latest compaction summary. Detection on the
	// next request and generation here share this marker so they cannot drift.
	compactionFileCtxPrefix = "[system] Automatically loaded key files from the latest compaction checkpoint"
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

func compactionFileContextAlreadyInjected(msgs []message.Message, checkpointIdx int) bool {
	next := checkpointIdx + 1
	if next < 0 || next >= len(msgs) {
		return false
	}
	msg := msgs[next]
	if msg.Role != "user" || len(msg.Parts) == 0 {
		return false
	}
	return strings.Contains(msg.Parts[0].Text, compactionFileCtxPrefix)
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
	if compactionFileContextAlreadyInjected(messages, checkpointIdx) {
		return messages
	}
	keyFiles := extractCompactionKeyFiles(signature, a.projectRoot)
	if len(keyFiles) == 0 {
		return messages
	}

	result := filectx.BuildFilePartsWithOptions(keyFiles, a.resolveCheckpointFilePath, filectx.BuildFilePartsOptions{
		MaxFileBytes:  compactionInjectedFileMaxBytes,
		MaxTotalBytes: compactionInjectedFilesMaxBytes,
	})
	if len(result.Parts) == 0 {
		return messages
	}
	if result.TruncatedFiles > 0 || result.OmittedFiles > 0 {
		log.Debugf("compaction key-file context bounded loaded_files=%v truncated_files=%v omitted_files=%v total_bytes=%v max_file_bytes=%v max_total_bytes=%v", result.LoadedFiles, result.TruncatedFiles, result.OmittedFiles, result.TotalBytes, compactionInjectedFileMaxBytes, compactionInjectedFilesMaxBytes)
	}

	injected := message.Message{
		Role: "user",
		Parts: append([]message.ContentPart{{
			Type: "text",
			Text: compactionFileCtxPrefix + " for continuation.\n",
		}}, result.Parts...),
	}

	out := make([]message.Message, 0, len(messages)+1)
	out = append(out, messages[:checkpointIdx+1]...)
	out = append(out, injected)
	out = append(out, messages[checkpointIdx+1:]...)

	return out
}
