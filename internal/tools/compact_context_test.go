package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
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

func TestCompactContextPlannedStateFilesAreNormalizedSeparately(t *testing.T) {
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(`{"active_objective":"a","next_step":"b","state_files":["notes/current.md"],"planned_state_files":["./notes/future.md"]}`))
	if err != nil {
		t.Fatalf("ParseCompactContextArgs: %v", err)
	}
	if !slices.Equal(args.StateFiles, []string{"notes/current.md"}) {
		t.Fatalf("state_files = %#v", args.StateFiles)
	}
	if !slices.Equal(args.PlannedStateFiles, []string{"notes/future.md"}) {
		t.Fatalf("planned_state_files = %#v", args.PlannedStateFiles)
	}
}

func TestCompactContextEvidenceRefsAreTrimmedAndBudgeted(t *testing.T) {
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(`{"active_objective":"a","next_step":"b","evidence_refs":["  e-1  "]}`))
	if err != nil || !slices.Equal(args.EvidenceRefs, []string{"e-1"}) {
		t.Fatalf("args=%#v err=%v", args, err)
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
	// Without a ProjectRoot provider the strict subset applies: only plain
	// relative spellings that stay under the root after cleaning are accepted.
	v := testCompactValidator()
	for name, raw := range map[string]string{
		"empty":        `{"active_objective":"a","next_step":"b","state_files":[""]}`,
		"absolute":     `{"active_objective":"a","next_step":"b","state_files":["/etc/passwd"]}`,
		"dotdot":       `{"active_objective":"a","next_step":"b","state_files":["../secret.md"]}`,
		"inner_dotdot": `{"active_objective":"a","next_step":"b","state_files":["a/../../secret.md"]}`,
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
	// Without a project root, absolute and home-relative spellings cannot be
	// checked for containment, so the rejection must tell the model what to do
	// instead (rewrite when in-project, otherwise fold the state into text).
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
		"state_files":["docs/x.md","docs//x.md","docs/x.md","other.md","docs/../notes/x.md"]
	}`))
	if err != nil {
		t.Fatalf("ParseCompactContextArgs: %v", err)
	}
	if len(args.StateFiles) != 3 || args.StateFiles[0] != "docs/x.md" || args.StateFiles[1] != "other.md" || args.StateFiles[2] != "notes/x.md" {
		t.Fatalf("state_files = %#v, want deduped [docs/x.md other.md notes/x.md]", args.StateFiles)
	}
}

// rootedCompactValidator returns a validator whose ProjectRoot provider serves
// root, with HOME pinned to its parent so "~" spellings expand deterministically.
func rootedCompactValidator(t *testing.T, root string) CompactContextValidator {
	t.Helper()
	t.Setenv("HOME", filepath.Dir(root))
	return CompactContextValidator{
		ContinuationStateMaxTokens: 2048,
		ProjectRoot:                func() string { return root },
	}
}

func TestCompactContextStateFilesAcceptsInProjectSpellings(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "proj")
	v := rootedCompactValidator(t, root)
	abs := filepath.Join(root, "docs", "x.md")
	for name, raw := range map[string]string{
		"relative":              `{"active_objective":"a","next_step":"b","state_files":["docs/x.md"]}`,
		"dot_slash":             `{"active_objective":"a","next_step":"b","state_files":["./docs/x.md"]}`,
		"inner_dot":             `{"active_objective":"a","next_step":"b","state_files":["docs/./x.md"]}`,
		"inner_dotdot":          `{"active_objective":"a","next_step":"b","state_files":["docs/../docs/x.md"]}`,
		"absolute":              `{"active_objective":"a","next_step":"b","state_files":["` + abs + `"]}`,
		"tilde":                 `{"active_objective":"a","next_step":"b","state_files":["~/proj/docs/x.md"]}`,
		"leading_dotdot_inside": `{"active_objective":"a","next_step":"b","state_files":["sub/../../proj/docs/x.md"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			args, err := v.ParseCompactContextArgs(json.RawMessage(raw))
			if err != nil {
				t.Fatalf("expected %q to be accepted: %v", raw, err)
			}
			if len(args.StateFiles) != 1 || args.StateFiles[0] != "docs/x.md" {
				t.Fatalf("state_files = %#v, want normalized [docs/x.md]", args.StateFiles)
			}
		})
	}
}

