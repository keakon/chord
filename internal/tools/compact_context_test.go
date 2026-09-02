package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func testCompactValidator() CompactContextValidator {
	return CompactContextValidator{ContinuationStateMaxTokens: 2048}
}

func validCompactArgs() string {
	return `{
		"active_objective": "land the model-driven context reset",
		"completed": ["refactored the trigger type", "wrote the tool"],
		"decisions": ["keep the tool MainAgent-only"],
		"open_issues": ["gateway event contract"],
		"next_step": "run the agent tests",
		"state_files": ["internal/agent/compaction_model_driven.go"]
	}`
}

func TestCompactContextParseValidArgs(t *testing.T) {
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(validCompactArgs()))
	if err != nil {
		t.Fatalf("ParseCompactContextArgs: %v", err)
	}
	if args.ActiveObjective != "land the model-driven context reset" {
		t.Fatalf("active_objective = %q", args.ActiveObjective)
	}
	if len(args.StateFiles) != 1 || args.StateFiles[0] != "internal/agent/compaction_model_driven.go" {
		t.Fatalf("state_files = %#v", args.StateFiles)
	}
}

func TestCompactContextMissingRequired(t *testing.T) {
	v := testCompactValidator()
	for name, raw := range map[string]string{
		"missing_objective": `{"next_step": "x"}`,
		"missing_next":      `{"active_objective": "x"}`,
		"blank_objective":   `{"active_objective": "   ", "next_step": "x"}`,
		"blank_next":        `{"active_objective": "x", "next_step": ""}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
				t.Fatal("expected error for invalid required fields")
			}
		})
	}
}

func TestCompactContextTrimsFields(t *testing.T) {
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(`{
		"active_objective": "  trimmed goal  ",
		"completed": ["  done item  "],
		"next_step": "  next  "
	}`))
	if err != nil {
		t.Fatalf("ParseCompactContextArgs: %v", err)
	}
	if args.ActiveObjective != "trimmed goal" {
		t.Fatalf("active_objective not trimmed: %q", args.ActiveObjective)
	}
	if len(args.Completed) != 1 || args.Completed[0] != "done item" {
		t.Fatalf("completed not trimmed: %#v", args.Completed)
	}
}

func TestCompactContextStateFilesLexicalValidation(t *testing.T) {
	v := testCompactValidator()
	for name, raw := range map[string]string{
		"empty":        `{"active_objective":"a","next_step":"b","state_files":[""]}`,
		"absolute":     `{"active_objective":"a","next_step":"b","state_files":["/etc/passwd"]}`,
		"dotdot":       `{"active_objective":"a","next_step":"b","state_files":["../secret.md"]}`,
		"inner_dotdot": `{"active_objective":"a","next_step":"b","state_files":["a/../../secret.md"]}`,
		"dot_segment":  `{"active_objective":"a","next_step":"b","state_files":["a/./b.md"]}`,
		"backslash":    `{"active_objective":"a","next_step":"b","state_files":["a\\b.md"]}`,
		"tilde":        `{"active_objective":"a","next_step":"b","state_files":["~/secret.md"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
				t.Fatalf("expected %q to be rejected", raw)
			}
		})
	}
}

