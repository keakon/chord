package config

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/keybinding"
)

const keymapDiagnosticScope = "keymap"

// keymapSafeText bounds each user-controlled segment and quotes control bytes.
func keymapSafeText(value string) string {
	runes := []rune(value)
	if len(runes) > 120 {
		value = string(runes[:120]) + "…"
	}
	return strconv.Quote(value)
}

func keymapSource(index *SourceIndex, id keybinding.ActionID, inherited KeyMapConfig) string {
	if origin, ok := keymapEffectiveOrigin(index, id); ok {
		return fmt.Sprintf("%s:%d", keymapMessagePath(origin.File), origin.Line)
	}
	if _, ok := inherited[string(id)]; ok {
		return "inherited configuration"
	}
	return "built-in defaults"
}

func keymapBindingText(keys []string) string {
	if len(keys) == 0 {
		return "disabled"
	}
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = keymapSafeText(key)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func keymapDiagnostics(cfg *Config, index *SourceIndex, layers []keymapLayer, inherited KeyMapConfig) []Diagnostic {
	bindings := keybinding.Resolve(cfg.KeyMap)
	var diagnostics []Diagnostic
	for _, layer := range layers {
		issues := slices.Clone(layer.Issues)
		slices.SortStableFunc(issues, func(a, b keymapIssue) int {
			if d := cmp.Compare(a.Node.Line, b.Node.Line); d != 0 {
				return d
			}
			if d := cmp.Compare(a.Node.Column, b.Node.Column); d != 0 {
				return d
			}
			return cmp.Compare(a.Path, b.Path)
		})
		for _, issue := range issues {
			fallback := "no action changed"
			ignored := "declaration ignored"
			if issue.Path == "keymap" {
				fallback = "layer keymap ignored; effective bindings resolve from valid project, global and built-in settings"
				ignored = "entire field ignored"
			} else if _, ok := keybinding.Lookup(issue.Action); ok {
				fallback = "effective binding: " + keymapBindingText(bindings[issue.Action]) + " from " + keymapSource(index, issue.Action, inherited)
				ignored = "entire action ignored"
			}
			diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticSeverityWarning, File: layer.File, Path: issue.Path, Line: issue.Node.Line, Col: issue.Node.Column, Scope: keymapDiagnosticScope, Continues: true, Fallback: fallback, Message: keymapMessagePath(issue.Path) + ": " + issue.Reason + "; " + ignored + "; " + fallback})
		}
	}
	conflicts := keybinding.Conflicts(bindings)

	for _, conflict := range conflicts {
		loserOrigin, loserCustom := keymapEffectiveOrigin(index, conflict.Loser)
		winnerID := keybinding.ActionID(conflict.Winner)
		winnerOrigin, winnerCustom := keymapEffectiveOrigin(index, winnerID)
		if !loserCustom && !winnerCustom {
			continue
		}
		origin := loserOrigin
		if !loserCustom {
			origin = winnerOrigin
		}
		winnerSource := "built-in"
		if _, ok := keybinding.Lookup(winnerID); ok {
			winnerSource = keymapSource(index, winnerID, inherited)
		}
		fallback := "bindings retained; dispatch priority unchanged"
		message := fmt.Sprintf("keymap.%s: %s is shadowed in %s mode by %s (%s) before %s (%s); %s", conflict.Loser, keymapSafeText(conflict.Key), conflict.Mode, conflict.Winner, winnerSource, conflict.Loser, keymapSource(index, conflict.Loser, inherited), fallback)
		diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticSeverityWarning, File: origin.File, Line: origin.Line, Col: origin.Col, Path: "keymap." + string(conflict.Loser), Scope: keymapDiagnosticScope, Continues: true, Message: message})
	}
	return diagnostics
}

func keymapMessagePath(path string) string {
	// Keep ordinary dotted paths readable while escaping control characters and
	// oversized user-supplied action names.
	quoted := keymapSafeText(path)
	return quoted[1 : len(quoted)-1]
}

func logKeymapDiagnostics(diagnostics []Diagnostic) {
	for _, diagnostic := range diagnostics {
		log.Warn(diagnostic.String())
	}
}
