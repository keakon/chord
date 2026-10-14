package filefilter

import "strings"

// skipDirNames lists directory names that are always excluded from Grep/Glob
// searches (unless the user explicitly specifies one as the search path).
// These are VCS internal data, tool-specific runtime directories, and
// dependency/install directories that typically contain many non-source files.
var skipDirNames = map[string]bool{
	// VCS internal data directories
	".git": true,
	".svn": true,
	".hg":  true,
	".bzr": true,
	".jj":  true, // Jujutsu
	".sl":  true, // Sapling

	// Python
	"__pycache__":   true, // Python bytecode cache
	".mypy_cache":   true,
	".pytest_cache": true,
	".ruff_cache":   true,
	".pyre":         true,
	".pytype":       true,

	// Python virtual environments
	".venv": true,
	"venv":  true,
	"env":   true,

	// Node.js
	"node_modules": true,

	// Build artifacts
	"dist":   true,
	"build":  true,
	"target": true, // Rust, Java, etc.
	"out":    true, // Common output directory

	// Cache directories
	".cache": true,

	// IDE/Editor directories
	".idea": true, // JetBrains IDEs
}

// IsSkippedDirName reports whether Chord always excludes a directory with
// this basename from Grep/Glob/@-mention walks (e.g. VCS metadata, tool
// caches, compiled-bytecode dirs).
func IsSkippedDirName(name string) bool {
	return skipDirNames[name]
}

// IsExcludedPath returns true if the path is inside a skipped directory
// (VCS or tool data directories) or is one itself. The path must be relative
// to the scan root and use forward-slash separators.
func IsExcludedPath(p string) bool {
	parts := strings.SplitSeq(p, "/")
	for part := range parts {
		if skipDirNames[part] {
			return true
		}
	}
	return false
}
