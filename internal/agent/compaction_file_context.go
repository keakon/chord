package agent

import (
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/message"
)

const (
	compactionInjectedFileMaxBytes  = 12 * 1024
	compactionInjectedFilesMaxBytes = 48 * 1024
	compactionInjectedFilesMinBytes = 8 * 1024

	// compactionFileCtxPrefix opens the synthesized user message that re-loads
	// key files identified by the latest compaction summary. Detection on the
	// next request and generation here share this marker so they cannot drift.
	compactionFileCtxPrefix = "[system] Automatically loaded key files from the latest compaction checkpoint"
)

func (a *MainAgent) latestCompactionSummarySignature(msgs []message.Message) (int, string) {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != message.RoleUser || !msg.IsCompactionSummary {
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
	if msg.Role != message.RoleUser || len(msg.Parts) == 0 {
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

	maxFileBytes, maxTotalBytes := a.compactionInjectedFileBudgets(messages)
	if maxTotalBytes <= 0 {
		log.Debugf("compaction key-file context omitted due to exhausted request budget key_files=%v", len(keyFiles))
		return messages
	}

	result := filectx.BuildFilePartsWithOptions(keyFiles, a.resolveCheckpointFilePath, filectx.BuildFilePartsOptions{
		MaxFileBytes:  maxFileBytes,
		MaxTotalBytes: maxTotalBytes,
	})
	if len(result.Parts) == 0 {
		return messages
	}
	if result.TruncatedFiles > 0 || result.OmittedFiles > 0 {
		log.Debugf("compaction key-file context bounded loaded_files=%v truncated_files=%v omitted_files=%v total_bytes=%v max_file_bytes=%v max_total_bytes=%v", result.LoadedFiles, result.TruncatedFiles, result.OmittedFiles, result.TotalBytes, maxFileBytes, maxTotalBytes)
	}

	injected := message.Message{
		Role: message.RoleUser,
		Parts: append([]message.ContentPart{{
			Type: message.ContentPartText,
			Text: compactionFileCtxPrefix + " for continuation.\n",
		}}, result.Parts...),
	}
	a.trackObservedFileParts(injected.Parts)

	out := make([]message.Message, 0, len(messages)+1)
	out = append(out, messages[:checkpointIdx+1]...)
	out = append(out, injected)
	out = append(out, messages[checkpointIdx+1:]...)

	return out
}

func (a *MainAgent) compactionInjectedFileBudgets(messages []message.Message) (maxFileBytes, maxTotalBytes int) {
	maxFileBytes = compactionInjectedFileMaxBytes
	maxTotalBytes = compactionInjectedFilesMaxBytes
	if a == nil || a.ctxMgr == nil {
		return maxFileBytes, maxTotalBytes
	}
	if a.ctxMgr.GetMaxTokens() <= 8192 {
		return maxFileBytes, maxTotalBytes
	}
	decision := a.ctxMgr.AutoCompactDecision()
	budget := decision.UsableInputBudget
	if budget <= 0 {
		budget = decision.InputBudget
	}
	if budget <= 0 {
		return maxFileBytes, maxTotalBytes
	}
	used := ctxmgr.EstimateMessagesTokens(messages)
	remainingTokens := budget - used
	if remainingTokens <= 0 {
		return maxFileBytes, 0
	}
	remainingBytes := remainingTokens * 3
	allowed := remainingBytes / 4
	if allowed > maxTotalBytes {
		allowed = maxTotalBytes
	}
	if allowed < compactionInjectedFilesMinBytes {
		return maxFileBytes, 0
	}
	maxTotalBytes = allowed
	if maxFileBytes > maxTotalBytes {
		maxFileBytes = maxTotalBytes
	}
	return maxFileBytes, maxTotalBytes
}
