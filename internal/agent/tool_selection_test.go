package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestFilterEditToolsByModel_GPTModels(t *testing.T) {
	tests := []struct {
		modelID   string
		wantPatch bool
		wantEdit  bool
	}{
		// gpt-3.5/gpt-4 families have no apply_patch training signal and should
		// see the edit tool; write/delete stay visible for them.
		{"gpt-4", false, true},
		{"gpt-4-turbo", false, true},
		{"gpt-4o", false, true},
		{"gpt-3.5-turbo", false, true},
		{"GPT-4", false, true}, // case insensitive

		// The whole gpt-5 family is patch-native: dotted subfamily names
		// (gpt-5.1-mini, gpt-5.1-codex, ...), the bare base name, and undotted
		// subfamily names (gpt-5-mini, gpt-5-nano, gpt-5-codex). Later majors
		// (gpt-6, ...) inherit the same signal. Lookalike names (gpt-5x) stay
		// on edit.
		{"gpt-5.5", true, false},
		{"gpt-5.3-codex", true, false},
		{"gpt-5.11", true, false},
		{"gpt-5.1-mini", true, false},
		{"gpt-5.1-codex", true, false},
		{"gpt-5", true, false},
		{"gpt-5-mini", true, false},
		{"gpt-5-nano", true, false},
		{"gpt-5-codex", true, false},
		{"gpt-6", true, false},
		{"gpt-6.1", true, false},
		{"gpt-50", true, false},
		{"gpt-5x", false, true},

		// o-series reasoning models default to edit: they have no apply_patch
		// training signal (the tool was introduced with GPT-5 in August 2025).
		// All o-series names share the same negative path, so two representative
		// IDs suffice.
		{"o1", false, true},
		{"o4-mini", false, true},

		// Patch-native names: the gpt-5-and-later family and the explicit
		// codex-auto-review alias. A *-codex suffix alone (daybreak-codex,
		// foo-codex-bar) carries no signal; gpt-daybreak-blue defaults to
		// edit.
		{"codex-auto-review", true, false},
		{"codex/gpt-5.3-codex", true, false},
		{"daybreak-codex", false, true},
		{"foo-codex-bar", false, true},
		{"gpt-daybreak-blue", false, true},

		// The whitelist is by name pattern: gpt-5-and-later names match
		// regardless of suffixes; the separate gpt-oss-* product naming never
		// matches.
		{"gpt-oss-120b", false, true},
		{"gpt-oss-4", false, true},
		{"gpt-5.1-oss", true, false},
		{"gpt-5.5-oss-codex", true, false},

		// Edge cases: similar-looking non-OpenAI names still use edit tool.
		{"gpt", false, true},               // bare gpt doesn't match gpt-*
		{"gptx", false, true},              // gptx doesn't match gpt-*
		{"gpt-daybreak-blue", false, true}, // daybreak names need compat, no codex signal
		{"octo-model", false, true},
		{"oracle-1", false, true},

		// Claude models should only see edit tool
		{"claude-3-opus", false, true},
		{"claude-3-sonnet", false, true},
		{"claude-3-haiku", false, true},
		{"claude-3.5-sonnet", false, true},
		{"claude-opus-4", false, true},

		// Other models should only see edit tool
		{"qwen-plus", false, true},
		{"qwen-turbo", false, true},
		{"glm-4", false, true},
		{"deepseek-chat", false, true},
		{"gemini-pro", false, true},
		{"llama-3", false, true},
		{"mistral-large", false, true},
		{"unknown-model", false, true},
		{"", false, true}, // empty model
	}

	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
		tools.WriteTool{},
		tools.DeleteTool{},
	}

	// Ruleset that allows both edit and patch
	ruleset := permission.Ruleset{
		{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			filtered := filterEditToolsByModel(allTools, tt.modelID, ruleset, nil)

			hasPatch := false
			hasEdit := false
			hasRead := false
			hasWrite := false
			hasDelete := false

			for _, tool := range filtered {
				switch tool.(type) {
				case tools.ApplyPatchTool:
					hasPatch = true
				case tools.EditTool:
					hasEdit = true
				case tools.ReadTool:
					hasRead = true
				case tools.WriteTool:
					hasWrite = true
				case tools.DeleteTool:
					hasDelete = true
				}
			}

			if hasPatch != tt.wantPatch {
				t.Errorf("model %q: hasPatch=%v, want %v", tt.modelID, hasPatch, tt.wantPatch)
			}
			if hasEdit != tt.wantEdit {
				t.Errorf("model %q: hasEdit=%v, want %v", tt.modelID, hasEdit, tt.wantEdit)
			}
			if hasPatch && hasEdit {
				t.Errorf("model %q: both patch and edit tools exposed, should only expose one", tt.modelID)
			}
			if !hasPatch && !hasEdit {
				t.Errorf("model %q: neither patch nor edit tool exposed, should expose one", tt.modelID)
			}
			if !hasRead {
				t.Errorf("model %q: read tool was filtered, should always stay", tt.modelID)
			}
			// Patch-native models route file creation and deletion through the
			// apply_patch envelope, so write/delete are hidden for them and kept
			// for everyone else.
			wantWriteDelete := !tt.wantPatch
			if hasWrite != wantWriteDelete {
				t.Errorf("model %q: hasWrite=%v, want %v", tt.modelID, hasWrite, wantWriteDelete)
			}
			if hasDelete != wantWriteDelete {
				t.Errorf("model %q: hasDelete=%v, want %v", tt.modelID, hasDelete, wantWriteDelete)
			}
		})
	}
}