func TestCompactContextStateFilesRejectsOutsideRootEvenWithRoot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "proj")
	v := rootedCompactValidator(t, root)
	sibling := filepath.Join(home, "other.md")
	for name, raw := range map[string]string{
		"absolute_outside":    `{"active_objective":"a","next_step":"b","state_files":["` + sibling + `"]}`,
		"tilde_outside":       `{"active_objective":"a","next_step":"b","state_files":["~/other.md"]}`,
		"dotdot_outside":      `{"active_objective":"a","next_step":"b","state_files":["../other.md"]}`,
		"system_path":         `{"active_objective":"a","next_step":"b","state_files":["/etc/passwd"]}`,
		"tilde_home_dir":      `{"active_objective":"a","next_step":"b","state_files":["~"]}`,
		"project_root_itself": `{"active_objective":"a","next_step":"b","state_files":["` + root + `"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
				t.Fatalf("expected %q to be rejected", raw)
			}
		})
	}
}

func TestCompactContextStateFilesCollapsesEquivalentSpellings(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "proj")
	v := rootedCompactValidator(t, root)
	abs := filepath.Join(root, "notes", "task.md")
	args, err := v.ParseCompactContextArgs(json.RawMessage(`{
		"active_objective":"a","next_step":"b",
		"state_files":["notes/task.md","./notes/task.md","notes/../notes/task.md","` + abs + `","~/proj/notes/task.md"]
	}`))
	if err != nil {
		t.Fatalf("ParseCompactContextArgs: %v", err)
	}
	if len(args.StateFiles) != 1 || args.StateFiles[0] != "notes/task.md" {
		t.Fatalf("state_files = %#v, want single normalized [notes/task.md]", args.StateFiles)
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

// TestCompactContextAcceptsLongItemsWithoutFieldCaps is the regression for
// the removed per-field/per-item character caps: the original failing call
// packed several dense commit summaries into a single completed item and was
// rejected at the 250-rune item cap even though the whole state sat far below
// the token budget. Any per-field shape must now pass as long as the
// aggregated budget holds.
func TestCompactContextAcceptsLongItemsWithoutFieldCaps(t *testing.T) {
	args := map[string]any{
		"active_objective": strings.Repeat("o", 1200), // ~400 estimated tokens
		"next_step":        "run the agent tests",
		"completed":        []string{strings.Repeat("c", 1500)}, // single ~500-token item
		"decisions":        []string{strings.Repeat("d", 1200)}, // ~400 estimated tokens
		"state_files":      []string{strings.Repeat("p", 300) + "/x.md"},
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (CompactContextValidator{ContinuationStateMaxTokens: 2048}).ParseCompactContextArgs(raw); err != nil {
		t.Fatalf("long fields/items within the budget must be accepted: %v", err)
	}
	// The same state must fail once the aggregate exceeds the budget, even
	// though no single item is individually oversized.
	v := CompactContextValidator{ContinuationStateMaxTokens: 500}
	_, err = v.ParseCompactContextArgs(raw)
	if err == nil {
		t.Fatal("expected token budget rejection")
	}
	if !strings.Contains(err.Error(), "token budget") {
		t.Fatalf("error = %q, want token budget mention", err)
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
	// The combined estimated-token budget is the binding limit (the per-field
	// caps only prefilter), so the description must state it up front; a
	// validator without a budget must not invent one.
	tool := NewCompactContextTool(CompactContextValidator{ContinuationStateMaxTokens: 2048})
	if desc := tool.Description(); !strings.Contains(desc, "about 2048 estimated tokens") {
		t.Fatalf("description must state the combined continuation-state budget, got:\n%s", desc)
	}
	if desc := (CompactContextTool{}).Description(); strings.Contains(desc, "estimated tokens") {
		t.Fatalf("a validator without a budget must not advertise one, got:\n%s", desc)
	}
}

func TestCompactContextDescriptionTodoSyncTracksTodoWriteVisibility(t *testing.T) {
	// The checkpoint snapshots runtime todos verbatim, so a todo list that
	// drifted behind actual progress misleads the continuation. The sync
	// guidance is baked at registration time — rendered only when todo_write
	// is visible in the same surface, so the description never pushes a tool
	// the model cannot call.
	tool := NewCompactContextTool(CompactContextValidator{TodoWriteVisible: true})
	if desc := tool.Description(); !strings.Contains(desc, "todo_write") {
		t.Fatalf("description must advise syncing todos when todo_write is visible, got:\n%s", desc)
	}
	tool = NewCompactContextTool(CompactContextValidator{})
	if desc := tool.Description(); strings.Contains(desc, "todo_write") {
		t.Fatalf("description must not mention todo_write when it is not visible, got:\n%s", desc)
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

// With per-field caps gone, one dense field can eat the whole budget on its
// own. The rejection must say which one, or the model shortens the state
// blindly and usually trims the fields that were never the problem.
func TestCompactContextTokenBudgetNamesLargestFields(t *testing.T) {
	v := CompactContextValidator{ContinuationStateMaxTokens: 60}
	raw := `{
		"active_objective": "a",
		"next_step": "b",
		"completed": ["` + strings.Repeat("c", 900) + `"],
		"decisions": ["` + strings.Repeat("d", 300) + `"]
	}`
	_, err := v.ParseCompactContextArgs(json.RawMessage(raw))
	if err == nil {
		t.Fatal("expected token budget rejection")
	}
	msg := err.Error()
	// bytes/3 default estimator: completed ~300 tokens, decisions ~100.
	for _, want := range []string{"largest: completed≈300", "decisions≈100"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want substring %q", msg, want)
		}
	}
	// Only the top two are named; the tiny fields must not pad the message.
	if strings.Contains(msg, "active_objective≈") {
		t.Fatalf("error = %q, want only the two largest fields named", msg)
	}
}

func TestCompactContextRejectsUnknownStageMetadata(t *testing.T) {
	for _, raw := range []string{
		`{"active_objective":"a","next_step":"b","stage_status":"done"}`,
		`{"active_objective":"a","next_step":"b","checkpoint_kind":"authoritative"}`,
	} {
		if _, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
			t.Fatalf("expected invalid stage metadata to be rejected: %s", raw)
		}
	}
}

func TestCompactContextClaimEvidenceIsTrimmed(t *testing.T) {
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(`{"active_objective":"a","next_step":"b","completed":["tests pass"],"claim_evidence":{"tests pass":["  ev-1  "]}}`))
	if err != nil {
		t.Fatalf("ParseCompactContextArgs: %v", err)
	}
	if !slices.Equal(args.ClaimEvidence["tests pass"], []string{"ev-1"}) {
		t.Fatalf("claim_evidence = %#v", args.ClaimEvidence)
	}
}

func TestCompactContextClaimEvidenceMustMatchClaim(t *testing.T) {
	raw := `{"active_objective":"a","next_step":"b","completed":["implemented parser"],"claim_evidence":{"unrelated claim":["ev-1"]}}`
	if _, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
		t.Fatal("orphan claim evidence should be rejected")
	}
}
