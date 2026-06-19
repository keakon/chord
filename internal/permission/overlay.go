// Package permission implements overlay layering for permission rules.
//
// Overlays allow runtime additions to the permission ruleset. Persistent project
// and user-global additions are stored in agent YAML files; this overlay keeps
// the current process in sync immediately after a rule is added. Rules are
// evaluated in reverse order (last match wins), with layers merged in this
// priority (highest first):
//
//  1. session overlay (memory only, highest priority)
//  2. project agent-file additions made in this process
//  3. user-global agent-file additions made in this process
//  4. role base rules (activeConfig.Permission)
package permission

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// RuleScope represents where a permission rule is persisted.
type RuleScope int

const (
	ScopeSession    RuleScope = iota // memory only, cleared on session end
	ScopeProject                     // project agent file: <project>/.chord/agents/<role>.yaml
	ScopeUserGlobal                  // user agent file: <config-home>/agents/<role>.yaml
)

func (s RuleScope) String() string {
	switch s {
	case ScopeSession:
		return "session"
	case ScopeProject:
		return "project"
	case ScopeUserGlobal:
		return "user-global"
	default:
		return "unknown"
	}
}

// AddedRule tracks a rule added through the picker UI within this session.
type AddedRule struct {
	Role    string // active role name when added (e.g. "builder")
	Rule    Rule
	Scope   RuleScope
	Path    string // file path for persistent scopes, empty for session
	AddedAt time.Time
}

// Overlay manages the layered permission rules.
type Overlay struct {
	mu sync.RWMutex

	// activeRole determines which role-specific session rules participate in the
	// merged ruleset. Persistent overlays remain per-role because their file path
	// already encodes the role.
	activeRole string

	// session rules (memory only, highest priority), partitioned by role so a
	// session allow added under builder does not leak into planner.
	sessionByRole map[string]Ruleset

	// project persistent overlay path
	projectPath string
	project     Ruleset

	// user-global persistent overlay path
	userGlobalPath string
	userGlobal     Ruleset

	// base rules from active role config
	base Ruleset

	// mergedCache stores the current merged ruleset for hot-path lookups. It is
	// invalidated on any state change and rebuilt lazily under lock.
	mergedCache Ruleset
	mergedDirty bool

	// addedRules tracks rules added this session (for /rules undo)
	addedRules []AddedRule
}

// NewOverlay creates a new overlay manager.
func NewOverlay() *Overlay {
	return &Overlay{sessionByRole: make(map[string]Ruleset), mergedDirty: true}
}

func (o *Overlay) invalidateMergedLocked() {
	o.mergedCache = nil
	o.mergedDirty = true
}

// SetActiveRole updates the role whose session overlay participates in merged evaluation.
func (o *Overlay) SetActiveRole(role string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.activeRole = normalizeOverlayRole(role)
	if o.sessionByRole == nil {
		o.sessionByRole = make(map[string]Ruleset)
	}
	o.invalidateMergedLocked()
}

// SetBase sets the base rules from the active role config.
func (o *Overlay) SetBase(rules Ruleset) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.base = rules
	o.invalidateMergedLocked()
}

// SetProjectPath sets the path for project-scoped persistent overlay.
func (o *Overlay) SetProjectPath(path string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.projectPath = path
}

// SetUserGlobalPath sets the path for user-global persistent overlay.
func (o *Overlay) SetUserGlobalPath(path string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.userGlobalPath = path
}

// MergedRuleset returns the full ruleset with all layers merged.
// Later rules override earlier ones (last-match-wins).
func (o *Overlay) MergedRuleset() Ruleset {
	merged := o.mergedRuleset()
	out := make(Ruleset, len(merged))
	copy(out, merged)
	return out
}

func (o *Overlay) mergedRuleset() Ruleset {
	o.mu.RLock()
	if !o.mergedDirty {
		merged := o.mergedCache
		o.mu.RUnlock()
		return merged
	}
	o.mu.RUnlock()

	o.mu.Lock()
	defer o.mu.Unlock()
	return o.mergedRulesetLocked()
}

func (o *Overlay) mergedRulesetLocked() Ruleset {
	if !o.mergedDirty {
		return o.mergedCache
	}
	// Merge order: base → user-global → project → session
	// last-match-wins means session has highest priority
	merged := Merge(o.base, o.userGlobal, o.project, o.activeSessionLocked())
	o.mergedCache = merged
	o.mergedDirty = false
	return merged
}

func (o *Overlay) activeSessionLocked() Ruleset {
	role := normalizeOverlayRole(o.activeRole)
	if role == "" {
		return nil
	}
	return o.sessionByRole[role]
}

// Evaluate resolves the action using overlay layers with last-match-wins semantics.
func (o *Overlay) Evaluate(permission, pattern string) Action {
	return o.mergedRuleset().Evaluate(permission, pattern)
}

// IsDisabled checks if a tool is completely unavailable.
func (o *Overlay) IsDisabled(toolName string) bool {
	return o.mergedRuleset().IsDisabled(toolName)
}

// LastMatch finds the last matching rule.
func (o *Overlay) LastMatch(permission, pattern string) MatchResult {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, rs := range o.rulesetsHighToLowLocked() {
		if result := rs.LastMatch(permission, pattern); result.Found {
			return result
		}
	}
	return MatchResult{}
}

// LastExactPatternMatch finds the last exact pattern match.
func (o *Overlay) LastExactPatternMatch(permission, pattern string) MatchResult {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, rs := range o.rulesetsHighToLowLocked() {
		if result := rs.LastExactPatternMatch(permission, pattern); result.Found {
			return result
		}
	}
	return MatchResult{}
}

func (o *Overlay) rulesetsHighToLowLocked() [4]Ruleset {
	return [4]Ruleset{o.activeSessionLocked(), o.project, o.userGlobal, o.base}
}