func TestFilterEditToolsByModel_OnlyOneEditToolExposed(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionAllow},
	}

	models := []string{
		"gpt-4",
		"claude-3-opus",
		"qwen-plus",
		"o1-preview",
		"unknown-model",
	}

	for _, model := range models {
		filtered := filterEditToolsByModel(allTools, model, ruleset, nil)
		if len(filtered) != 1 {
			t.Errorf("model %q: filtered count=%d, want exactly 1 edit tool", model, len(filtered))
		}
	}
}

func TestFilterEditToolsByModel_NoEditTools(t *testing.T) {
	// When there are no edit tools, filterEditToolsByModel should return the input unchanged
	allTools := []tools.Tool{
		tools.ReadTool{},
		tools.WriteTool{},
		tools.ShellTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-4", ruleset, nil)
	if len(filtered) != len(allTools) {
		t.Errorf("filtered count=%d, want %d (no edit tools to filter)", len(filtered), len(allTools))
	}
}

func TestFilterEditToolsByModel_EditFamilyRuleOverridesWildcardDeny(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}
	ruleset := permission.Ruleset{
		{Permission: "edit", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", ruleset, nil)
	hasPatch := false
	for _, tool := range filtered {
		if tool.Name() == tools.NameApplyPatch {
			hasPatch = true
		}
		if tool.Name() == tools.NameEdit {
			t.Fatalf("edit tool remained visible for patch-native model")
		}
	}
	if !hasPatch {
		t.Fatal("patch tool should remain visible because edit-family rule overrides wildcard deny")
	}
}

func TestFilterEditToolsByModel_OnlyApplyPatchTool(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.ReadTool{},
	}

	rulesetEditFamilyAllowed := permission.Ruleset{
		{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", rulesetEditFamilyAllowed, nil)
	hasPatch := false
	for _, tool := range filtered {
		if _, ok := tool.(tools.ApplyPatchTool); ok {
			hasPatch = true
		}
	}
	if !hasPatch {
		t.Errorf("gpt-5.5 should keep ApplyPatchTool when it is the only registered edit-family tool")
	}

	filtered = filterEditToolsByModel(allTools, "claude-opus-4", rulesetEditFamilyAllowed, nil)
	hasPatch = false
	for _, tool := range filtered {
		if _, ok := tool.(tools.ApplyPatchTool); ok {
			hasPatch = true
		}
	}
	if !hasPatch {
		t.Errorf("claude should keep ApplyPatchTool when it is the only registered edit-family tool")
	}
}

func TestFilterEditToolsByModel_OnlyEditTool(t *testing.T) {
	allTools := []tools.Tool{
		tools.EditTool{},
		tools.ReadTool{},
	}

	rulesetEditFamilyAllowed := permission.Ruleset{
		{Permission: "edit", Pattern: "*", Action: permission.ActionAllow},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", rulesetEditFamilyAllowed, nil)
	hasEdit := false
	for _, tool := range filtered {
		if _, ok := tool.(tools.EditTool); ok {
			hasEdit = true
		}
	}
	if !hasEdit {
		t.Errorf("gpt-5.5 should keep EditTool when it is the only registered edit-family tool")
	}

	filtered = filterEditToolsByModel(allTools, "claude-opus-4", rulesetEditFamilyAllowed, nil)
	hasEdit = false
	for _, tool := range filtered {
		if _, ok := tool.(tools.EditTool); ok {
			hasEdit = true
		}
	}
	if !hasEdit {
		t.Errorf("claude should keep EditTool when it is the only registered edit-family tool")
	}
}

func TestFilterEditToolsByModel_ScopedPatchRuleKeepsPatchVisible(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "patch", Pattern: "src/**", Action: permission.ActionAllow},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", ruleset, nil)
	hasPatch := false
	hasEdit := false
	for _, tool := range filtered {
		switch tool.(type) {
		case tools.ApplyPatchTool:
			hasPatch = true
		case tools.EditTool:
			hasEdit = true
		}
	}
	if !hasPatch {
		t.Fatal("patch tool should stay visible when scoped patch permission allows some paths")
	}
	if hasEdit {
		t.Fatal("edit tool should not remain visible for GPT model when patch is available")
	}
	if got := ruleset.Evaluate("edit", "src/main.go"); got != permission.ActionAllow {
		t.Fatalf("edit should inherit scoped patch allow, got %s", got)
	}
}

func TestFilterEditToolsByModel_ScopedEditRuleKeepsEditVisible(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "edit", Pattern: "docs/**", Action: permission.ActionAsk},
	}

	filtered := filterEditToolsByModel(allTools, "claude-opus-4", ruleset, nil)
	hasPatch := false
	hasEdit := false
	for _, tool := range filtered {
		switch tool.(type) {
		case tools.ApplyPatchTool:
			hasPatch = true
		case tools.EditTool:
			hasEdit = true
		}
	}
	if !hasEdit {
		t.Fatal("edit tool should stay visible when scoped edit permission allows or asks on some paths")
	}
	if hasPatch {
		t.Fatal("patch tool should not remain visible for Claude model when edit is available")
	}
	if got := ruleset.Evaluate("patch", "docs/readme.md"); got != permission.ActionAsk {
		t.Fatalf("patch should inherit scoped edit ask, got %s", got)
	}
}

func TestFilterEditToolsByModel_PatchDenyHidesEditUnlessEditExplicitlyAllowed(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "patch", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", ruleset, nil)
	for _, tool := range filtered {
		switch tool.Name() {
		case tools.NameApplyPatch:
			t.Fatal("patch should stay hidden when explicitly denied")
		case tools.NameEdit:
			t.Fatal("edit should inherit patch deny when edit is not explicitly allowed")
		}
	}
	if got := ruleset.Evaluate("patch", "file.txt"); got != permission.ActionDeny {
		t.Fatalf("patch should remain explicitly denied, got %s", got)
	}
	if got := ruleset.Evaluate("edit", "file.txt"); got != permission.ActionDeny {
		t.Fatalf("edit should inherit patch deny, got %s", got)
	}
}

func TestFilterEditToolsByModel_EditDenyHidesPatchUnlessPatchExplicitlyAllowed(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "claude-opus-4", ruleset, nil)
	for _, tool := range filtered {
		switch tool.Name() {
		case tools.NameEdit:
			t.Fatal("edit should stay hidden when explicitly denied")
		case tools.NameApplyPatch:
			t.Fatal("patch should inherit edit deny when patch is not explicitly allowed")
		}
	}
	if got := ruleset.Evaluate("edit", "file.txt"); got != permission.ActionDeny {
		t.Fatalf("edit should remain explicitly denied, got %s", got)
	}
	if got := ruleset.Evaluate("patch", "file.txt"); got != permission.ActionDeny {
		t.Fatalf("patch should inherit edit deny, got %s", got)
	}
}

