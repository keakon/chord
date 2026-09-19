package agent

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
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

func (a *MainAgent) latestCompactionSummarySignature(msgs []message.Message) (int, string, map[string]string) {
	for i, msg := range slices.Backward(msgs) {

		if msg.Role != message.RoleUser || !msg.IsCompactionSummary {
			continue
		}
		raw := strings.TrimSpace(msg.Content)
		if raw == "" {
			continue
		}
		return i, raw, cloneCompactionFileRevisions(msg.CompactionFileRevisions)
	}
	return -1, "", nil
}

func cloneCompactionFileRevisions(revisions map[string]string) map[string]string {
	if len(revisions) == 0 {
		return nil
	}
	copy := make(map[string]string, len(revisions))
	maps.Copy(copy, revisions)
	return copy
}

func captureCompactionFileRevisions(paths []string, resolvePath func(string) string) map[string]string {
	if len(paths) == 0 {
		return nil
	}
	revisions := make(map[string]string, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		resolved := path
		if resolvePath != nil {
			resolved = resolvePath(path)
		}
		// Keep an empty revision as an explicit baseline. If the file is
		// unavailable when the checkpoint is created, a later readable copy
		// must be reported as changed rather than treated as a first sighting.
		revisions[path] = computeFileHash(resolved)
	}
	if len(revisions) == 0 {
		return nil
	}
	return revisions
}

