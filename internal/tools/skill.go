package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/skill"
)

// SkillProvider exposes discovered skills, invoked skill state, and can load a skill's full content on demand.
type SkillProvider interface {
	HasCallableSkills() bool
	InvokedSkills() []*skill.Meta
	MarkSkillInvoked(meta *skill.Meta)
	LoadSkill(name string) (*skill.Skill, error)
	// HasSkillBodyInContext reports whether a full skill result carrying the
	// given body digest is still part of the request the model will see. The
	// tool uses it to answer a repeated load with the envelope alone instead of
	// a second copy of instructions that are already in front of the model.
	HasSkillBodyInContext(digest string) bool
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

// skillToolLoadContract is the tool's load contract and the first sentence of
// its full description. The short Description() and DescriptionForTools share
// this constant so the tool_search index (which reads the short text) and the
// model-visible long text cannot drift apart.
const skillToolLoadContract = "Load a skill's full instructions on demand when a task clearly matches an available skill."

func (SkillTool) Description() string {
	return skillToolLoadContract
}

// Listing budget constants for the Available Skills section.  The section
// budget counts bytes because it bounds prompt-prefix cost; the per-entry
// description budget counts characters so multi-byte text is not penalized.
const (
	// SkillListingMaxTotalBytes is the byte budget for the Available Skills
	// section (not counting the preamble text shared by all tools).
	SkillListingMaxTotalBytes = 8192
	// SkillListingMaxDescCharsPerEntry is the per-skill description budget in
	// characters, including the "..." appended on truncation.
	SkillListingMaxDescCharsPerEntry = 1024
	// SkillListingMaxEntries is the default max skills shown; overflow shows "+N more".
	SkillListingMaxEntries = 32
)

// TruncateSkillDesc truncates a skill description to fit the per-entry budget.
// The budget counts characters and includes the ellipsis, so an overlong
// description keeps SkillListingMaxDescCharsPerEntry-3 characters.
func TruncateSkillDesc(desc string) string {
	return TruncateSkillDescription(desc, SkillListingMaxDescCharsPerEntry)
}

// TruncateSkillDescription preserves UTF-8 while bounding Unicode rune count.
func TruncateSkillDescription(desc string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(desc) <= limit {
		return desc
	}
	runes := []rune(desc)
	if limit < 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

// SkillListingEntry is a lightweight name+description pair used by the shared
// listing builder.  Both tools.SkillTool and agent prompt blocks can use it.
type SkillListingEntry struct {
	Name, Desc string
}

// BuildStartupSkillListing builds the Available Skills listing section with
// truncation budgets.  The header (e.g. "\n\n## Available Skills\n") is
// included in the total budget, which is measured in bytes.  Returns empty
// string when no entries remain.
func BuildStartupSkillListing(entries []SkillListingEntry, header string) string {
	if len(entries) == 0 {
		return ""
	}
	reserve := len(fmt.Sprintf("%d additional skills are listed by name below.\n", len(entries)))
	var body strings.Builder
	if len(header)+reserve > SkillListingMaxTotalBytes {
		header = ""
	}
	body.WriteString(header)
	var omitted []string
	shown := 0
	for _, entry := range entries {
		name, _ := json.Marshal(entry.Name)
		desc, _ := json.Marshal(TruncateSkillDesc(entry.Desc))
		line := fmt.Sprintf("- name: %s; description: %s\n", name, desc)
		if shown >= SkillListingMaxEntries || body.Len()+len(line)+reserve > SkillListingMaxTotalBytes {
			omitted = append(omitted, entry.Name)
			continue
		}
		body.WriteString(line)
		shown++
	}
	if len(omitted) > 0 {
		fmt.Fprintf(&body, "%d additional skills are listed by name below.\n", len(omitted))
		slices.Sort(omitted)
		names, _ := json.Marshal(omitted)
		body.WriteString("Additional available skill names: ")
		body.Write(names)
		body.WriteByte('\n')
	}
	return body.String()
}

func (t SkillTool) DescriptionForTools(_ map[string]struct{}) string {
	base := []string{
		skillToolLoadContract,
		"The loaded result includes the skill body plus the skill root directory so relative `scripts/`, `references/`, and `assets/` paths are unambiguous.",
		"Relative paths mentioned by a skill should be interpreted relative to the reported `<root>` directory.",
	}
	base = append(base, "Skill metadata is provided in the startup catalog or in optional path discovery notices.")
	return strings.Join(base, " ")
}

func (t SkillTool) Parameters() map[string]any {
	name := map[string]any{
		"type":        "string",
		"description": "Name of the skill to load.",
	}

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": name,
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

// ConcurrencyPolicy keeps a skill load in the read-only class: it reads skill
// files and mutates nothing, so it must not serialize a turn.
func (SkillTool) ConcurrencyPolicy(json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{Resource: "tool:skill", Mode: ConcurrencyModeRead}
}

func substituteSkillPlaceholders(content, rootDir, args string) string {
	content = strings.ReplaceAll(content, "${CHORD_SKILL_DIR}", rootDir)
	content = strings.ReplaceAll(content, "${CHORD_SKILL_ARGS}", args)
	return content
}

// SkillCallArguments marshals the arguments of a skill tool call. A
// user-triggered load records its call through this helper so the persisted
// pair stays byte-shaped like a model-produced one.
func SkillCallArguments(name, args string) (json.RawMessage, error) {
	return json.Marshal(skillArgs{Name: name, Args: args})
}

// skillBodyDigestTag identifies the full-body fingerprint every complete skill
// result carries. It is the tool's own identity marker: requesttrim summaries
// keep only the envelope anchors and heading outline, so a summarized copy has
// no digest and can never be mistaken for the loaded body.
const skillBodyDigestTag = "body_sha256"

// skillBodyDigest fingerprints everything a full skill result shows the model:
// the envelope identity, the placeholder-expanded body, and the resource
// warning block. Two loads that render different bytes get different digests,
// so a load for another root or another `${CHORD_SKILL_ARGS}` substitution
// always returns the fresh body instead of the earlier one.
func skillBodyDigest(name, location, root, args, warning, expandedContent string) string {
	h := sha256.New()
	for _, part := range []string{name, location, root, args, warning, expandedContent} {
		_, _ = io.WriteString(h, part)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SkillResultBodyDigest reads the fingerprint out of a complete skill result.
// It reports false for anything that is not a full body — a summarized copy, a
// short "already loaded" result, or a failed load.
func SkillResultBodyDigest(result string) (string, bool) {
	value, ok := skillResultField(result, skillBodyDigestTag)
	if !ok || len(value) != sha256.Size*2 {
		return "", false
	}
	return value, true
}

// skillResultField reads one field out of a skill result envelope. The
// envelope's fields precede the body, so the first occurrence is the field; a
// body that mentions the same tag later is left alone.
func skillResultField(result, tag string) (string, bool) {
	_, rest, ok := strings.Cut(result, "<"+tag+">")
	if !ok {
		return "", false
	}
	value, _, ok := strings.Cut(rest, "</"+tag+">")
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

// FormatSkillInvocationResult renders the tool result of a skill load: the
// envelope the model reads, including the declared-resource warning block. The
// model path and the user's explicit load both go through it, so both origins
// produce one identical result shape — that equivalence is what lets restore,
// compaction and the TUI treat a user-triggered load exactly like a model call.
func FormatSkillInvocationResult(sk *skill.Skill, args string) string {
	if sk == nil {
		return ""
	}
	warning := SkillDeclaredResourceWarningBlock(sk.RootDir, sk.Meta.Resources)
	expandedContent := substituteSkillPlaceholders(sk.Content, sk.RootDir, args)
	digest := skillBodyDigest(sk.Name, sk.Location, sk.RootDir, strings.TrimSpace(args), warning, expandedContent)
	var sb strings.Builder
	sb.WriteString("<skill>\n")
	fmt.Fprintf(&sb, "<name>%s</name>\n", sk.Name)
	fmt.Fprintf(&sb, "<path>%s</path>\n", sk.Location)
	fmt.Fprintf(&sb, "<root>%s</root>\n", sk.RootDir)
	fmt.Fprintf(&sb, "<relative_paths_base>%s</relative_paths_base>\n", sk.RootDir)
	fmt.Fprintf(&sb, "<%s>%s</%s>\n", skillBodyDigestTag, digest, skillBodyDigestTag)
	if strings.TrimSpace(args) != "" {
		fmt.Fprintf(&sb, "<args>%s</args>\n", args)
	}
	fmt.Fprintf(&sb, "<notes>%s</notes>\n", skillInvocationNotes)
	sb.WriteString("\n")
	if warning != "" {
		sb.WriteString(warning)
		sb.WriteString("\n")
	}
	sb.WriteString(expandedContent)
	if !strings.HasSuffix(expandedContent, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("</skill>")
	return sb.String()
}

// FormatSkillAlreadyLoadedResult renders the answer to a load whose identical
// body is still in the request face: the envelope anchors keep the workflow's
// paths resolvable, while the body stays the earlier copy. It deliberately
// carries no body digest — a digest would make this result look like a full
// body to a later visibility check, and repeated-output detection would then
// collapse the real copy as the duplicate.
func FormatSkillAlreadyLoadedResult(sk *skill.Skill, args string) string {
	if sk == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<skill>\n")
	fmt.Fprintf(&sb, "<name>%s</name>\n", sk.Name)
	fmt.Fprintf(&sb, "<path>%s</path>\n", sk.Location)
	fmt.Fprintf(&sb, "<root>%s</root>\n", sk.RootDir)
	fmt.Fprintf(&sb, "<relative_paths_base>%s</relative_paths_base>\n", sk.RootDir)
	if strings.TrimSpace(args) != "" {
		fmt.Fprintf(&sb, "<args>%s</args>\n", args)
	}
	fmt.Fprintf(&sb, "<already_loaded>%s</already_loaded>\n", skillAlreadyLoadedNote)
	sb.WriteString("</skill>")
	return sb.String()
}

const skillAlreadyLoadedNote = "The full instructions were already loaded earlier in this conversation and are still visible; use that copy instead of loading them again."

const skillInvocationNotes = "Relative paths from the skill content resolve against <root>. Read referenced files only when needed; do not guess other entry points if the skill already provides one."

// SkillResourceWarningOpen and SkillResourceWarningClose delimit the
// machine-readable resource warning block prepended to a skill body when
// declared resources are missing or empty. The block sits before the body so
// tests can assert the body itself is untouched, and the TUI parses the same
// block for its card marker instead of re-statting files.
const (
	SkillResourceWarningOpen  = "<skill_resource_warnings>"
	SkillResourceWarningClose = "</skill_resource_warnings>"
)

// SkillResourceWarningBlock formats failed and warning resource entries as a
// standalone block. It returns "" when every entry passes, so callers can
// keep the skill output byte-identical to the body in the healthy case.
func SkillResourceWarningBlock(entries []skill.ResourceEntry) string {
	var lines []string
	for _, entry := range entries {
		if entry.Status != skill.ResourceStatusFailed && entry.Status != skill.ResourceStatusWarning {
			continue
		}
		reason := entry.Reason
		if reason == "" {
			reason = entry.Status
		}
		line := "- " + entry.Declared + ": " + reason
		if detail := strings.TrimSpace(entry.Detail); detail != "" {
			line += ": " + detail
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return SkillResourceWarningOpen + "\n" + strings.Join(lines, "\n") + "\n" + SkillResourceWarningClose
}

// SkillDeclaredResourceWarningBlock checks declared resources under rootDir
// and formats any problems. It performs read-only stats only.
func SkillDeclaredResourceWarningBlock(rootDir string, resources []string) string {
	if len(resources) == 0 {
		return ""
	}
	return SkillResourceWarningBlock(skill.CheckDeclaredResources(rootDir, resources))
}

// ExtractSkillResourceWarningLines returns the inner lines of the warning
// block embedded in a skill result or body, or nil when absent.
func ExtractSkillResourceWarningLines(s string) []string {
	_, rest, ok := strings.Cut(s, SkillResourceWarningOpen)
	if !ok {
		return nil
	}
	inner, _, ok := strings.Cut(rest, SkillResourceWarningClose)
	if !ok {
		return nil
	}
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return nil
	}
	lines := strings.Split(inner, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// StripSkillResourceWarningBlock removes the warning block from a skill body,
// leaving the original content intact for display and assertions.
func StripSkillResourceWarningBlock(s string) string {
	before, rest, ok := strings.Cut(s, SkillResourceWarningOpen)
	if !ok {
		return s
	}
	_, after, ok := strings.Cut(rest, SkillResourceWarningClose)
	if !ok {
		return s
	}
	cleaned := before + after
	return strings.Trim(cleaned, "\n")
}

// SkillResourceWarningSummary condenses a skill result warning into one line
// for collapsed cards, or "" when there is no warning.
func SkillResourceWarningSummary(s string) string {
	lines := ExtractSkillResourceWarningLines(s)
	if len(lines) == 0 {
		return ""
	}
	if len(lines) == 1 {
		return "resources: 1 issue: " + strings.TrimPrefix(lines[0], "- ")
	}
	return fmt.Sprintf("resources: %d issues (see expanded)", len(lines))
}

// FormatSkillBodyForDisplay replaces the raw warning block with a readable
// section above the clean body. Bodies without warnings pass through.
func FormatSkillBodyForDisplay(body string) string {
	lines := ExtractSkillResourceWarningLines(body)
	if len(lines) == 0 {
		return body
	}
	clean := strings.TrimSpace(StripSkillResourceWarningBlock(body))
	var sb strings.Builder
	sb.WriteString("Skill resources:\n")
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if clean != "" {
		sb.WriteString("\n")
		sb.WriteString(clean)
	}
	return sb.String()
}

func (t SkillTool) IsAvailable() bool {
	if t.provider == nil {
		return false
	}
	return t.provider.HasCallableSkills()
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
	t.provider.MarkSkillInvoked(&sk.Meta)
	rendered := FormatSkillInvocationResult(sk, a.Args)
	if digest, ok := SkillResultBodyDigest(rendered); ok && t.provider.HasSkillBodyInContext(digest) {
		return FormatSkillAlreadyLoadedResult(sk, a.Args), nil
	}
	return rendered, nil
}