func TestFilterEditToolsByModel_EditAllowPatchDenyFallsBackToEditForGPT(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "edit", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "patch", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", ruleset, nil)
	hasEdit := false
	for _, tool := range filtered {
		switch tool.Name() {
		case tools.NameApplyPatch:
			t.Fatal("patch should stay hidden when explicitly denied")
		case tools.NameEdit:
			hasEdit = true
		}
	}
	if !hasEdit {
		t.Fatal("GPT model should fall back to edit when edit is explicitly allowed and patch is denied")
	}
}

func TestFilterEditToolsByModel_PatchAllowEditDenyFallsBackToPatchForClaude(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "claude-opus-4", ruleset, nil)
	hasPatch := false
	for _, tool := range filtered {
		switch tool.Name() {
		case tools.NameEdit:
			t.Fatal("edit should stay hidden when explicitly denied")
		case tools.NameApplyPatch:
			hasPatch = true
		}
	}
	if !hasPatch {
		t.Fatal("Claude model should fall back to patch when patch is explicitly allowed and edit is denied")
	}
}

func TestFilterEditToolsByModel_ExplicitPatchAllowBeatsEditDeny(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "claude-opus-4", ruleset, nil)
	hasPatch := false
	hasEdit := false
	for _, tool := range filtered {
		switch tool.(type) {
		case tools.ApplyPatchTool:
			hasPatch = true
		case tools.EditTool:
			hasEdit = true
		}
	}
	if !hasPatch {
		t.Fatal("patch should remain visible when patch:* allow explicitly overrides edit:* deny")
	}
	if hasEdit {
		t.Fatal("edit should stay hidden when edit:* deny is explicit")
	}
	if got := ruleset.Evaluate("patch", "file.txt"); got != permission.ActionAllow {
		t.Fatalf("patch explicit allow should win, got %s", got)
	}
	if got := ruleset.Evaluate("edit", "file.txt"); got != permission.ActionDeny {
		t.Fatalf("edit explicit deny should remain in effect, got %s", got)
	}
}