func (a *MainAgent) refreshCompactionFileRevisions(messages []message.Message) []message.Message {
	if a == nil || len(messages) == 0 {
		return messages
	}
	refreshed := append([]message.Message(nil), messages...)
	for i := range refreshed {
		msg := &refreshed[i]
		if !msg.IsCompactionSummary {
			continue
		}
		paths := a.compactionContinuationFiles(msg.Content)
		msg.CompactionFileRevisions = captureCompactionFileRevisions(paths, a.resolveCheckpointFilePath)
		break
	}
	return refreshed
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

// resolveCheckpointFileReadPath maps a checkpoint path to the location the
// confined read opens, or "" when it must not be loaded. The returned path is
// the symlink-resolved location relative to the resolved project root: os.Root
// refuses to follow a symlink whose target is absolute even when the target
// stays inside the root, so the lexical spelling cannot be used for the read.
// A target outside the resolved root is rejected here; os.Root stays the
// boundary against a symlink swapped in after this check.
func (a *MainAgent) resolveCheckpointFileReadPath(path string) string {
	if path == "" || filepath.IsAbs(path) || a.projectRoot == "" {
		return ""
	}
	resolvedRoot, err := filepath.EvalSymlinks(a.projectRoot)
	if err != nil {
		return ""
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Join(a.projectRoot, filepath.FromSlash(path)))
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (a *MainAgent) readCheckpointFile(path string) ([]byte, error) {
	if a == nil || a.projectRoot == "" {
		return nil, os.ErrInvalid
	}
	root, err := os.OpenRoot(a.projectRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(filepath.FromSlash(path))
}

// compactionContinuationFiles is the ordered, de-duplicated file set a
// continuation re-loads after a checkpoint: the state_files the checkpoint
// registered, then the summary's key files that were not already declared.
// Every candidate must still pass the read permission gate; a rejected path is
// marked spent so the key-file pass cannot re-add it.
func (a *MainAgent) compactionContinuationFiles(signature string) []string {
	if a == nil {
		return nil
	}
	declared := extractCompactionStateFiles(signature, a.projectRoot)
	keyFiles := extractCompactionKeyFiles(signature, a.projectRoot)
	if len(declared) == 0 && len(keyFiles) == 0 {
		return nil
	}
	files := make([]string, 0, len(declared)+len(keyFiles))
	seen := make(map[string]bool, len(declared)+len(keyFiles))
	appendInjectable := func(rel string) {
		if rel == "" || seen[rel] {
			return
		}
		seen[rel] = true
		if !a.stateFileInjectableForRead(a.resolveCheckpointFilePath(rel)) || a.resolveCheckpointFileReadPath(rel) == "" {
			return
		}
		files = append(files, rel)
	}
	for _, rel := range declared {
		appendInjectable(rel)
	}
	for _, rel := range keyFiles {
		appendInjectable(rel)
	}
	return files
}

// stateFileInjectableForRead reports whether the runtime may put a
// model-declared or summary-extracted state file back into the context after a
// reset. The overlay must never widen what the model could already reach, so
// the current read permission rule has to resolve to allow — an ask rule would
// otherwise turn the overlay into a silent auto-approval. The file tracker is
// deliberately not consulted: it only carries the surviving transcript, so an
// archived checkpoint's read records are gone after a restore, and re-injecting
// content the model itself registered is what this overlay exists for.
func (a *MainAgent) stateFileInjectableForRead(absPath string) bool {
	if a == nil || absPath == "" {
		return false
	}
	// Under YOLO the execution gate bypasses ordinary tools outright, so a
	// read of this file is already reachable and the overlay cannot widen
	// anything: mirror the bypass instead of evaluating the filtered ruleset,
	// whose ordinary read rules are dropped and would report a false deny.
	if a.YoloEnabled() {
		return true
	}
	action := a.effectiveRuleset().EvaluatePath(tools.NameRead, absPath, a.projectRoot)
	return normalizeToolPermissionAction(tools.NameRead, action) == permission.ActionAllow
}

// injectCompactionFileContext inserts the request-local key-file overlay right
// after the latest compaction checkpoint. It returns the (possibly) extended
// message list plus the index the overlay was inserted at, or -1 when nothing
// was injected. Callers must invoke it only after the prepared surface has
// been remembered: the overlay never enters the durable history, so recording
// it in the stable-prefix shapes would break prefix compatibility on the next
// request and disable incremental reduction reuse after the first compaction.
func (a *MainAgent) injectCompactionFileContext(messages []message.Message) ([]message.Message, int) {
	if len(messages) == 0 || a.projectRoot == "" {
		return messages, -1
	}
	checkpointIdx, signature, revisions := a.latestCompactionSummarySignature(messages)
	if checkpointIdx < 0 || signature == "" {
		return messages, -1
	}
	if compactionFileContextAlreadyInjected(messages, checkpointIdx) {
		return messages, -1
	}
	keyFiles := a.compactionContinuationFiles(signature)
	if len(keyFiles) == 0 {
		return messages, -1
	}

	maxFileBytes, maxTotalBytes := a.compactionInjectedFileBudgets(messages)
	if maxTotalBytes <= 0 {
		log.Debugf("compaction key-file context omitted due to exhausted request budget key_files=%v", len(keyFiles))
		return messages, -1
	}

	result := filectx.BuildFilePartsWithOptions(keyFiles, a.resolveCheckpointFileReadPath, filectx.BuildFilePartsOptions{
		MaxFileBytes:  maxFileBytes,
		MaxTotalBytes: maxTotalBytes,
		ReadFile:      a.readCheckpointFile,
	})
	if len(result.Parts) == 0 {
		return messages, -1
	}
	a.annotateCompactionFileParts(signature, revisions, result.Parts)
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

	return out, checkpointIdx + 1
}

func (a *MainAgent) annotateCompactionFileParts(checkpoint string, revisions map[string]string, parts []message.ContentPart) {
	if a == nil || checkpoint == "" || len(parts) == 0 {
		return
	}
	for i := range parts {
		part := &parts[i]
		if part.Type != message.ContentPartText || !message.IsFileRefContent(part.Text) {
			continue
		}
		displayPath, ok := message.FirstFileRefPath(part.Text)
		if !ok || displayPath == "" {
			continue
		}
		hash := computeFileHash(a.resolveCheckpointFilePath(displayPath))
		if hash == "" {
			continue
		}
		previous, seen := revisions[displayPath]
		// Legacy checkpoints have no persisted manifest. Treat every loaded
		// file as changed so the model does not receive a false "unchanged"
		// assertion after a restart. A manifest that omits a path is also
		// conservative: the summary did not establish a baseline for it.
		changed := len(revisions) == 0 || !seen || previous != hash
		part.Text = annotateFileRefRevision(part.Text, hash, changed)
	}
}

func annotateFileRefRevision(text, revision string, changed bool) string {
	close := strings.IndexByte(text, '>')
	if close < 0 || !strings.HasPrefix(strings.TrimSpace(text), message.FileRefOpenTag) {
		return text
	}
	attrs := fmt.Sprintf(" revision=%q changed_since_checkpoint=%q", "sha256:"+revision, strconv.FormatBool(changed))
	return text[:close] + attrs + text[close:]
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
	used := estimateMessagesTokens(a.ctxMgr, messages)
	remainingTokens := budget - used
	if remainingTokens <= 0 {
		return maxFileBytes, 0
	}
	remainingBytes := estimateBytesForTokens(a.ctxMgr, remainingTokens)
	allowed := min(remainingBytes/4, maxTotalBytes)
	if allowed < compactionInjectedFilesMinBytes {
		return maxFileBytes, 0
	}
	maxTotalBytes = allowed
	if maxFileBytes > maxTotalBytes {
		maxFileBytes = maxTotalBytes
	}
	return maxFileBytes, maxTotalBytes
}
