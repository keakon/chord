package agent

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// TestSubAgent_AppliesModelEditToolFilter verifies that SubAgent respects
// the same per-model edit/patch tool filtering as MainAgent
func TestSubAgent_AppliesModelEditToolFilter(t *testing.T) {
	tests := []struct {
		name             string
		modelName        string
		wantPatchVisible bool
		wantEditVisible  bool
	}{
		{
			name:             "GPT model should see only patch tool",
			modelName:        "gpt-4",
			wantPatchVisible: true,
			wantEditVisible:  false,
		},
		{
			name:             "o1 model should see only patch tool",
			modelName:        "o1-preview",
			wantPatchVisible: true,
			wantEditVisible:  false,
		},
		{
			name:             "Claude model should see only edit tool",
			modelName:        "claude-3-opus",
			wantPatchVisible: false,
			wantEditVisible:  true,
		},
		{
			name:             "Qwen model should see only edit tool",
			modelName:        "qwen-max",
			wantPatchVisible: false,
			wantEditVisible:  true,
		},
		{
			name:             "Unknown model should default to edit tool",
			modelName:        "unknown-model",
			wantPatchVisible: false,
			wantEditVisible:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &SubAgent{
				modelName: tt.modelName,
				tools:     tools.NewRegistry(),
				ruleset:   permission.Ruleset{}, // empty ruleset = all allowed
			}
			s.tools.Register(tools.PatchTool{})
			s.tools.Register(tools.EditTool{})

			// Get visible tools through the filtering logic
			visibleTools := visibleLLMTools(s.tools, s.ruleset, isSubAgentInternalTool)
			filteredTools := filterEditToolsByModel(visibleTools, s.modelName, s.ruleset)

			// Check which tools are visible
			hasPatch := false
			hasEdit := false
			for _, tool := range filteredTools {
				if tool.Name() == tools.NamePatch {
					hasPatch = true
				}
				if tool.Name() == tools.NameEdit {
					hasEdit = true
				}
			}

			if hasPatch != tt.wantPatchVisible {
				t.Errorf("patch tool visible = %v, want %v", hasPatch, tt.wantPatchVisible)
			}
			if hasEdit != tt.wantEditVisible {
				t.Errorf("edit tool visible = %v, want %v", hasEdit, tt.wantEditVisible)
			}
		})
	}
}

// TestSubAgent_ToolFilteringInAllVisibilityPaths verifies that SubAgent
// tool visibility code paths apply the model filter correctly
func TestSubAgent_ToolFilteringInAllVisibilityPaths(t *testing.T) {
	// Test the filtering logic directly without creating a full SubAgent
	baseTools := tools.NewRegistry()
	baseTools.Register(tools.PatchTool{})
	baseTools.Register(tools.EditTool{})

	ruleset := permission.Ruleset{} // empty = all allowed
	modelName := "gpt-4"            // GPT model should see patch tool

	// Simulate what SubAgent does: apply model filter
	visibleTools := visibleLLMTools(baseTools, ruleset, isSubAgentInternalTool)
	filteredTools := filterEditToolsByModel(visibleTools, modelName, ruleset)

	// Convert to tool names map (simulates visibleToolNames)
	visibleNames := make(map[string]struct{})
	for _, tool := range filteredTools {
		visibleNames[tool.Name()] = struct{}{}
	}

	if _, ok := visibleNames[tools.NamePatch]; !ok {
		t.Error("patch tool not visible for GPT model")
	}
	if _, ok := visibleNames[tools.NameEdit]; ok {
		t.Error("edit tool incorrectly visible for GPT model")
	}

	// Test with Claude model (should see edit tool)
	modelName = "claude-opus-4"
	filteredTools = filterEditToolsByModel(visibleTools, modelName, ruleset)
	visibleNames = make(map[string]struct{})
	for _, tool := range filteredTools {
		visibleNames[tool.Name()] = struct{}{}
	}

	if _, ok := visibleNames[tools.NamePatch]; ok {
		t.Error("patch tool incorrectly visible for Claude model")
	}
	if _, ok := visibleNames[tools.NameEdit]; !ok {
		t.Error("edit tool not visible for Claude model")
	}
}

