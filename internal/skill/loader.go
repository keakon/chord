package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// Package skill discovers and loads SKILL.md files from configurable directories.
//
// Skills are reusable capability packages that inject domain-specific instructions
// into the agent runtime. Discovery only needs lightweight metadata; the full
// markdown body is loaded on demand when the Skill tool is invoked.
//
// Scan order: project .chord/skills/ → project .agents/skills/ → global <config-home>/skills/ → extra paths.
// First occurrence of a skill name wins (earlier directories override later ones).

// Meta represents discovered skill metadata without loading the full body.
type Meta struct {
	Name         string   // from frontmatter
	Description  string   // from frontmatter
	Location     string   // absolute path to SKILL.md
	RootDir      string   // absolute path to the skill directory
	Discovered   bool     // true when present in the current metadata index
	Invoked      bool     // true when loaded via the Skill tool in the current session
	WhenToUse    string   // optional: guidance for when to use this skill
	ArgsHint     string   // optional: hint for skill arguments
	Context      string   // optional: "inline" (default) or "fork"
	Model        string   // optional: override model for fork context
	Effort       string   // optional: effort level for fork context
	AllowedTools []string // optional: tool allowlist for fork context
	Paths        []string // optional: conditional activation glob patterns
	Resources    []string // optional: files relative to the skill root the body depends on
	// DisableModelInvocation keeps the skill out of the model-facing catalog:
	// it never enters the Available Skills block or the skill tool listing, and
	// only the user's explicit /skill request can load it.
	DisableModelInvocation bool
}

// Skill represents a fully loaded skill definition.
type Skill struct {
	Meta
	Content string // markdown body (after frontmatter)
}

// frontmatter holds the YAML fields parsed from the SKILL.md header.
type frontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	WhenToUse    string   `yaml:"when_to_use"`
	ArgsHint     string   `yaml:"argument_hint"`
	Context      string   `yaml:"context"`
	Model        string   `yaml:"model"`
	Effort       string   `yaml:"effort"`
	AllowedTools []string `yaml:"allowed_tools"`
	Paths        []string `yaml:"paths"`
	Resources    []string `yaml:"resources"`
	// DisableModelInvocation is the ecosystem-compatible
	// `disable-model-invocation` key. It is a pointer so a sidecar can override
	// the frontmatter in both directions; nil means the key was not declared.
	DisableModelInvocation *bool `yaml:"disable-model-invocation"`
}

// Loader scans directories for SKILL.md files and loads them.
type Loader struct {
	dirs []string // directories to scan, in priority order (first wins)
}

// NewLoader creates a skill loader that will scan the given directories.
// Order matters: first occurrence of a skill name wins.
func NewLoader(dirs []string) *Loader {
	return &Loader{dirs: dirs}
}

// ScanMeta discovers skills from configured directories without loading their full content.
// Returns a deduplicated slice of skill metadata; first occurrence of a name wins.
// Directories that do not exist are silently skipped.
// Individual skill files that fail to parse are skipped with no error.
func (l *Loader) ScanMeta() ([]*Meta, error) {
	seen := make(map[string]struct{})
	var skills []*Meta

	for _, dir := range l.dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		matches, err := doublestar.Glob(os.DirFS(dir), "**/SKILL.md")
		if err != nil {
			continue
		}

		for _, match := range matches {
			fullPath := filepath.Join(dir, match)
			meta, err := LoadMeta(fullPath)
			if err != nil {
				continue // skip invalid skills
			}
			if _, exists := seen[meta.Name]; !exists {
				seen[meta.Name] = struct{}{}
				skills = append(skills, meta)
			}
		}
	}

	return skills, nil
}

// LoadMeta loads only a skill's metadata from a SKILL.md file.
func LoadMeta(path string) (*Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read skill %s: %w", path, err)
	}

	fm, _, err := parseFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse skill %s: %w", path, err)
	}
	if fm.Name == "" {
		return nil, fmt.Errorf("skill %s: missing name in frontmatter", path)
	}
	if fm.Description == "" {
		return nil, fmt.Errorf("skill %s: missing description in frontmatter", path)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	rootDir := filepath.Dir(absPath)

	meta := &Meta{
		Name:                   fm.Name,
		Description:            fm.Description,
		Location:               absPath,
		RootDir:                rootDir,
		WhenToUse:              fm.WhenToUse,
		ArgsHint:               fm.ArgsHint,
		Context:                normalizeContext(fm.Context),
		Model:                  fm.Model,
		Effort:                 fm.Effort,
		AllowedTools:           fm.AllowedTools,
		Paths:                  fm.Paths,
		Resources:              NormalizeResourceList(fm.Resources),
		DisableModelInvocation: fm.DisableModelInvocation != nil && *fm.DisableModelInvocation,
	}

	// Apply sidecar metadata if present (overrides frontmatter).
	loadSidecarMeta(rootDir, meta)

	return meta, nil
}

