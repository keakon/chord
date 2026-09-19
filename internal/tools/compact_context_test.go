package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(`{"active_objective":"a","next_step":"b","evidence_refs":["  ev-000000000001  "]}`))
	if err != nil || !slices.Equal(args.EvidenceRefs, []string{"ev-000000000001"}) {
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
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(`{"active_objective":"a","next_step":"b","completed":["tests pass"],"claim_evidence":{"tests pass":["  ev-000000000001  "]}}`))
	if err != nil {
		t.Fatalf("ParseCompactContextArgs: %v", err)
	}
	if !slices.Equal(args.ClaimEvidence["tests pass"], []string{"ev-000000000001"}) {
		t.Fatalf("claim_evidence = %#v", args.ClaimEvidence)
	}
}

func TestCompactContextClaimEvidenceAcceptsStandaloneClaim(t *testing.T) {
	raw := `{"active_objective":"a","next_step":"b","completed":["implemented parser"],"claim_evidence":{"unrelated claim":["ev-000000000001"]}}`
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("standalone claim evidence should be accepted: %v", err)
	}
	if _, ok := args.ClaimEvidence["unrelated claim"]; !ok {
		t.Fatal("claim_evidence should preserve the standalone claim")
	}
}

func TestCompactContextDescriptionDoesNotTreatPlannedFilesAsExternalized(t *testing.T) {
	description := NewCompactContextTool(testCompactValidator()).Description()
	if !strings.Contains(description, "planned_state_files") || !strings.Contains(description, "do not externalize state") {
		t.Fatalf("description must distinguish planned files from externalized state: %q", description)
	}
}

func TestCompactContextDescriptionExplainsPressureAwareSafeStops(t *testing.T) {
	description := NewCompactContextTool(testCompactValidator()).Description()
	for _, want := range []string{
		"costed state transition",
		"safe stop",
		"provisional checkpoint",
		"stage remains active or candidate",
		"task is complete and only the final response remains",
		"A checkpoint never completes the task or replaces the final response",
		"use the normal question or waiting mechanism",
		"A terminal TODO state alone is not a reason to checkpoint",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("description must mention %q, got:\n%s", want, description)
		}
	}
}

func TestCompactContextDescriptionRetainsCallConstraintsWithoutDelegation(t *testing.T) {
	tool := NewCompactContextTool(CompactContextValidator{
		ContinuationStateMaxTokens: 2048,
		TodoWriteVisible:           true,
	})
	description := tool.Description()
	for _, want := range []string{
		"about 2048 estimated tokens",
		"Call it alone",
		"sync drifted entries with todo_write",
		"a later model-driven [Context Summary] checkpoint confirms the reset was applied",
		"normal policy result, not an error",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("missing call constraint %q", want)
		}
	}
	if strings.Contains(strings.ToLower(description), NameDelegate) {
		t.Fatal("compaction tool must not route to a possibly unavailable delegate")
	}
	required := tool.Parameters()["required"].([]string)
	if !slices.Equal(required, []string{"active_objective", "next_step"}) {
		t.Fatalf("required = %v, want only the minimum continuation state", required)
	}
}

// The rejection guidance must stay generic. The runtime validates every field
// against its own schema description and returns the reason, so the model fixes
// a rejection by addressing the reported problem — not by memorizing one
// field's remedy. Baking a parameter-specific fix into the description is the
// failure mode where each new violation grew its own example, so this pins the
// generic contract and the absence of such examples.
func TestCompactContextDescriptionKeepsRejectionGuidanceGeneric(t *testing.T) {
	description := NewCompactContextTool(testCompactValidator()).Description()
	for _, want := range []string{
		"re-submitting the same values cannot succeed",
		"If the arguments are rejected, fix the reported problem and retry",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("description must state %q, got:\n%s", want, description)
		}
	}
	for _, gone := range []string{"shorten over-budget text", "drop non-workspace paths"} {
		if strings.Contains(description, gone) {
			t.Fatalf("rejection guidance must not bake a parameter-specific remedy %q, got:\n%s", gone, description)
		}
	}
}

