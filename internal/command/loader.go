// Package command discovers, parses, and expands custom slash commands.
//
// Command definitions are loaded from (in priority order):
//  1. Project-level MD:   .chord/commands/**/*.md
//  2. Global-level MD:    <config-home>/commands/**/*.md
//  3. Project-level YAML: .chord/config.yaml commands field
//  4. Global-level YAML:  <config-home>/config.yaml commands field
//
// First occurrence of a name wins. Built-in names are always reserved.
package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// Definition holds a single custom slash command.
type Definition struct {
	Name        string // without leading "/", e.g. "review" or "foo/bar"
	Description string // from frontmatter; may be empty
	Template    string // full body (with $ARGUMENTS placeholder if present)
	Location    string // absolute path; YAML commands use config file path
	Source      string // project-md / global-md / project-yaml / global-yaml
}

// builtinNames is the set of reserved slash command names (without "/").
var builtinNames = map[string]bool{
	"compact": true,
	"export":  true,
	"help":    true,
	"model":   true,
	"new":     true,
	"resume":  true,
	"stats":   true,
}

// frontmatter holds the YAML fields from an MD file header.
type frontmatter struct {
	Description string `yaml:"description"`
}

// parseMDFile parses a command MD file and returns frontmatter + body.
func parseMDFile(data []byte) (frontmatter, string, error) {
	var fm frontmatter
	s := string(data)
	if !strings.HasPrefix(s, "---") {
		return fm, s, nil
	}
	rest := s[3:]
	if len(rest) > 0 && rest[0] == '\r' {
		rest = rest[1:]
	}
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}
	closingIdx := strings.Index(rest, "\n---")
	if closingIdx < 0 {
		return fm, s, nil
	}
	fmContent := rest[:closingIdx]
	body := rest[closingIdx+4:] // skip "\n---"
	if len(body) > 0 && body[0] == '\n' {
		body = body[1:]
	} else if len(body) > 1 && body[0] == '\r' && body[1] == '\n' {
		body = body[2:]
	}
	if err := yaml.Unmarshal([]byte(fmContent), &fm); err != nil {
		return fm, s, fmt.Errorf("invalid frontmatter YAML: %w", err)
	}
	return fm, body, nil
}

// normalizeName strips a leading "/" and converts path separators to "/".
func normalizeName(raw string) string {
	name := strings.TrimPrefix(raw, "/")
	return filepath.ToSlash(name)
}

// scanMDDir scans dir for **/*.md files and returns Definitions.
// source is "project-md" or "global-md".
func scanMDDir(dir, source string) ([]*Definition, []string) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	matches, err := doublestar.Glob(os.DirFS(dir), "**/*.md")
	if err != nil {
		return nil, []string{fmt.Sprintf("scan %s: %v", dir, err)}
	}
	var defs []*Definition
	var warnings []string
	for _, rel := range matches {
		absPath := filepath.Join(dir, rel)
		data, readErr := os.ReadFile(absPath)
		if readErr != nil {
			warnings = append(warnings, fmt.Sprintf("read %s: %v", absPath, readErr))
			continue
		}
		fm, body, parseErr := parseMDFile(data)
		if parseErr != nil {
			warnings = append(warnings, fmt.Sprintf("parse %s: %v", absPath, parseErr))
			continue
		}
		body = strings.TrimSpace(body)
		if body == "" {
			warnings = append(warnings, fmt.Sprintf("skip %s: empty body", absPath))
			continue
		}
		// Derive name from relative path: strip .md extension.
		name := normalizeName(strings.TrimSuffix(filepath.ToSlash(rel), ".md"))
		if name == "" {
			continue
		}
		defs = append(defs, &Definition{
			Name:        name,
			Description: fm.Description,
			Template:    body,
			Location:    absPath,
			Source:      source,
		})
	}
	return defs, warnings
}

