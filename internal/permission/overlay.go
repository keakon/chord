// Package permission implements overlay layering for permission rules.
//
// Overlays allow runtime additions to the permission ruleset without modifying
// the base role configuration. Rules are evaluated in reverse order (last match wins),
// with layers merged in this priority (highest first):
//
//  1. session overlay (memory only, highest priority)
//  2. project persistent overlay (.chord/permissions/<role>.yaml)
//  3. user-global persistent overlay (<config-home>/permissions/<role>.yaml)
//  4. role base rules (activeConfig.Permission)
package permission

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// RuleScope represents where a permission rule is persisted.
type RuleScope int

const (
	ScopeSession    RuleScope = iota // memory only, cleared on session end
	ScopeProject                     // .chord/permissions/<role>.yaml
	ScopeUserGlobal                  // <config-home>/permissions/<role>.yaml
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

	// addedRules tracks rules added this session (for /rules undo)
	addedRules []AddedRule
}

// NewOverlay creates a new overlay manager.
func NewOverlay() *Overlay {
	return &Overlay{sessionByRole: make(map[string]Ruleset)}
}

// SetActiveRole updates the role whose session overlay participates in merged evaluation.
func (o *Overlay) SetActiveRole(role string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.activeRole = normalizeOverlayRole(role)
	if o.sessionByRole == nil {
		o.sessionByRole = make(map[string]Ruleset)
	}
}

// SetBase sets the base rules from the active role config.
func (o *Overlay) SetBase(rules Ruleset) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.base = rules
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

// LoadPersistentOverlays loads project and user-global overlays from disk.
func (o *Overlay) LoadPersistentOverlays() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.projectPath != "" {
		rules, err := loadOverlayFile(o.projectPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		o.project = rules
	}

	if o.userGlobalPath != "" {
		rules, err := loadOverlayFile(o.userGlobalPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		o.userGlobal = rules
	}

	return nil
}

// loadOverlayFile reads and parses an overlay YAML file.
func loadOverlayFile(path string) (Ruleset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, err
	}

	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		root := node.Content[0]
		if root != nil && root.Kind == yaml.MappingNode {
			permissionNode := mappingValue(root, "permission")
			if permissionNode != nil && permissionNode.Kind == yaml.MappingNode {
				return ParsePermission(permissionNode), nil
			}
			return nil, fmt.Errorf("overlay file %q must use root \"permission\" mapping", path)
		}
		return nil, fmt.Errorf("overlay file %q has invalid root structure", path)
	}
	return nil, fmt.Errorf("overlay file %q has invalid document structure", path)
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		v := mapping.Content[i+1]
		if k != nil && k.Value == key {
			return v
		}
	}
	return nil
}

// MergedRuleset returns the full ruleset with all layers merged.
// Later rules override earlier ones (last-match-wins).
func (o *Overlay) MergedRuleset() Ruleset {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.mergedRulesetLocked()
}

func (o *Overlay) mergedRulesetLocked() Ruleset {
	// Merge order: base → user-global → project → session
	// last-match-wins means session has highest priority
	return Merge(o.base, o.userGlobal, o.project, o.activeSessionLocked())
}

func (o *Overlay) activeSessionLocked() Ruleset {
	role := normalizeOverlayRole(o.activeRole)
	if role == "" {
		return nil
	}
	return o.sessionByRole[role]
}

// Evaluate resolves the action using merged rulesets with last-match-wins.
func (o *Overlay) Evaluate(permission, pattern string) Action {
	return o.MergedRuleset().Evaluate(permission, pattern)
}

// IsDisabled checks if a tool is completely unavailable.
func (o *Overlay) IsDisabled(toolName string) bool {
	return o.MergedRuleset().IsDisabled(toolName)
}

// LastMatch finds the last matching rule.
func (o *Overlay) LastMatch(permission, pattern string) MatchResult {
	return o.MergedRuleset().LastMatch(permission, pattern)
}

// LastExactPatternMatch finds the last exact pattern match.
func (o *Overlay) LastExactPatternMatch(permission, pattern string) MatchResult {
	return o.MergedRuleset().LastExactPatternMatch(permission, pattern)
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
	o.addedRules = append(o.addedRules, AddedRule{
		Role:    role,
		Rule:    r,
		Scope:   ScopeSession,
		AddedAt: time.Now(),
	})
}

// AddProjectRule adds a rule to the project overlay and persists it.
func (o *Overlay) AddProjectRule(role string, r Rule) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	role = normalizeOverlayRole(role)
	if o.projectPath == "" {
		return fmt.Errorf("project overlay path is empty")
	}
	if rulesetContainsRule(o.project, r) {
		return nil
	}
	if err := AppendRoleOverlayRule(o.projectPath, r); err != nil {
		return err
	}
	o.project = append(o.project, r)
	o.addedRules = append(o.addedRules, AddedRule{
		Role:    role,
		Rule:    r,
		Scope:   ScopeProject,
		Path:    o.projectPath,
		AddedAt: time.Now(),
	})
	return nil
}

// AddUserGlobalRule adds a rule to the user-global overlay and persists it.
func (o *Overlay) AddUserGlobalRule(role string, r Rule) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	role = normalizeOverlayRole(role)
	if o.userGlobalPath == "" {
		return fmt.Errorf("user-global overlay path is empty")
	}
	if rulesetContainsRule(o.userGlobal, r) {
		return nil
	}
	if err := AppendRoleOverlayRule(o.userGlobalPath, r); err != nil {
		return err
	}
	o.userGlobal = append(o.userGlobal, r)
	o.addedRules = append(o.addedRules, AddedRule{
		Role:    role,
		Rule:    r,
		Scope:   ScopeUserGlobal,
		Path:    o.userGlobalPath,
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
		return o.AddProjectRule(role, r)
	case ScopeUserGlobal:
		return o.AddUserGlobalRule(role, r)
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
		if added.Path != "" {
			if err := RemoveRoleOverlayRule(added.Path, added.Rule); err != nil {
				return err
			}
		}
		o.project = removeLastMatchingRule(o.project, added.Rule)
		o.addedRules = append(o.addedRules[:index], o.addedRules[index+1:]...)
		return nil
	case ScopeUserGlobal:
		if added.Path != "" {
			if err := RemoveRoleOverlayRule(added.Path, added.Rule); err != nil {
				return err
			}
		}
		o.userGlobal = removeLastMatchingRule(o.userGlobal, added.Rule)
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

// ProjectPath returns the project overlay file path.
func (o *Overlay) ProjectPath() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.projectPath
}

// UserGlobalPath returns the user-global overlay file path.
func (o *Overlay) UserGlobalPath() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.userGlobalPath
}