// An empty state_files list stays valid (pure analysis, final delivery, or a
// role without write tools), so the contract must state when it is the right
// answer instead of leaving the model to infer it from a rejection it cannot
// afford on the checkpoint's critical path. The ban on inventing files is
// scoped to filling the field: the externalization route the budget rejection
// recommends has to stay reachable, or the two texts contradict each other.
func TestCompactContextStatesWhenStateFilesMayBeEmpty(t *testing.T) {
	tool := NewCompactContextTool(testCompactValidator())
	description := tool.Description()
	for _, want := range []string{
		"Leave state_files empty when the structured arguments fully carry the recovery state",
		"never create a file merely to fill the field",
		"write that file before submitting the checkpoint",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("description must mention %q, got:\n%s", want, description)
		}
	}
	properties, ok := tool.Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatal("Parameters() must expose a properties object")
	}
	stateFiles, ok := properties["state_files"].(map[string]any)
	if !ok {
		t.Fatal("Parameters() must declare state_files")
	}
	schema, _ := stateFiles["description"].(string)
	for _, want := range []string{"Leave empty when structured arguments carry the state", "never reads them and never verifies that they exist", "current read permission allows it"} {
		if !strings.Contains(schema, want) {
			t.Fatalf("state_files schema must mention %q, got:\n%s", want, schema)
		}
	}
}

// Claim keys are normalized the same way in claim_evidence and claim_kinds.
// claim_evidence keys have always been trimmed, so a padded claim_kinds key
// used to survive untouched and drift apart from its claim_evidence twin —
// the runtime join on exact keys then reported "observed but has no
// claim_evidence" for a claim that did carry evidence, or split one claim
// across two entries.
func TestCompactContextClaimKindsKeysNormalizedLikeClaimEvidence(t *testing.T) {
	raw := `{
		"active_objective": "a", "next_step": "b",
		"completed": ["tests pass"],
		"claim_evidence": {"tests pass": ["ev-000000000001"]},
		"claim_kinds": {"  tests pass  ": "observed"}
	}`
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseCompactContextArgs: %v", err)
	}
	if got := args.ClaimKinds["tests pass"]; got != "observed" {
		t.Fatalf("claim_kinds key was not normalized: %q = %#v", "tests pass", args.ClaimKinds)
	}
	if _, ok := args.ClaimEvidence["tests pass"]; !ok {
		t.Fatalf("claim_evidence = %#v, want the trimmed twin key present", args.ClaimEvidence)
	}
}

func TestCompactContextClaimEvidenceCollapsesWhitespaceTwins(t *testing.T) {
	raw := `{"active_objective":"a","next_step":"b","claim_evidence":{"fact":["ev-000000000001"],"  fact  ":["ev-000000000002"]}}`
	args, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseCompactContextArgs: %v", err)
	}
	if len(args.ClaimEvidence) != 1 {
		t.Fatalf("claim_evidence = %#v, want the whitespace twins collapsed to one claim", args.ClaimEvidence)
	}
	if _, ok := args.ClaimEvidence["fact"]; !ok {
		t.Fatalf("claim_evidence = %#v, want key %q", args.ClaimEvidence, "fact")
	}
}