// TestExecuteToolCall_RejectsInvisibleEditFamilyTool is a regression test for a
// bug where a model that was only told about the "edit" tool could still execute
// "patch" (learned from conversation history) because the execution pipeline
// consulted the global registry instead of the model-appropriate visible set.
// The invisible sibling must be rejected with a hint pointing to the visible
// counterpart. The visible tool must still execute normally.
func TestFilterEditToolsByModel_CompatOverride(t *testing.T) {
	allTools := []tools.Tool{tools.ApplyPatchTool{}, tools.EditTool{}, tools.WriteTool{}, tools.DeleteTool{}, tools.ReadTool{}}
	ruleset := permission.Ruleset{} // all allowed
	names := func(filtered []tools.Tool) map[string]bool {
		m := make(map[string]bool, len(filtered))
		for _, tool := range filtered {
			m[tool.Name()] = true
		}
		return m
	}

	t.Run("enabled_true_forces_patch_for_non_patch_native", func(t *testing.T) {
		// deepseek would normally get edit; enabled:true adopts the full
		// patch-native semantics (patch kept, edit/write/delete hidden).
		filtered := filterEditToolsByModel(allTools, "deepseek-v4-flash", ruleset, new(true))
		got := names(filtered)
		if !got[tools.NameApplyPatch] {
			t.Error("apply_patch not visible with enabled:true override")
		}
		if got[tools.NameEdit] || got[tools.NameWrite] || got[tools.NameDelete] {
			t.Errorf("edit/write/delete must be hidden with enabled:true, got %#v", got)
		}
	})

	t.Run("enabled_false_forces_edit_for_patch_native", func(t *testing.T) {
		filtered := filterEditToolsByModel(allTools, "gpt-5.5", ruleset, new(false))
		got := names(filtered)
		if !got[tools.NameEdit] {
			t.Error("edit not visible with enabled:false override")
		}
		if got[tools.NameApplyPatch] {
			t.Error("apply_patch must be hidden with enabled:false override")
		}
		// write/delete stay visible for the edit surface.
		if !got[tools.NameWrite] || !got[tools.NameDelete] {
			t.Errorf("write/delete must stay visible on the edit surface, got %#v", got)
		}
	})

	t.Run("nil_keeps_name_inference", func(t *testing.T) {
		got := names(filterEditToolsByModel(allTools, "gpt-5.5", ruleset, nil))
		if !got[tools.NameApplyPatch] || got[tools.NameEdit] {
			t.Errorf("nil override must use name inference (patch for gpt-5.5), got %#v", got)
		}
		got = names(filterEditToolsByModel(allTools, "claude-opus-4", ruleset, nil))
		if !got[tools.NameEdit] || got[tools.NameApplyPatch] {
			t.Errorf("nil override must use name inference (edit for claude), got %#v", got)
		}
	})
}

