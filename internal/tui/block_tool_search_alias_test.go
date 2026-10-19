package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

// A model that reaches for glob's plural spelling sends grep a "patterns" list
// instead of the singular "pattern" the schema declares. The executor decodes
// it (and reports it as consumed, never as ignored), so the card must render the
// pattern it searched with: the header used to fall back to a bare
// "4 matches · 1 file", leaving the query invisible.
func TestGrepHeaderRendersToleratedPluralPatternsAlias(t *testing.T) {
	args := `{"patterns":["alpha|beta","gamma"],"paths":["docs/plan.md"]}`
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "grep",
		Content:    args,
		RawArgs:    args,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"docs/plan.md:471:alpha",
			"docs/plan.md:550:beta",
			"docs/plan.md:635:gamma",
			"docs/plan.md:636:gamma",
		}, "\n"),
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(joined, "grep (?:alpha|beta)|(?:gamma) (paths=docs/plan.md) · 4 matches · 1 file") {
		t.Fatalf("expected the alias to render as the searched pattern; got:\n%s", joined)
	}
	if strings.Contains(joined, "ignored patterns") {
		t.Fatalf("a consumed alias must not render as a discarded argument; got:\n%s", joined)
	}
}

// The tolerated singular aliases (path for paths, glob for includes) take
// effect too, so the option group must name them the way the executor decoded
// them instead of dropping the search scope.
func TestGrepHeaderRendersToleratedSingularAliases(t *testing.T) {
	args := `{"pattern":"TODO","path":"internal","glob":"**/*.go"}`
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "grep",
		Content:    args,
		RawArgs:    args,
		ResultDone: true,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(joined, "grep TODO (paths=internal, includes=**/*.go)") {
		t.Fatalf("expected the singular aliases in the option group; got:\n%s", joined)
	}
}

// Glob tolerates the legacy singular "pattern" field; the card must show the
// patterns it searched with rather than a bare result count.
func TestGlobHeaderRendersToleratedSingularPatternAlias(t *testing.T) {
	args := `{"pattern":"**/*.go","path":"internal"}`
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "glob",
		Content:    args,
		RawArgs:    args,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"internal/a.go",
			"internal/b.go",
		}, "\n"),
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(joined, "glob **/*.go (path=internal) · 2 files") {
		t.Fatalf("expected the legacy pattern field to render; got:\n%s", joined)
	}
}

