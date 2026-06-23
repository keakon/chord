package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

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
		// GPT models should only see patch tool
		{"gpt-4", true, false},
		{"gpt-4-turbo", true, false},
		{"gpt-4o", true, false},
		{"gpt-3.5-turbo", true, false},
		{"GPT-4", true, false}, // case insensitive

		// o-series OpenAI reasoning models should use patch, including bare IDs.
		{"o1-preview", true, false},
		{"o1-mini", true, false},
		{"o1", true, false},
		{"o3-mini", true, false},
		{"o3", true, false},
		{"o4-mini", true, false},
		{"o4", true, false},
		{"o5", true, false},
		{"o10", false, true},

		// Codex-family names should also use patch.
		{"gpt-5.3-codex", true, false},
		{"codex/gpt-5.3-codex", true, false},

		// Edge cases: similar-looking non-OpenAI names still use edit tool.
		{"gpt", false, true},  // bare gpt doesn't match gpt-*
		{"gptx", false, true}, // gptx doesn't match gpt-*
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
		tools.PatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
		tools.WriteTool{},
	}

	// Ruleset that allows both edit and patch
	ruleset := permission.Ruleset{
		{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			filtered := filterEditToolsByModel(allTools, tt.modelID, ruleset)

			hasPatch := false
			hasEdit := false
			hasOther := 0

			for _, tool := range filtered {
				switch tool.(type) {
				case tools.PatchTool:
					hasPatch = true
				case tools.EditTool:
					hasEdit = true
				default:
					hasOther++
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
			// Other tools should not be filtered
			if hasOther != 2 {
				t.Errorf("model %q: other tools count=%d, want 2", tt.modelID, hasOther)
			}
		})
	}
}

func TestFilterEditToolsByModel_OnlyOneEditToolExposed(t *testing.T) {
	allTools := []tools.Tool{
		tools.PatchTool{},
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
		filtered := filterEditToolsByModel(allTools, model, ruleset)
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

	filtered := filterEditToolsByModel(allTools, "gpt-4", ruleset)
	if len(filtered) != len(allTools) {
		t.Errorf("filtered count=%d, want %d (no edit tools to filter)", len(filtered), len(allTools))
	}
}

func TestFilterEditToolsByModel_EditFamilyRuleOverridesWildcardDeny(t *testing.T) {
	allTools := []tools.Tool{
		tools.PatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}
	ruleset := permission.Ruleset{
		{Permission: "edit", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-4", ruleset)
	hasPatch := false
	for _, tool := range filtered {
		if tool.Name() == tools.NamePatch {
			hasPatch = true
		}
		if tool.Name() == tools.NameEdit {
			t.Fatalf("edit tool remained visible for GPT model")
		}
	}
	if !hasPatch {
		t.Fatal("patch tool should remain visible because edit-family rule overrides wildcard deny")
	}
}

func TestFilterEditToolsByModel_OnlyPatchTool(t *testing.T) {
	allTools := []tools.Tool{
		tools.PatchTool{},
		tools.ReadTool{},
	}

	rulesetEditFamilyAllowed := permission.Ruleset{
		{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", rulesetEditFamilyAllowed)
	hasPatch := false
	for _, tool := range filtered {
		if _, ok := tool.(tools.PatchTool); ok {
			hasPatch = true
		}
	}
	if !hasPatch {
		t.Errorf("gpt-5.5 should keep PatchTool when it is the only registered edit-family tool")
	}

	filtered = filterEditToolsByModel(allTools, "claude-opus-4", rulesetEditFamilyAllowed)
	hasPatch = false
	for _, tool := range filtered {
		if _, ok := tool.(tools.PatchTool); ok {
			hasPatch = true
		}
	}
	if !hasPatch {
		t.Errorf("claude should keep PatchTool when it is the only registered edit-family tool")
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

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", rulesetEditFamilyAllowed)
	hasEdit := false
	for _, tool := range filtered {
		if _, ok := tool.(tools.EditTool); ok {
			hasEdit = true
		}
	}
	if !hasEdit {
		t.Errorf("gpt-5.5 should keep EditTool when it is the only registered edit-family tool")
	}

	filtered = filterEditToolsByModel(allTools, "claude-opus-4", rulesetEditFamilyAllowed)
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
		tools.PatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "patch", Pattern: "src/**", Action: permission.ActionAllow},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", ruleset)
	hasPatch := false
	hasEdit := false
	for _, tool := range filtered {
		switch tool.(type) {
		case tools.PatchTool:
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
		tools.PatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "edit", Pattern: "docs/**", Action: permission.ActionAsk},
	}

	filtered := filterEditToolsByModel(allTools, "claude-opus-4", ruleset)
	hasPatch := false
	hasEdit := false
	for _, tool := range filtered {
		switch tool.(type) {
		case tools.PatchTool:
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
		tools.PatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "patch", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", ruleset)
	for _, tool := range filtered {
		switch tool.Name() {
		case tools.NamePatch:
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
		tools.PatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "claude-opus-4", ruleset)
	for _, tool := range filtered {
		switch tool.Name() {
		case tools.NameEdit:
			t.Fatal("edit should stay hidden when explicitly denied")
		case tools.NamePatch:
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
		tools.PatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "edit", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "patch", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "gpt-5.5", ruleset)
	hasEdit := false
	for _, tool := range filtered {
		switch tool.Name() {
		case tools.NamePatch:
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
		tools.PatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "claude-opus-4", ruleset)
	hasPatch := false
	for _, tool := range filtered {
		switch tool.Name() {
		case tools.NameEdit:
			t.Fatal("edit should stay hidden when explicitly denied")
		case tools.NamePatch:
			hasPatch = true
		}
	}
	if !hasPatch {
		t.Fatal("Claude model should fall back to patch when patch is explicitly allowed and edit is denied")
	}
}

func TestFilterEditToolsByModel_ExplicitPatchAllowBeatsEditDeny(t *testing.T) {
	allTools := []tools.Tool{
		tools.PatchTool{},
		tools.EditTool{},
		tools.ReadTool{},
	}

	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "edit", Pattern: "*", Action: permission.ActionDeny},
	}

	filtered := filterEditToolsByModel(allTools, "claude-opus-4", ruleset)
	hasPatch := false
	hasEdit := false
	for _, tool := range filtered {
		switch tool.(type) {
		case tools.PatchTool:
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
func TestExecuteToolCall_RejectsInvisibleEditFamilyTool(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	targetPath := projectRoot + "/target.txt"
	writeFile(t, targetPath, "old line\n")
	a.tools.Register(tools.PatchTool{})
	a.tools.Register(tools.EditTool{})
	// Allow both edit-family tools at the permission layer so the only thing
	// hiding patch is the per-model edit-tool selection (model preference), not
	// a permission deny.
	a.ruleset = permission.Ruleset{
		{Permission: tools.NamePatch, Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameEdit, Pattern: "*", Action: permission.ActionAllow},
	}
	// A non-OpenAI edit-only model: live visible set contains edit, not patch.
	a.llmMu.Lock()
	a.modelName = "claude-opus-4"
	a.llmMu.Unlock()

	// Calling the invisible "patch" must be rejected and point to "edit".
	patchCall := message.ToolCall{
		ID:   "patch-1",
		Name: tools.NamePatch,
		Args: json.RawMessage(`{"path":"` + targetPath + `","patch":"@@\n-old line\n+new line\n"}`),
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