func TestCompactContextStateFilesRejectionGuidesRemediation(t *testing.T) {
	// Absolute and home-relative paths have no guaranteed workspace-relative
	// spelling, so the rejection must tell the model what to do instead
	// (rewrite when in-project, otherwise fold the state into text fields).
	v := testCompactValidator()
	for name, raw := range map[string]string{
		"absolute": `{"active_objective":"a","next_step":"b","state_files":["/tmp/scratch"]}`,
		"tilde":    `{"active_objective":"a","next_step":"b","state_files":["~/scratch"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := v.ParseCompactContextArgs(json.RawMessage(raw))
			if err == nil {
				t.Fatal("expected rejection")
			}
			msg := err.Error()
			for _, want := range []string{"workspace-relative", "docs/usage.md", "completed/decisions/open_issues text"} {
				if !strings.Contains(msg, want) {
					t.Fatalf("error %q is missing remediation guidance %q", msg, want)
				}
			}
		})
	}
}

func TestCompactContextStateFilesDedupedAndNormalized(t *testing.T) {
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(`{
		"active_objective":"a","next_step":"b",
		"state_files":["docs/x.md","docs//x.md","docs/x.md","other.md"]
	}`))
	if err != nil {
		t.Fatalf("ParseCompactContextArgs: %v", err)
	}
	if len(args.StateFiles) != 2 || args.StateFiles[0] != "docs/x.md" || args.StateFiles[1] != "other.md" {
		t.Fatalf("state_files = %#v, want deduped [docs/x.md other.md]", args.StateFiles)
	}
}

func TestCompactContextTokenBudgetRejectsOversized(t *testing.T) {
	v := CompactContextValidator{ContinuationStateMaxTokens: 10}
	_, err := v.ParseCompactContextArgs(json.RawMessage(`{
		"active_objective": "` + strings.Repeat("x", 200) + `",
		"next_step": "y"
	}`))
	if err == nil {
		t.Fatal("expected token budget rejection")
	}
	if !strings.Contains(err.Error(), "token budget") {
		t.Fatalf("error = %q, want token budget mention", err)
	}
}

// The budget must be language-independent: Chinese text is roughly 3x the
// byte density of ASCII, so a rune-based cap would admit ~3x the tokens. The
// token estimator must reject both at the same token threshold.
func TestCompactContextTokenBudgetSameForChineseAndEnglish(t *testing.T) {
	english := strings.Repeat("abcdefghij", 60) // 600 bytes -> 200 tokens bytes/3
	chinese := strings.Repeat("中文状态文本", 60)     // 6 runes * 3 bytes * 60 = 1080 bytes -> 360 tokens bytes/3
	if len(english)/3 == len(chinese)/3 {
		t.Fatal("fixture sizes must differ in bytes")
	}
	v := CompactContextValidator{ContinuationStateMaxTokens: 300}
	for name, text := range map[string]string{"english": english, "chinese": chinese} {
		raw := `{"active_objective":"` + text + `","next_step":"y"}`
		_, err := v.ParseCompactContextArgs(json.RawMessage(raw))
		if name == "english" {
			if err != nil {
				t.Fatalf("english under budget rejected: %v", err)
			}
		} else if err == nil {
			t.Fatal("chinese over budget accepted; rune-based cap would admit ~3x tokens")
		}
	}
}

func TestCompactContextTokenBudgetIncludesStateFiles(t *testing.T) {
	// state_files paths are model-authored text too; they must count against
	// the continuation-state budget so a large path list cannot slip past the
	// evidence-tier cap.
	v := CompactContextValidator{ContinuationStateMaxTokens: 30}
	raw := `{"active_objective":"a","next_step":"b","state_files":["` + strings.Repeat("p", 60) + `/x.md","` + strings.Repeat("q", 60) + `/y.md"]}`
	_, err := v.ParseCompactContextArgs(json.RawMessage(raw))
	if err == nil {
		t.Fatal("expected token budget rejection including state_files")
	}
	if !strings.Contains(err.Error(), "token budget") {
		t.Fatalf("error = %q, want token budget mention", err)
	}
}

func TestCompactContextRejectsEmptyListItems(t *testing.T) {
	v := testCompactValidator()
	for name, raw := range map[string]string{
		"completed_blank":   `{"active_objective":"a","next_step":"b","completed":["  " ]}`,
		"decisions_blank":   `{"active_objective":"a","next_step":"b","decisions":[""]}`,
		"open_issues_blank": `{"active_objective":"a","next_step":"b","open_issues":["ok",""]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
				t.Fatal("expected empty list item to be rejected")
			}
		})
	}
}