// The audit guard is per field: an unrelated discarded argument must not stop
// the header from recovering the pattern a consumed alias carried.
func TestGrepHeaderKeepsAliasPatternWithUnrelatedArgDiagnostics(t *testing.T) {
	args := `{"patterns":["alpha"],"paths":["docs"],"bogus":"x"}`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "grep",
		Content:       args,
		RawArgs:       args,
		ResultDone:    true,
		ResultContent: "docs/a.md:1:alpha",
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  args,
			EffectiveArgsJSON: `{"pattern":"alpha","paths":["docs"]}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.bogus",
				ValueJSON: `"x"`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(joined, "grep alpha") {
		t.Fatalf("expected the consumed alias pattern to survive an unrelated diagnostic; got:\n%s", joined)
	}
	if !strings.Contains(joined, "ignored bogus") {
		t.Fatalf("expected the discarded argument to keep its own rendering; got:\n%s", joined)
	}
}

// A search card with no subject still names what the call searched: the option
// group must survive the count summary instead of being dropped along with the
// empty pattern slot.
func TestAppendSearchHeaderSummaryKeepsOptionsWithoutSubject(t *testing.T) {
	wide := terminaltext.StripANSI(appendSearchHeaderSummary("grep", "", "(paths=docs/plan.md)", "4 matches · 1 file", 80))
	if wide != "grep (paths=docs/plan.md) · 4 matches · 1 file" {
		t.Fatalf("wide header = %q", wide)
	}
	narrow := terminaltext.StripANSI(appendSearchHeaderSummary("grep", "", "(paths=docs/plan.md)", "4 matches · 1 file", 30))
	if narrow != "grep 4 matches · 1 file" {
		t.Fatalf("narrow header = %q, want the summary alone when the group cannot fit", narrow)
	}
	// Neither extreme above reaches the boundary where the option group is
	// truncated to exactly the width the summary leaves. Pin it: the rendered
	// header must fill the requested width there, so a budget that reserves one
	// column too many turns into a missing column instead of passing silently.
	const gray = "(paths=docs/plan.md)"
	const summary = "4 matches · 1 file"
	for _, width := range []int{41, 42, 43} {
		got := terminaltext.StripANSI(appendSearchHeaderSummary("grep", "", gray, summary, width))
		if !strings.Contains(got, "paths=") {
			t.Fatalf("width %d dropped the option group: %q", width, got)
		}
		if rendered := ansi.StringWidth(got); rendered != width {
			t.Fatalf("width %d rendered %d columns, want the full budget: %q", width, rendered, got)
		}
	}
}

// The header decoder reads b.Content first, then falls back to b.RawArgs when
// Content is empty. A block that only carries RawArgs must still recover the
// alias-spelled pattern, or the streamed-and-finalized card loses its query.
func TestGrepHeaderRecoversAliasFromRawArgsWhenContentEmpty(t *testing.T) {
	args := `{"patterns":["alpha|beta"],"path":"internal"}`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "grep",
		RawArgs:       args,
		ResultDone:    true,
		ResultContent: "internal/a.go:1:alpha",
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(joined, "grep alpha|beta (paths=internal)") {
		t.Fatalf("expected the alias recovered from RawArgs; got:\n%s", joined)
	}
}

// When the audit already reports the alias field itself as ignored, the header
// must not recover a value from it: the discarded argument owns that spelling.
func TestGrepHeaderSkipsAliasClaimedByAudit(t *testing.T) {
	args := `{"patterns":["alpha"],"paths":["docs"]}`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "grep",
		Content:       args,
		RawArgs:       args,
		ResultDone:    true,
		ResultContent: "docs/a.md:1:alpha",
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  args,
			EffectiveArgsJSON: `{"paths":["docs"]}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.patterns",
				ValueJSON: `"alpha"`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(joined, "ignored patterns=") || !strings.Contains(joined, "alpha") {
		t.Fatalf("expected the claimed alias to keep its diagnostic; got:\n%s", joined)
	}
	if strings.Contains(joined, "grep alpha") {
		t.Fatalf("claimed alias must not be recovered as the pattern; got:\n%s", joined)
	}
}

// claimedHeaderAliasPaths maps every audited path to its canonical name so the
// recovery loop leaves already-reported fields to the diagnostic renderer.
func TestClaimedHeaderAliasPathsMapsAuditPaths(t *testing.T) {
	b := &Block{
		Type: BlockToolCall,
		Audit: &message.ToolArgsAudit{
			IgnoredArgs: []message.IgnoredToolArg{
				{Path: "args.pattern", ValueJSON: `"alpha"`},
				{Path: "args.path", ValueJSON: `"internal"`},
			},
			InvalidArgs: []message.InvalidToolArg{
				{Path: "args.glob", Reason: message.InvalidToolArgReasonMissing},
			},
		},
	}
	claimed := b.claimedHeaderAliasPaths()
	for _, want := range []string{"pattern", "path", "glob"} {
		if !claimed[want] {
			t.Fatalf("claimed[%q] = false, want true", want)
		}
	}
	if b.claimedHeaderAliasPaths() == nil {
		t.Fatal("an audit must yield a non-nil claimed map")
	}
	noAudit := (&Block{Type: BlockToolCall}).claimedHeaderAliasPaths()
	if noAudit != nil {
		t.Fatalf("expected nil when there is no audit, got %v", noAudit)
	}
}

// When the model writes a tolerated alias beside the canonical key it renames
// to, the executor keeps the canonical value and drops the alias, so the header
// must name the scope once. grep's option group is the observable case: it
// rebuilds from the effective paths and the raw "path" spelling, so a redundant
// alias used to appear as "path=..." next to "paths=...".
func TestGrepHeaderDropsAliasBesideCanonicalKey(t *testing.T) {
	args := `{"pattern":"TODO","patterns":["TODO"],"paths":["internal"],"path":"internal"}`
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "grep",
		Content:    args,
		RawArgs:    args,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"internal/a.go:1:TODO",
			"internal/b.go:2:TODO",
		}, "\n"),
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(joined, "grep TODO (paths=internal)") {
		t.Fatalf("expected the canonical scope once; got:\n%s", joined)
	}
	if got := strings.Count(joined, "=internal"); got != 1 {
		t.Fatalf("expected one scope for internal, got %d; header:\n%s", got, joined)
	}
}