func TestCompactContextClaimKindsRejectsEmptyKey(t *testing.T) {
	for name, raw := range map[string]string{
		"blank_key": `{"active_objective":"a","next_step":"b","claim_kinds":{" ": "observed"}}`,
		"empty_key": `{"active_objective":"a","next_step":"b","claim_kinds":{"": "observed"}}`,
		"tabs_key":  `{"active_objective":"a","next_step":"b","claim_kinds":{"\t\t": "observed"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := testCompactValidator().ParseCompactContextArgs(json.RawMessage(raw)); err == nil {
				t.Fatalf("expected whitespace-only claim_kinds key %s to be rejected", name)
			}
		})
	}
}

func TestCompactContextClaimKindsEnforcesClaimCap(t *testing.T) {
	kinds := make(map[string]any, 21)
	for i := range 21 {
		kinds[fmt.Sprintf("claim-%d", i)] = "derived"
	}
	raw, err := json.Marshal(map[string]any{
		"active_objective": "a",
		"next_step":        "b",
		"claim_kinds":      kinds,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = testCompactValidator().ParseCompactContextArgs(raw)
	if err == nil || !strings.Contains(err.Error(), "claim_kinds contains 21 claims, exceeding the maximum of 20") {
		t.Fatalf("error = %v, want claim_kinds cap rejection", err)
	}
}

// The token-budget rejection must list every field that carries cost, which
// includes the stage metadata trio (stage_id/stage_status/checkpoint_kind). A
// shorten list that omitted them made the model shorten only the headline
// fields while the budget was still consumed by stage text. stage_status and
// checkpoint_kind are short enum values (they pass enum validation, so the
// budget branch runs), and stage_id is free text — the only stage field that
// can dominate the budget on its own.
func TestCompactContextTokenBudgetShortenListIncludesStageMetadata(t *testing.T) {
	v := CompactContextValidator{ContinuationStateMaxTokens: 60}
	longStageID := strings.Repeat("s", 300) // ~100 estimated tokens with the bytes/3 estimator
	raw := `{
		"active_objective": "a",
		"next_step": "b",
		"stage_id": "` + longStageID + `",
		"stage_status": "active",
		"checkpoint_kind": "provisional"
	}`
	_, err := v.ParseCompactContextArgs(json.RawMessage(raw))
	if err == nil {
		t.Fatal("expected token budget rejection")
	}
	msg := err.Error()
	// Shorten list is cost-descending: stage_id (100) first, then the enum
	// trio members checkpoint_kind≈3 and stage_status≈2, ahead of the tiny
	// text fields at ≈0. Its prefix must carry all three stage fields.
	for _, want := range []string{"token budget", "largest: stage_id≈100", "shorten stage_id/checkpoint_kind/stage_status"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want substring %q", msg, want)
		}
	}
}

// A budget rejection must name the externalization route alongside the
// shortening one. Without it the only compliant action visible to the model is
// a shorter retry, which drops the facts the checkpoint existed to preserve.
//
// The rejection and the description must also agree on that route: state_files
// references existing files and the checkpoint call cannot create one, so the
// rejection names the write-first order, and the description's ban on
// inventing files is scoped to filling the field rather than forbidding the
// file the rejection asks for.
func TestCompactContextTokenBudgetRejectionNamesStateFilesRoute(t *testing.T) {
	v := CompactContextValidator{ContinuationStateMaxTokens: 60}
	longObjective := strings.Repeat("o", 300) // ~100 estimated tokens with the bytes/3 estimator
	raw := `{
		"active_objective": "` + longObjective + `",
		"next_step": "b"
	}`
	_, err := v.ParseCompactContextArgs(json.RawMessage(raw))
	if err == nil {
		t.Fatal("expected token budget rejection")
	}
	msg := err.Error()
	for _, want := range []string{
		"token budget",
		"shorten",
		"state_files",
		"write it to a file inside the workspace",
		"the file must exist before the checkpoint is submitted",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want substring %q", msg, want)
		}
	}
	description := NewCompactContextTool(CompactContextValidator{ContinuationStateMaxTokens: 2048}).Description()
	if strings.Contains(description, "do not create or modify files solely to request a checkpoint") {
		t.Fatalf("the description must not ban the externalization the rejection recommends:\n%s", description)
	}
}

func TestCompactContextParametersCrossReferenceEvidenceRefsForObservedClaims(t *testing.T) {
	tool := NewCompactContextTool(testCompactValidator())
	properties := tool.Parameters()["properties"].(map[string]any)
	claimEvidence := properties["claim_evidence"].(map[string]any)["description"].(string)
	evidenceRefs := properties["evidence_refs"].(map[string]any)["description"].(string)
	for name, desc := range map[string]string{"claim_evidence": claimEvidence, "evidence_refs": evidenceRefs} {
		if !strings.Contains(desc, "evidence_refs") || !strings.Contains(desc, "automatically included") {
			t.Fatalf("%s description must cross-reference evidence_refs for observed claims: %q", name, desc)
		}
	}
	if got := properties["claim_evidence"].(map[string]any)["maxProperties"]; got != maxCompactContextClaims {
		t.Fatalf("claim_evidence maxProperties = %v, want %d", got, maxCompactContextClaims)
	}
}

// Values that can never resolve are rejected where the error can name the
// field and the claim they came from: the runtime validates the merged
// evidence_refs union, so its unknown-ID rejection cannot say which field the
// model has to edit.
func TestCompactContextRejectsNonEvidenceIDReferences(t *testing.T) {
	v := testCompactValidator()
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "claim_kind_in_evidence_refs",
			raw:  `{"active_objective":"a","next_step":"b","evidence_refs":["derived"]}`,
			want: []string{"argument evidence_refs", `"derived"`, "not an evidence ID"},
		},
		{
			name: "claim_kind_in_claim_evidence",
			raw:  `{"active_objective":"a","next_step":"b","claim_evidence":{"tests pass":["derived"]}}`,
			want: []string{`claim_evidence for claim "tests pass"`, `"derived"`, "not an evidence ID"},
		},
		{
			name: "path_in_claim_evidence",
			raw:  `{"active_objective":"a","next_step":"b","claim_evidence":{"tests pass":["internal/agent/compaction.go"]}}`,
			want: []string{`claim_evidence for claim "tests pass"`, "not an evidence ID"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.ParseCompactContextArgs(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatal("expected a rejection")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q must contain %q", err, want)
				}
			}
		})
	}
	// A well-shaped but unresolvable ID passes the tool layer: only the
	// runtime can tell whether it exists.
	args, err := v.ParseCompactContextArgs(json.RawMessage(`{"active_objective":"a","next_step":"b","evidence_refs":["ev-000000000000"]}`))
	if err != nil || !slices.Equal(args.EvidenceRefs, []string{"ev-000000000000"}) {
		t.Fatalf("well-shaped IDs must pass the tool layer: args=%+v err=%v", args, err)
	}
}

// The evidence-ID contract must teach the only form the main model actually
// sees and rule out stand-in values: a bare path or prose description in
// claim_evidence/evidence_refs is the observed first-attempt failure, and the
// [evidence:ev-...] rendering lives only in the summarization prompt, so
// pointing the main model at it sends it looking for an ID it cannot find.
func TestCompactContextEvidenceParamsStateVisibleIDContract(t *testing.T) {
	properties := NewCompactContextTool(testCompactValidator()).Parameters()["properties"].(map[string]any)
	for _, field := range []string{"claim_evidence", "claim_kinds", "evidence_refs"} {
		desc, _ := properties[field].(map[string]any)["description"].(string)
		if !strings.Contains(strings.ToLower(desc), "evidence id") {
			t.Errorf("%s description must name evidence IDs, got: %q", field, desc)
		}
		if !strings.Contains(desc, "visible in this conversation") {
			t.Errorf("%s description must require an ID visible in this conversation, got: %q", field, desc)
		}
		if strings.Contains(desc, "[evidence:") {
			t.Errorf("%s description must not point at the summarization-only [evidence:...] rendering, got: %q", field, desc)
		}
	}
	claimEvidence, _ := properties["claim_evidence"].(map[string]any)["description"].(string)
	for _, want := range []string{"File paths", "are not evidence IDs", "claim kinds", "derived or assumed"} {
		if !strings.Contains(claimEvidence, want) {
			t.Errorf("claim_evidence description must forbid stand-in values and name the fallback, missing %q in: %q", want, claimEvidence)
		}
	}
}
