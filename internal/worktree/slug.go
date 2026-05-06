// Package worktree manages chord-owned git worktrees: creation, removal,
// listing, and cross-worktree session discovery.
//
// Worktrees are created at <stateDir>/worktrees/<repoID>/<slug> with branches
// named chord/<slug>. Each worktree gets its own ProjectKey via the existing
// PathLocator, so sessions/cache/exports are physically isolated. A repo
// index at <stateDir>/repos/<repoID>.json provides logical aggregation
// across the main repo and all of its worktrees.
package worktree

import (
	"fmt"
	"strings"
	"time"
)

// MaxSlugLen caps slug length; keeps file system paths short on Windows
// where MAX_PATH can be a real ceiling.
const MaxSlugLen = 80

// ValidateSlug enforces a strict, portable allowlist for worktree names.
// Disallows '/', whitespace, '..', leading '.' or '-', and characters
// outside [a-zA-Z0-9._-]. Returns nil when s is acceptable.
func ValidateSlug(s string) error {
	if s == "" {
		return fmt.Errorf("worktree name is empty")
	}
	if len(s) > MaxSlugLen {
		return fmt.Errorf("worktree name %q is longer than %d characters", s, MaxSlugLen)
	}
	if s == "." || s == ".." || strings.Contains(s, "..") {
		return fmt.Errorf("worktree name %q contains forbidden sequence", s)
	}
	switch s[0] {
	case '.', '-':
		return fmt.Errorf("worktree name %q must not start with %q", s, string(s[0]))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.' || c == '_' || c == '-':
			// allowed
		default:
			return fmt.Errorf("worktree name %q contains forbidden character %q", s, string(c))
		}
	}
	return nil
}

// GenerateAutoSlug returns a deterministic timestamp-based slug like
// "task-20260507-091245". The result always passes ValidateSlug.
func GenerateAutoSlug(now time.Time) string {
	return "task-" + now.UTC().Format("20060102-150405")
}
