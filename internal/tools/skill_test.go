package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/skill"
)

type skillProviderStub struct {
	list           []*skill.Meta
	loaded         map[string]*skill.Skill
	visibleDigests map[string]bool
}

func (s skillProviderStub) HasCallableSkills() bool {
	for _, meta := range s.list {
		if meta != nil && meta.Discovered && strings.TrimSpace(meta.Name) != "" {
			return true
		}
	}
	return false
}

func (s skillProviderStub) LoadSkill(name string) (*skill.Skill, error) {
	if sk, ok := s.loaded[name]; ok {
		return sk, nil
	}
	return nil, context.Canceled
}

func (s skillProviderStub) InvokedSkills() []*skill.Meta {
	return nil
}

func (s skillProviderStub) MarkSkillInvoked(meta *skill.Meta) {}

func (s skillProviderStub) HasSkillBodyInContext(digest string) bool {
	return s.visibleDigests[digest]
}

func TestSkillToolIsAvailableRequiresListableSkills(t *testing.T) {
	tests := []struct {
		name string
		tool *SkillTool
		want bool
	}{
		{name: "no provider", tool: NewSkillTool(nil), want: false},
		{name: "empty list", tool: NewSkillTool(skillProviderStub{}), want: false},
		{name: "undiscovered skill", tool: NewSkillTool(skillProviderStub{list: []*skill.Meta{{Name: "hidden", Discovered: false}}}), want: false},
		{name: "empty name", tool: NewSkillTool(skillProviderStub{list: []*skill.Meta{{Name: " ", Discovered: true}}}), want: false},
		{name: "visible skill", tool: NewSkillTool(skillProviderStub{list: []*skill.Meta{{Name: "go-expert", Discovered: true}}}), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tool.IsAvailable(); got != tt.want {
				t.Fatalf("IsAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSkillToolDescriptionForToolsPointsAtSystemPromptListing(t *testing.T) {
	tool := NewSkillTool(skillProviderStub{
		list: []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Discovered: true}},
	})

	desc := tool.DescriptionForTools(nil)
	for _, want := range []string{
		"Load a skill's full instructions on demand",
		"startup catalog",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q in %q", want, desc)
		}
	}
	// The listing itself lives only in the system prompt's Available Skills
	// block; the tool description must not duplicate it.
	for _, unwanted := range []string{"## Available Skills", "go-expert"} {
		if strings.Contains(desc, unwanted) {
			t.Fatalf("description should not embed the skill listing, found %q in %q", unwanted, desc)
		}
	}
}

func TestSkillToolExecuteSubstitutesSkillPlaceholders(t *testing.T) {
	tool := NewSkillTool(skillProviderStub{
		list: []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Location: "/tmp/go-expert/SKILL.md", RootDir: "/tmp/go-expert", Discovered: true}},
		loaded: map[string]*skill.Skill{
			"go-expert": {
				Meta: skill.Meta{
					Name:        "go-expert",
					Description: "Go language development expert",
					Location:    "/tmp/go-expert/SKILL.md",
					RootDir:     "/tmp/go-expert",
				},
				Content: "Run `${CHORD_SKILL_DIR}/scripts/check.sh` with `${CHORD_SKILL_ARGS}`.",
			},
		},
	})

	got, err := tool.Execute(context.Background(), []byte(`{"name":"go-expert","args":"--fast"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{
		"<name>go-expert</name>",
		"<path>/tmp/go-expert/SKILL.md</path>",
		"<root>/tmp/go-expert</root>",
		"<relative_paths_base>/tmp/go-expert</relative_paths_base>",
		"<args>--fast</args>",
		"<notes>Relative paths from the skill content resolve against <root>. Read referenced files only when needed; do not guess other entry points if the skill already provides one.</notes>",
		"/tmp/go-expert/scripts/check.sh",
		"--fast",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "${CHORD_SKILL_DIR}") || strings.Contains(got, "${CHORD_SKILL_ARGS}") {
		t.Fatalf("placeholders should be substituted, got %q", got)
	}
}

func TestBuildStartupSkillListingTruncatesLongDescription(t *testing.T) {
	longDesc := strings.Repeat("A", SkillListingMaxDescCharsPerEntry*2)
	listing := BuildStartupSkillListing([]SkillListingEntry{{Name: "long-skill", Desc: longDesc}}, "## Available Skills\n")
	if !strings.Contains(listing, "long-skill") {
		t.Fatal("skill name should be present")
	}
	if strings.Contains(listing, strings.Repeat("A", SkillListingMaxDescCharsPerEntry)) {
		t.Fatal("description should be truncated")
	}
	if !strings.Contains(listing, strings.Repeat("A", SkillListingMaxDescCharsPerEntry-3)+"...") {
		t.Fatalf("expected truncated description ending with ..., got:\n%s", listing)
	}
}

func TestTruncateSkillDescCountsCharacters(t *testing.T) {
	// The per-entry budget counts characters, so a CJK description at the cap
	// passes through untouched even though its UTF-8 encoding is three times
	// larger, and truncation must not split a multi-byte character.
	atCap := strings.Repeat("厂", SkillListingMaxDescCharsPerEntry)
	if got := TruncateSkillDesc(atCap); got != atCap {
		t.Fatalf("description at the character cap should pass through, got %d characters", utf8.RuneCountInString(got))
	}

	got := TruncateSkillDesc(strings.Repeat("厂", SkillListingMaxDescCharsPerEntry+1))
	if !utf8.ValidString(got) {
		t.Fatalf("TruncateSkillDesc returned invalid UTF-8: %q", got)
	}
	want := strings.Repeat("厂", SkillListingMaxDescCharsPerEntry-3) + "..."
	if got != want {
		t.Fatalf("truncated description has %d characters, want a %d-character prefix plus \"...\"", utf8.RuneCountInString(got), SkillListingMaxDescCharsPerEntry-3)
	}
}

func TestBuildStartupSkillListingCapsAt32Entries(t *testing.T) {
	entries := make([]SkillListingEntry, 40)
	for i := range entries {
		entries[i] = SkillListingEntry{
			Name: fmt.Sprintf("skill-%02d", i),
			Desc: fmt.Sprintf("Description for skill %d", i),
		}
	}

	listing := BuildStartupSkillListing(entries, "## Available Skills\n")
	if !strings.Contains(listing, "8 additional skills") {
		t.Fatalf("expected overflow summary, got:\n%s", listing)
	}
	// The first 32 skills should be shown.
	if !strings.Contains(listing, "skill-00") {
		t.Fatal("first skill should be listed")
	}
	if !strings.Contains(listing, "skill-31") {
		t.Fatal("32nd skill should be listed")
	}
	if !strings.Contains(listing, "skill-32") {
		t.Fatal("omitted skill name must remain discoverable")
	}
}

func TestBuildStartupSkillListingRespectsTotalBudget(t *testing.T) {
	// Descriptions at the per-entry cap make the section budget bind well
	// before the entry cap does.
	entries := make([]SkillListingEntry, 100)
	for i := range entries {
		entries[i] = SkillListingEntry{
			Name: fmt.Sprintf("skill-%03d", i),
			Desc: strings.Repeat("X", SkillListingMaxDescCharsPerEntry),
		}
	}

	listing := BuildStartupSkillListing(entries, "## Available Skills\n")
	if listing == "" {
		t.Fatal("missing Available Skills listing")
	}
	if len(strings.Split(listing, "Additional available skill names:")[0]) > SkillListingMaxTotalBytes { // small tolerance for the overflow summary
		t.Fatalf("listing section too large: %d bytes", len(listing))
	}
	if shown := strings.Count(listing, "- name:"); shown >= SkillListingMaxEntries {
		t.Fatalf("section budget should cut entries below the %d-entry cap, got %d entries", SkillListingMaxEntries, shown)
	}
	if !strings.Contains(listing, "additional skills") {
		t.Fatal("expected overflow summary in listing")
	}
}

func TestSkillToolDescriptionDoesNotListSkillNames(t *testing.T) {
	tool := NewSkillTool(skillProviderStub{
		list: []*skill.Meta{
			{Name: "visible", Description: "Visible skill", Discovered: true},
			{Name: "hidden", Description: "Hidden skill", Discovered: false},
		},
	})
	desc := tool.DescriptionForTools(nil)
	for _, unwanted := range []string{"visible", "hidden"} {
		if strings.Contains(desc, unwanted) {
			t.Fatalf("skill %q should not appear in the tool description", unwanted)
		}
	}
}

func TestSkillToolDefinitionIndependentOfCatalog(t *testing.T) {
	empty := NewSkillTool(skillProviderStub{})
	full := NewSkillTool(skillProviderStub{list: []*skill.Meta{{Name: "conditional", Discovered: true, Paths: []string{"**"}}}})
	if empty.DescriptionForTools(nil) != full.DescriptionForTools(nil) {
		t.Fatal("description depends on catalog")
	}
	a, _ := json.Marshal(empty.Parameters())
	b, _ := json.Marshal(full.Parameters())
	if string(a) != string(b) || strings.Contains(string(a), "enum") {
		t.Fatal("schema depends on catalog")
	}
}
func TestSkillListingNamesBeyondPromptBudget(t *testing.T) {
	var entries []SkillListingEntry
	for i := range SkillListingMaxEntries + 5 {
		entries = append(entries, SkillListingEntry{Name: fmt.Sprintf("sample-%03d", i)})
	}
	listing := BuildStartupSkillListing(entries, "")
	if strings.Count(listing, "- name:") != SkillListingMaxEntries {
		t.Fatal("description entry limit violated")
	}
	for _, entry := range entries {
		if !strings.Contains(listing, entry.Name) {
			t.Fatalf("lost name %s", entry.Name)
		}
	}
}
func TestSkillListingOversizedFirstName(t *testing.T) {
	entries := []SkillListingEntry{{Name: strings.Repeat("X", 9000)}, {Name: "small"}}
	listing := BuildStartupSkillListing(entries, "")
	body, _, _ := strings.Cut(listing, "Additional available skill names:")
	if len(body) > SkillListingMaxTotalBytes || !strings.Contains(body, `"small"`) {
		t.Fatal("first oversized item bypassed budget or hid later item")
	}
}

func digestTestSkill(root string) *skill.Skill {
	return &skill.Skill{
		Name:     "go-expert",
		Location: root + "/SKILL.md",
		RootDir:  root,
		Content:  "Run `${CHORD_SKILL_DIR}/scripts/check.sh` with `${CHORD_SKILL_ARGS}`.\n",
	}
}

func TestSkillResultDigestIdentifiesFullBodies(t *testing.T) {
	root := t.TempDir()
	sk := digestTestSkill(root)
	full := FormatSkillInvocationResult(sk, "--fast")
	digest, ok := SkillResultBodyDigest(full)
	if !ok || digest == "" {
		t.Fatalf("full result should carry a body digest: %q", full)
	}

	same := FormatSkillInvocationResult(digestTestSkill(root), "--fast")
	if got, _ := SkillResultBodyDigest(same); got != digest {
		t.Fatalf("identical renderings should share the digest: %q != %q", got, digest)
	}
	// Different args change the placeholder expansion, and a different root
	// changes where relative references resolve: neither may reuse the body.
	for _, other := range []string{
		FormatSkillInvocationResult(sk, "--slow"),
		FormatSkillInvocationResult(digestTestSkill(t.TempDir()), "--fast"),
	} {
		if got, _ := SkillResultBodyDigest(other); got == digest {
			t.Fatalf("different rendering kept the digest %q", got)
		}
	}
	if _, ok := SkillResultBodyDigest(FormatSkillAlreadyLoadedResult(sk, "--fast")); ok {
		t.Fatal("the short already-loaded result must not look like a full body")
	}
}

func TestSkillToolExecuteShortCircuitsWhenIdenticalBodyIsVisible(t *testing.T) {
	root := t.TempDir()
	sk := digestTestSkill(root)
	digest, ok := SkillResultBodyDigest(FormatSkillInvocationResult(sk, ""))
	if !ok {
		t.Fatal("fixture result has no digest")
	}
	tool := NewSkillTool(skillProviderStub{
		loaded:         map[string]*skill.Skill{"go-expert": sk},
		visibleDigests: map[string]bool{digest: true},
	})

	got, err := tool.Execute(context.Background(), []byte(`{"name":"go-expert"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "<already_loaded>") {
		t.Fatalf("visible identical body should short-circuit, got %q", got)
	}
	if strings.Contains(got, "<body_sha256>") {
		t.Fatalf("short result must not carry a body digest: %q", got)
	}
	for _, want := range []string{"<name>go-expert</name>", "<root>" + root + "</root>", "<relative_paths_base>" + root + "</relative_paths_base>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("short result missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "scripts/check.sh") {
		t.Fatalf("short result must not repeat the body: %q", got)
	}

	// The digest belongs to one exact rendering: a call with different args
	// expands the placeholders differently and must return the fresh body.
	got, err = tool.Execute(context.Background(), []byte(`{"name":"go-expert","args":"--fast"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(got, "<already_loaded>") || !strings.Contains(got, "scripts/check.sh") {
		t.Fatalf("different args must return the full body, got %q", got)
	}
}