// fromYAML converts a map[string]string commands config into Definitions.
// source is "project-yaml" or "global-yaml".
func fromYAML(commands map[string]string, configPath, source string) ([]*Definition, []string) {
	if len(commands) == 0 {
		return nil, nil
	}
	var defs []*Definition
	var warnings []string
	for key, text := range commands {
		name := normalizeName(key)
		if name == "" {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			warnings = append(warnings, fmt.Sprintf("skip YAML command %q: empty body", key))
			continue
		}
		defs = append(defs, &Definition{
			Name:     name,
			Template: text,
			Location: configPath,
			Source:   source,
		})
	}
	return defs, warnings
}

// Merge combines definitions from multiple sources in priority order.
// Built-in names are skipped with a warning. First occurrence of a name wins.
func Merge(sources ...[]*Definition) ([]*Definition, []string) {
	seen := make(map[string]bool)
	var out []*Definition
	var warnings []string
	for _, defs := range sources {
		for _, d := range defs {
			if builtinNames[d.Name] {
				warnings = append(warnings, fmt.Sprintf("skip command %q: reserved built-in name", d.Name))
				continue
			}
			if seen[d.Name] {
				continue
			}
			seen[d.Name] = true
			out = append(out, d)
		}
	}
	return out, warnings
}

// LoadOptions holds the paths needed to load commands from all four layers.
type LoadOptions struct {
	ProjectRoot    string            // project working directory
	ConfigHome     string            // user-level config directory
	ProjectCfg     map[string]string // project config.yaml commands
	ProjectCfgPath string            // absolute path to project config.yaml
	GlobalCfg      map[string]string // global config.yaml commands
	GlobalCfgPath  string            // absolute path to global config.yaml
}

// Load scans all four layers, merges them, and returns the final definitions.
// Warnings from bad files or reserved names are returned but do not block loading.
func Load(opts LoadOptions) ([]*Definition, []string) {
	var allWarnings []string

	projectMDDir := filepath.Join(opts.ProjectRoot, ".chord", "commands")
	globalMDDir := filepath.Join(opts.ConfigHome, "commands")

	projectMD, w := scanMDDir(projectMDDir, "project-md")
	allWarnings = append(allWarnings, w...)
	globalMD, w := scanMDDir(globalMDDir, "global-md")
	allWarnings = append(allWarnings, w...)

	projectYAML, w := fromYAML(opts.ProjectCfg, opts.ProjectCfgPath, "project-yaml")
	allWarnings = append(allWarnings, w...)
	globalYAML, w := fromYAML(opts.GlobalCfg, opts.GlobalCfgPath, "global-yaml")
	allWarnings = append(allWarnings, w...)

	merged, w := Merge(projectMD, globalMD, projectYAML, globalYAML)
	allWarnings = append(allWarnings, w...)
	return merged, allWarnings
}

// ParseInput splits a trimmed slash line into (commandName, arguments).
// commandName is without the leading "/", e.g. "review".
// arguments is everything after the first whitespace-separated token.
// Returns ("", "") if input does not start with "/".
func ParseInput(trimmedLine string) (name, args string) {
	if !strings.HasPrefix(trimmedLine, "/") {
		return "", ""
	}
	rest := trimmedLine[1:]
	idx := strings.IndexAny(rest, " \t")
	if idx < 0 {
		return normalizeName(rest), ""
	}
	return normalizeName(rest[:idx]), strings.TrimSpace(rest[idx+1:])
}

// Expand applies the $ARGUMENTS substitution rule to a template.
// If template contains $ARGUMENTS, all occurrences are replaced with args.
// If template does not contain $ARGUMENTS and args is non-empty, args is
// appended after two newlines.
// If args is empty and template has no $ARGUMENTS, template is returned as-is.
func Expand(template, args string) string {
	if strings.Contains(template, "$ARGUMENTS") {
		return strings.ReplaceAll(template, "$ARGUMENTS", args)
	}
	if args != "" {
		return template + "\n\n" + args
	}
	return template
}