func TestCompactContextRejectsListOverMaxItems(t *testing.T) {
	v := testCompactValidator()
	items := `["a","b","c","d","e","f","g","h","i","j","k","l","m","n"]` // 14 > 12
	raw := `{"active_objective":"a","next_step":"b","completed":` + items + `}`
	if _, err := v.ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
		t.Fatal("expected maxItems rejection for completed")
	}
	stateFiles := `["a.md","b.md","c.md","d.md","e.md","f.md","g.md","h.md","i.md","j.md","k.md","l.md","m.md","n.md","o.md","p.md","q.md"]` // 17 > 16
	raw = `{"active_objective":"a","next_step":"b","state_files":` + stateFiles + `}`
	if _, err := v.ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
		t.Fatal("expected maxItems rejection for state_files")
	}
}

func TestCompactContextRejectsFieldOverMaxLength(t *testing.T) {
	v := testCompactValidator()
	long := strings.Repeat("x", 1001)
	raw := `{"active_objective":"` + long + `","next_step":"b"}`
	if _, err := v.ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
		t.Fatal("expected maxLength rejection for active_objective")
	}
	longItem := strings.Repeat("y", 501)
	raw = `{"active_objective":"a","next_step":"b","completed":["` + longItem + `"]}`
	if _, err := v.ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
		t.Fatal("expected maxLength rejection for completed item")
	}
}

func TestCompactContextRejectsControlCharsInStateFiles(t *testing.T) {
	v := testCompactValidator()
	for name, path := range map[string]string{
		"newline":  "a.md\n## forged heading",
		"tab":      "a\tb.md",
		"carriage": "a\rb.md",
	} {
		t.Run(name, func(t *testing.T) {
			raw := `{"active_objective":"a","next_step":"b","state_files":["` + path + `"]}`
			if _, err := v.ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
				t.Fatal("expected control-character rejection in state_files")
			}
		})
	}
}

func TestCompactContextExecuteAcceptsWithoutSideEffects(t *testing.T) {
	tool := NewCompactContextTool(testCompactValidator())
	result, err := tool.Execute(context.Background(), json.RawMessage(validCompactArgs()))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "accepted") {
		t.Fatalf("result = %q, want accepted wording", result)
	}
}

func TestCompactContextExecuteRejectsInvalid(t *testing.T) {
	tool := NewCompactContextTool(testCompactValidator())
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"state_files":["/etc/passwd"]}`)); err == nil {
		t.Fatal("expected rejection for absolute state path")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected rejection for missing required fields")
	}
}

func TestCompactContextDescriptionStatesBudget(t *testing.T) {
	// The combined budget is the binding limit and is several times smaller
	// than the per-field character caps add up to, so the description must
	// state it up front; a validator without a budget must not invent one.
	tool := NewCompactContextTool(CompactContextValidator{ContinuationStateMaxTokens: 2048})
	if desc := tool.Description(); !strings.Contains(desc, "about 2048 estimated tokens") {
		t.Fatalf("description must state the combined continuation-state budget, got:\n%s", desc)
	}
	if desc := (CompactContextTool{}).Description(); strings.Contains(desc, "estimated tokens") {
		t.Fatalf("a validator without a budget must not advertise one, got:\n%s", desc)
	}
}

func TestCompactContextToolTraits(t *testing.T) {
	tool := CompactContextTool{}
	if tool.IsReadOnly() {
		t.Fatal("compact_context rewrites session history; IsReadOnly must be false")
	}
	if tool.Name() != NameCompactContext {
		t.Fatalf("name = %q, want %q", tool.Name(), NameCompactContext)
	}
	policy := PolicyForTool(nil, NameCompactContext, json.RawMessage(validCompactArgs()))
	if policy.Mode != ConcurrencyModeExclusive {
		t.Fatalf("concurrency mode = %q, want exclusive", policy.Mode)
	}
}