func TestExecuteToolCall_RejectsInvisibleEditFamilyTool(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	targetPath := projectRoot + "/target.txt"
	writeFile(t, targetPath, "old line\n")
	a.tools.Register(tools.ApplyPatchTool{})
	a.tools.Register(tools.EditTool{})
	// Allow both edit-family tools at the permission layer so the only thing
	// hiding patch is the per-model edit-tool selection (model preference), not
	// a permission deny.
	a.ruleset = permission.Ruleset{
		{Permission: tools.NameApplyPatch, Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameEdit, Pattern: "*", Action: permission.ActionAllow},
	}
	// A non-OpenAI edit-only model: live visible set contains edit, not patch.
	a.llmMu.Lock()
	a.modelName = "claude-opus-4"
	a.llmMu.Unlock()

	// Calling the invisible "patch" must be rejected and point to "edit".
	patchCall := message.ToolCall{
		ID:   "patch-1",
		Name: tools.NameApplyPatch,
		Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: ` + targetPath + `\n@@\n-old line\n+new line\n*** End Patch"}`),
	}
	_, err := a.executeToolCallWithHook(context.Background(), patchCall, false)
	if err == nil {
		t.Fatal("execute patch on edit-only model succeeded; want rejection of invisible tool")
	}
	if !strings.Contains(err.Error(), "not available for the current model") || !strings.Contains(err.Error(), tools.NameEdit) {
		t.Fatalf("patch rejection err = %q; want error mentioning unavailability and the edit alternative", err.Error())
	}
	// The file must be untouched by the rejected call.
	if got, err := os.ReadFile(targetPath); err != nil || string(got) != "old line\n" {
		t.Fatalf("file changed after rejected patch call = %q; want old line", got)
	}

	// The visible "edit" tool must still work normally.
	editCall := message.ToolCall{
		ID:   "edit-1",
		Name: tools.NameEdit,
		Args: json.RawMessage(`{"path":"` + targetPath + `","old_string":"old line","new_string":"new line"}`),
	}
	if _, err := a.executeToolCallWithHook(context.Background(), editCall, false); err != nil {
		t.Fatalf("execute edit on edit-only model failed: %v", err)
	}
}

