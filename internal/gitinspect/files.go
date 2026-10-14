package gitinspect

import "context"

// Files returns Git's raw newline-delimited path listing. Tracked listings
// recurse into submodules; untracked listings honor standard ignore rules.
// Callers own parsing, filesystem existence checks and result limits.
func Files(ctx context.Context, workDir string, tracked bool) (string, error) {
	if tracked {
		return gitCommand(ctx, workDir, "ls-files", "--recurse-submodules")
	}
	return gitCommand(ctx, workDir, "ls-files", "--others", "--exclude-standard")
}
