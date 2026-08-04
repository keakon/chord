package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/skill"
)

// SkillProvider exposes discovered skills, invoked skill state, and can load a skill's full content on demand.
type SkillProvider interface {
	ListSkills() []*skill.Meta
	InvokedSkills() []*skill.Meta
	MarkSkillInvoked(meta *skill.Meta)
	LoadSkill(name string) (*skill.Skill, error)
}

// SkillTool loads a skill's full instructions on demand.
type SkillTool struct {
	provider SkillProvider
}

func NewSkillTool(provider SkillProvider) *SkillTool {
	return &SkillTool{provider: provider}
}

type skillArgs struct {
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}

func (SkillTool) Name() string { return NameSkill }

func (SkillTool) Description() string {
	return "Load a skill's full instructions on demand when a task matches an available skill."
}

// Listing budget constants for the Available Skills section.
const (
	// SkillListingMaxTotal is the character budget for the Available Skills
	// section (not counting the preamble text shared by all tools).
	SkillListingMaxTotal = 4000
	// SkillListingMaxDescPerEntry is the per-skill description character budget.
	SkillListingMaxDescPerEntry = 160
	// SkillListingMaxEntries is the default max skills shown; overflow shows "+N more".
	SkillListingMaxEntries = 32
)

// TruncateSkillDesc truncates a skill description to fit the per-entry budget.
// The budget is expressed in bytes, so back off to a UTF-8 rune boundary to
// avoid emitting a half-encoded multi-byte character.
func TruncateSkillDesc(desc string) string {
	if len(desc) <= SkillListingMaxDescPerEntry {
		return desc
	}
	end := SkillListingMaxDescPerEntry - 3
	for end > 0 && !utf8.RuneStart(desc[end]) {
		end--
	}
	return desc[:end] + "..."
}

// SkillListingEntry is a lightweight name+description pair used by the shared
// listing builder.  Both tools.SkillTool and agent prompt blocks can use it.
type SkillListingEntry struct {
	Name, Desc string
}

// BuildSkillListing builds the Available Skills listing section with
// truncation budgets.  The header (e.g. "\n\n## Available Skills\n") is
// included in the total budget.  Returns empty string when no entries remain.
func BuildSkillListing(entries []SkillListingEntry, header string) string {
	if len(entries) == 0 {
		return ""
	}
	budget := max(SkillListingMaxTotal-len(header), 0)

	shown := 0
	var sb strings.Builder
	sb.WriteString(header)
	for i, e := range entries {
		if shown >= SkillListingMaxEntries {
			break
		}
		desc := TruncateSkillDesc(e.Desc)
		line := fmt.Sprintf("- **%s**: %s\n", e.Name, desc)
		if sb.Len()+len(line)-len(header) > budget && shown > 0 {
			break
		}
		sb.WriteString(line)
		shown = i + 1
	}
	remaining := len(entries) - shown
	if remaining > 0 {
		fmt.Fprintf(&sb, "+%d more skills available\n", remaining)
	}
	return sb.String()
}

func isListableSkill(sk *skill.Meta) bool {
	return sk != nil && sk.Discovered && strings.TrimSpace(sk.Name) != ""
}

func (t SkillTool) DescriptionForTools(_ map[string]struct{}) string {
	base := []string{
		"Load a skill's full instructions on demand when a task clearly matches an available skill.",
		"The loaded result includes the skill body plus the skill root directory so relative `scripts/`, `references/`, and `assets/` paths are unambiguous.",
		"Relative paths mentioned by a skill should be interpreted relative to the reported `<root>` directory.",
	}
	if t.provider == nil || len(t.provider.ListSkills()) == 0 {
		base = append(base, "No skills are currently available.")
	} else {
		base = append(base, "The available skills and their descriptions are listed in the system prompt's \"Available Skills\" section; when a task clearly matches one of them, call `skill` before proceeding.")
	}
	return strings.Join(base, " ")
}

func (SkillTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Name of the skill to load.",
			},
			"args": map[string]any{
				"type":        "string",
				"description": "Optional free-form arguments that the skill instructions may reference.",
			},
		},
		"required":             []string{"name"},
		"additionalProperties": false,
	}
}

func (SkillTool) IsReadOnly() bool { return true }

func (SkillTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func substituteSkillPlaceholders(content, rootDir, args string) string {
	content = strings.ReplaceAll(content, "${CHORD_SKILL_DIR}", rootDir)
	content = strings.ReplaceAll(content, "${CHORD_SKILL_ARGS}", args)
	return content
}

func (t SkillTool) IsAvailable() bool {
	if t.provider == nil {
		return false
	}
	return slices.ContainsFunc(t.provider.ListSkills(), isListableSkill)
}

func (t SkillTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a skillArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Name) == "" {
		return "", fmt.Errorf("name is required")
	}
	if t.provider == nil {
		return "", fmt.Errorf("skill provider not configured")
	}
	sk, err := t.provider.LoadSkill(a.Name)
	if err != nil {
		return "", err
	}
	if t.provider != nil {
		t.provider.MarkSkillInvoked(&sk.Meta)
	}

	var sb strings.Builder
	sb.WriteString("<skill>\n")
	fmt.Fprintf(&sb, "<name>%s</name>\n", sk.Name)
	fmt.Fprintf(&sb, "<path>%s</path>\n", sk.Location)
	fmt.Fprintf(&sb, "<root>%s</root>\n", sk.RootDir)
	fmt.Fprintf(&sb, "<relative_paths_base>%s</relative_paths_base>\n", sk.RootDir)
	if strings.TrimSpace(a.Args) != "" {
		fmt.Fprintf(&sb, "<args>%s</args>\n", a.Args)
	}
	fmt.Fprintf(&sb, "<notes>%s</notes>\n", "Relative paths from the skill content resolve against <root>. Read referenced files only when needed; do not guess other entry points if the skill already provides one.")
	sb.WriteString("\n")
	expandedContent := substituteSkillPlaceholders(sk.Content, sk.RootDir, a.Args)
	sb.WriteString(expandedContent)
	if !strings.HasSuffix(expandedContent, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("</skill>")
	return sb.String(), nil
}