// Fallback pairings must keep write/delete: hiding them is only correct when a
// patch-native model actually kept apply_patch.
func TestFilterEditToolsByModel_FallbacksKeepWriteDelete(t *testing.T) {
	allTools := []tools.Tool{
		tools.ApplyPatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
		tools.WriteTool{},
		tools.DeleteTool{},
	}

	countKinds := func(filtered []tools.Tool) (hasWrite, hasDelete bool) {
		for _, tool := range filtered {
			switch tool.(type) {
			case tools.WriteTool:
				hasWrite = true
			case tools.DeleteTool:
				hasDelete = true
			}
		}
		return
	}

	// Non-patch-native model that gets apply_patch only because edit is denied:
	// it is not trained to route creation/deletion through the envelope.
	claudePatchFallback := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionDeny},
	}
	hasWrite, hasDelete := countKinds(filterEditToolsByModel(allTools, "claude-opus-4", claudePatchFallback, nil))
	if !hasWrite || !hasDelete {
		t.Fatalf("claude patch fallback: hasWrite=%v hasDelete=%v, want both kept", hasWrite, hasDelete)
	}

	// Patch-native model downgraded to edit needs write to create files at all.
	gptEditFallback := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "patch", Pattern: "*", Action: permission.ActionDeny},
	}
	hasWrite, hasDelete = countKinds(filterEditToolsByModel(allTools, "gpt-5.5", gptEditFallback, nil))
	if !hasWrite || !hasDelete {
		t.Fatalf("gpt edit fallback: hasWrite=%v hasDelete=%v, want both kept", hasWrite, hasDelete)
	}

	// The plain patch-native pairing hides both.
	bothAllowed := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
	}
	hasWrite, hasDelete = countKinds(filterEditToolsByModel(allTools, "gpt-5.5", bothAllowed, nil))
	if hasWrite || hasDelete {
		t.Fatalf("gpt patch-native: hasWrite=%v hasDelete=%v, want both hidden", hasWrite, hasDelete)
	}
}

// A patch-native model calling the hidden write/delete tools (learned from
// history or prior sessions) must be rejected with guidance that names the
// concrete apply_patch operation, and the filesystem must stay untouched.
func TestExecuteToolCall_RejectsHiddenWriteDeleteWithPatchGuidance(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	targetPath := projectRoot + "/target.txt"
	writeFile(t, targetPath, "old line\n")
	a.tools.Register(tools.ApplyPatchTool{})
	a.tools.Register(tools.EditTool{})
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.DeleteTool{})
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
	}
	a.llmMu.Lock()
	a.modelName = "gpt-5.5"
	a.llmMu.Unlock()
	// The tool surface resolves from the client-bound primary model; bind a
	// gpt-5.5 client so the patch-native surface hides write/delete.
	a.llmClient = llm.NewClient(
		llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, []string{"test-key"}),
		stubProvider{}, "gpt-5.5", 2048, "",
	)

	writeCall := message.ToolCall{
		ID:   "write-1",
		Name: tools.NameWrite,
		Args: json.RawMessage(`{"path":"` + targetPath + `","content":"overwritten\n"}`),
	}
	_, err := a.executeToolCallWithHook(context.Background(), writeCall, false)
	if err == nil {
		t.Fatal("execute write on patch-native model succeeded; want rejection of hidden tool")
	}
	if !strings.Contains(err.Error(), "not available for the current model") || !strings.Contains(err.Error(), "*** Add File:") {
		t.Fatalf("write rejection err = %q; want error mentioning unavailability and the Add File operation", err.Error())
	}
	if got, err := os.ReadFile(targetPath); err != nil || string(got) != "old line\n" {
		t.Fatalf("file changed after rejected write call = %q; want old line", got)
	}

	deleteCall := message.ToolCall{
		ID:   "delete-1",
		Name: tools.NameDelete,
		Args: json.RawMessage(`{"paths":["` + targetPath + `"],"reason":"cleanup"}`),
	}
	_, err = a.executeToolCallWithHook(context.Background(), deleteCall, false)
	if err == nil {
		t.Fatal("execute delete on patch-native model succeeded; want rejection of hidden tool")
	}
	if !strings.Contains(err.Error(), "not available for the current model") || !strings.Contains(err.Error(), "*** Delete File:") {
		t.Fatalf("delete rejection err = %q; want error mentioning unavailability and the Delete File operation", err.Error())
	}
	if _, statErr := os.Stat(targetPath); statErr != nil {
		t.Fatalf("file missing after rejected delete call: %v", statErr)
	}
}