// normalizeContext returns "fork" when s is "fork" (case-insensitive),
// otherwise returns "inline" (the default context mode).
func normalizeContext(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "fork") {
		return "fork"
	}
	return "inline"
}

// LoadSkill loads a single skill from a SKILL.md file.
func LoadSkill(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read skill %s: %w", path, err)
	}

	fm, content, err := parseFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse skill %s: %w", path, err)
	}
	if fm.Name == "" {
		return nil, fmt.Errorf("skill %s: missing name in frontmatter", path)
	}
	if fm.Description == "" {
		return nil, fmt.Errorf("skill %s: missing description in frontmatter", path)
	}

	meta, err := LoadMeta(path)
	if err != nil {
		return nil, err
	}

	return &Skill{
		Meta:    *meta,
		Content: content,
	}, nil
}

// loadSidecarMeta reads an optional sidecar metadata file from the skill directory.
// If a file named chord.yaml (or agent.yaml) exists in the skill's rootDir,
// its fields are merged into the provided meta (sidecar overrides frontmatter).
func loadSidecarMeta(rootDir string, meta *Meta) {
	// Check both chord.yaml and agent.yaml as sidecar names.
	for _, name := range []string{"chord.yaml", "agent.yaml"} {
		sidecarPath := filepath.Join(rootDir, name)
		if _, err := os.Stat(sidecarPath); os.IsNotExist(err) {
			continue
		}
		data, err := os.ReadFile(sidecarPath)
		if err != nil {
			continue
		}
		var sidecar frontmatter
		if err := yaml.Unmarshal(data, &sidecar); err != nil {
			continue
		}
		if sidecar.WhenToUse != "" {
			meta.WhenToUse = sidecar.WhenToUse
		}
		if sidecar.ArgsHint != "" {
			meta.ArgsHint = sidecar.ArgsHint
		}
		if sidecar.Context != "" {
			meta.Context = normalizeContext(sidecar.Context)
		}
		if sidecar.Model != "" {
			meta.Model = sidecar.Model
		}
		if sidecar.Effort != "" {
			meta.Effort = sidecar.Effort
		}
		if len(sidecar.AllowedTools) > 0 {
			meta.AllowedTools = sidecar.AllowedTools
		}
		if len(sidecar.Paths) > 0 {
			meta.Paths = sidecar.Paths
		}
		if len(sidecar.Resources) > 0 {
			meta.Resources = NormalizeResourceList(sidecar.Resources)
		}
		if sidecar.DisableModelInvocation != nil {
			meta.DisableModelInvocation = *sidecar.DisableModelInvocation
		}
		break // first sidecar wins
	}
}

// parseFrontmatter extracts YAML frontmatter (between --- delimiters)
// and returns the parsed frontmatter struct and the remaining body content.
// If no frontmatter is found, returns an empty frontmatter and the full content.
func parseFrontmatter(data string) (frontmatter, string, error) {
	var fm frontmatter

	// Must start with "---" on its own line.
	if !strings.HasPrefix(data, "---") {
		return fm, data, nil
	}

	// Find the closing "---".
	rest := data[3:]
	// Normalize CRLF to LF so the closing delimiter search works on Windows.
	rest = strings.ReplaceAll(rest, "\r\n", "\n")
	// Skip the newline after opening ---
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}

	closingIdx := strings.Index(rest, "\n---")
	if closingIdx < 0 {
		// No closing delimiter found — treat as no frontmatter.
		return fm, data, nil
	}

	fmContent := rest[:closingIdx]
	// Body starts after the closing --- and its trailing newline.
	bodyStart := closingIdx + 4 // len("\n---")
	body := rest[bodyStart:]
	if len(body) > 0 && body[0] == '\n' {
		body = body[1:]
	} else if len(body) > 1 && body[0] == '\r' && body[1] == '\n' {
		body = body[2:]
	}

	if err := yaml.Unmarshal([]byte(fmContent), &fm); err != nil {
		return fm, data, fmt.Errorf("invalid frontmatter YAML: %w", err)
	}

	return fm, body, nil
}
