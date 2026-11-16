package permission

import (
	"strings"

	"github.com/keakon/chord/internal/toolname"
)

// MayWiden reports whether a change cannot be proven no more permissive.
// The proof preserves selector order, including when the same parent rules
// precede both sets. Structural changes require review rather than a sampled
// approximation of glob and path coverage.
func (rs Ruleset) MayWiden(base Ruleset) bool {
	if len(rs) != len(base) {
		return true
	}
	for i, before := range base {
		after := rs[i]
		if toolname.Normalize(after.Permission) != toolname.Normalize(before.Permission) ||
			after.Pattern != before.Pattern || after.relaxScopedDeny != before.relaxScopedDeny {
			return true
		}
		if after.Action == before.Action {
			continue
		}
		switch after.Action {
		case ActionDeny:
			continue
		case ActionAsk:
			if before.Action == ActionAllow &&
				(!before.TargetsTool(toolname.Shell) || strings.TrimSpace(before.Pattern) == "*") {
				continue
			}
		}
		// A narrow shell allow is skipped for compound commands. Turning it
		// into ask can override an earlier deny, so it is not a restriction.
		return true
	}
	return false
}