func TestSubAgentSwitchModelRefreshesFrozenToolDefinitions(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.PatchTool{})
	registry.Register(tools.EditTool{})

	s := &SubAgent{
		instanceID: "sub-test",
		parent:     &MainAgent{outputCh: make(chan AgentEvent, 1), stoppingCh: make(chan struct{})},
		ctxMgr:     ctxmgr.NewManager(4096, 0),
		tools:      registry,
		ruleset:    permission.Ruleset{},
		modelName:  "gpt-4",
	}
	s.frozenToolDefs = llmToolDefinitionsFromVisibleTools(s.filteredVisibleTools())
	if !hasToolDefinition(s.frozenToolDefs, tools.NamePatch) || hasToolDefinition(s.frozenToolDefs, tools.NameEdit) {
		t.Fatalf("initial frozen tools = %v, want patch only", toolDefinitionNames(s.frozenToolDefs))
	}

	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{}, []string{"key"})
	client := llm.NewClient(providerCfg, stubProvider{}, "claude-3-opus", 1024, "")
	s.switchModel(client, "claude-3-opus", 4096)

	if hasToolDefinition(s.frozenToolDefs, tools.NamePatch) || !hasToolDefinition(s.frozenToolDefs, tools.NameEdit) {
		t.Fatalf("frozen tools after switch = %v, want edit only", toolDefinitionNames(s.frozenToolDefs))
	}
}

func hasToolDefinition(defs []message.ToolDefinition, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

func toolDefinitionNames(defs []message.ToolDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}

// TestSubAgent_EditPatchPermissionFallback verifies that SubAgent filtering logic
// respects permission fallback between edit and patch tools
func TestSubAgent_EditPatchPermissionFallback(t *testing.T) {
	tests := []struct {
		name             string
		modelName        string
		ruleset          permission.Ruleset
		wantPatchVisible bool
		wantEditVisible  bool
	}{
		{
			name:      "GPT model with patch denied hides edit unless edit explicitly allowed",
			modelName: "gpt-4",
			ruleset: permission.Ruleset{
				{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
				{Permission: "patch", Pattern: "*", Action: permission.ActionDeny},
			},
			wantPatchVisible: false,
			wantEditVisible:  false,
		},
		{
			name:      "Claude model with edit denied hides patch unless patch explicitly allowed",
			modelName: "claude-opus-4",
			ruleset: permission.Ruleset{
				{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
				{Permission: "edit", Pattern: "*", Action: permission.ActionDeny},
			},
			wantPatchVisible: false,
			wantEditVisible:  false,
		},
		{
			name:      "GPT model with edit allowed and patch denied falls back to edit",
			modelName: "gpt-4",
			ruleset: permission.Ruleset{
				{Permission: "edit", Pattern: "*", Action: permission.ActionAllow},
				{Permission: "patch", Pattern: "*", Action: permission.ActionDeny},
			},
			wantPatchVisible: false,
			wantEditVisible:  true,
		},
		{
			name:      "Claude model with patch allowed and edit denied falls back to patch",
			modelName: "claude-opus-4",
			ruleset: permission.Ruleset{
				{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
				{Permission: "edit", Pattern: "*", Action: permission.ActionDeny},
			},
			wantPatchVisible: true,
			wantEditVisible:  false,
		},
		{
			name:      "GPT model with edit denied but patch allowed (no fallback)",
			modelName: "gpt-4",
			ruleset: permission.Ruleset{
				{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
				{Permission: "edit", Pattern: "*", Action: permission.ActionDeny},
				{Permission: "patch", Pattern: "*", Action: permission.ActionAllow},
			},
			wantPatchVisible: true,
			wantEditVisible:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseTools := tools.NewRegistry()
			baseTools.Register(tools.PatchTool{})
			baseTools.Register(tools.EditTool{})

			// Test the filtering logic directly
			visibleTools := visibleLLMTools(baseTools, tt.ruleset, isSubAgentInternalTool)
			filteredTools := filterEditToolsByModel(visibleTools, tt.modelName, tt.ruleset)

			visibleNames := make(map[string]struct{})
			for _, tool := range filteredTools {
				visibleNames[tool.Name()] = struct{}{}
			}

			hasPatch := false
			hasEdit := false
			if _, ok := visibleNames[tools.NamePatch]; ok {
				hasPatch = true
			}
			if _, ok := visibleNames[tools.NameEdit]; ok {
				hasEdit = true
			}

			if hasPatch != tt.wantPatchVisible {
				t.Errorf("patch visible = %v, want %v", hasPatch, tt.wantPatchVisible)
			}
			if hasEdit != tt.wantEditVisible {
				t.Errorf("edit visible = %v, want %v", hasEdit, tt.wantEditVisible)
			}
		})
	}
}
