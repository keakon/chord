package tools

import (
	"context"
	"os"

	"github.com/keakon/chord/internal/lsp"
)

// PrewarmFile schedules preparation only; it never opens or edits the file.
// authorize is evaluated in the manager's bounded background task.
func (t ApplyPatchTool) PrewarmFile(ctx context.Context, path string, authorize func() bool) {
	prewarmEditFile(ctx, t.LSP, t.BaseDir, path, authorize)
}

func (t EditTool) PrewarmFile(ctx context.Context, path string, authorize func() bool) {
	prewarmEditFile(ctx, t.LSP, t.BaseDir, path, authorize)
}

func (t WriteTool) PrewarmFile(ctx context.Context, path string, authorize func() bool) {
	prewarmEditFile(ctx, t.LSP, t.BaseDir, path, authorize)
}

func prewarmEditFile(ctx context.Context, manager *lsp.Manager, baseDir, path string, authorize func() bool) {
	if manager == nil {
		return
	}
	resolved, err := resolveToolPathInDir(path, baseDir)
	if err == nil && !isBlockedDevicePath(resolved) {
		manager.Prewarm(ctx, resolved, func() bool {
			if authorize == nil || !authorize() {
				return false
			}
			// Patch edits and Unix file writes reject final symlinks; avoid
			// preparing them without changing directory symlink handling.
			info, err := os.Lstat(resolved)
			return os.IsNotExist(err) || err == nil && info.Mode()&os.ModeSymlink == 0
		})
	}
}