// AddSessionRule adds a rule to the session overlay (memory only).
func (o *Overlay) AddSessionRule(role string, r Rule) {
	o.mu.Lock()
	defer o.mu.Unlock()
	role = normalizeOverlayRole(role)
	if role == "" {
		return
	}
	if o.sessionByRole == nil {
		o.sessionByRole = make(map[string]Ruleset)
	}
	rs := o.sessionByRole[role]
	if rulesetContainsRule(rs, r) {
		return
	}
	rs = append(rs, r)
	o.sessionByRole[role] = rs
	o.invalidateMergedLocked()
	o.addedRules = append(o.addedRules, AddedRule{
		Role:    role,
		Rule:    r,
		Scope:   ScopeSession,
		AddedAt: time.Now(),
	})
}

// AddPersistentRule records a project or user-global agent-file rule in memory so
// the current process observes it immediately after the caller persists it.
func (o *Overlay) AddPersistentRule(role string, r Rule, scope RuleScope, path string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	role = normalizeOverlayRole(role)
	var target *Ruleset
	switch scope {
	case ScopeProject:
		target = &o.project
	case ScopeUserGlobal:
		target = &o.userGlobal
	default:
		return fmt.Errorf("persistent rule scope must be project or user-global, got %s", scope.String())
	}
	if rulesetContainsRule(*target, r) {
		return nil
	}
	*target = append(*target, r)
	o.invalidateMergedLocked()
	o.addedRules = append(o.addedRules, AddedRule{
		Role:    role,
		Rule:    r,
		Scope:   scope,
		Path:    strings.TrimSpace(path),
		AddedAt: time.Now(),
	})
	return nil
}

// AddRule adds a rule to the specified scope.
func (o *Overlay) AddRule(role string, r Rule, scope RuleScope) error {
	switch scope {
	case ScopeSession:
		o.AddSessionRule(role, r)
		return nil
	case ScopeProject:
		return o.AddPersistentRule(role, r, scope, o.projectPath)
	case ScopeUserGlobal:
		return o.AddPersistentRule(role, r, scope, o.userGlobalPath)
	default:
		return fmt.Errorf("unknown rule scope: %d", scope)
	}
}

// RemoveSessionRule removes a rule from the active role's session overlay by index.
func (o *Overlay) RemoveSessionRule(index int) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	role := normalizeOverlayRole(o.activeRole)
	rs := o.sessionByRole[role]
	if index < 0 || index >= len(rs) {
		return false
	}
	rs = append(rs[:index], rs[index+1:]...)
	if len(rs) == 0 {
		delete(o.sessionByRole, role)
	} else {
		o.sessionByRole[role] = rs
	}
	o.invalidateMergedLocked()
	return true
}

// AddedRules returns a copy of rules added this session in insertion order.
func (o *Overlay) AddedRules() []AddedRule {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]AddedRule, len(o.addedRules))
	copy(out, o.addedRules)
	return out
}

// RemoveAddedRule removes a rule that was added this session.
// For session rules, it removes from the role-specific session overlay.
// For persistent rules, it removes from both memory and disk.
func (o *Overlay) RemoveAddedRule(index int) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if index < 0 || index >= len(o.addedRules) {
		return fmt.Errorf("added rule index out of range: %d", index)
	}
	added := o.addedRules[index]

	switch added.Scope {
	case ScopeSession:
		o.removeSessionRuleLocked(added.Role, added.Rule)
		o.addedRules = append(o.addedRules[:index], o.addedRules[index+1:]...)
		return nil
	case ScopeProject:
		o.project = removeLastMatchingRule(o.project, added.Rule)
		o.invalidateMergedLocked()
		o.addedRules = append(o.addedRules[:index], o.addedRules[index+1:]...)
		return nil
	case ScopeUserGlobal:
		o.userGlobal = removeLastMatchingRule(o.userGlobal, added.Rule)
		o.invalidateMergedLocked()
		o.addedRules = append(o.addedRules[:index], o.addedRules[index+1:]...)
		return nil
	default:
		return fmt.Errorf("unknown added rule scope: %d", added.Scope)
	}
}

func (o *Overlay) removeSessionRuleLocked(role string, target Rule) {
	role = normalizeOverlayRole(role)
	rs := o.sessionByRole[role]
	rs = removeLastMatchingRule(rs, target)
	if len(rs) == 0 {
		delete(o.sessionByRole, role)
	} else {
		o.sessionByRole[role] = rs
	}
	o.invalidateMergedLocked()
}

func removeLastMatchingRule(rs Ruleset, target Rule) Ruleset {
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if sameRule(r, target) {
			return append(rs[:i], rs[i+1:]...)
		}
	}
	return rs
}

func sameRule(a, b Rule) bool {
	return a.Permission == b.Permission && a.Pattern == b.Pattern && a.Action == b.Action
}

func rulesetContainsRule(rs Ruleset, target Rule) bool {
	for _, r := range rs {
		if sameRule(r, target) {
			return true
		}
	}
	return false
}

func normalizeOverlayRole(role string) string {
	return strings.TrimSpace(role)
}

// SessionRuleCount returns the number of session rules for the active role.
func (o *Overlay) SessionRuleCount() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return len(o.activeSessionLocked())
}

// SessionRuleCountForRole returns the number of session rules for the provided role.
func (o *Overlay) SessionRuleCountForRole(role string) int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return len(o.sessionByRole[normalizeOverlayRole(role)])
}

// ProjectPath returns the project agent config path.
func (o *Overlay) ProjectPath() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.projectPath
}

// UserGlobalPath returns the user-global agent config path.
func (o *Overlay) UserGlobalPath() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.userGlobalPath
}
