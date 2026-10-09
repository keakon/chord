package agent

import (
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestToolDiscoveryVisibilityFollowsAgentCandidates(t *testing.T) {
	for _, worker := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			candidate tools.Tool
			rules     permission.Ruleset
			want      bool
		}{
			{name: "no MCP"},
			{name: "eager only", candidate: anchoredManualMCPTool{name: "mcp_sample_lookup"}},
			{name: "disabled", candidate: deferredTestTool{anchoredManualMCPTool: anchoredManualMCPTool{name: "mcp_sample_lookup"}, status: tools.DiscoveryDisabled}},
			{name: "unavailable", candidate: deferredTestTool{anchoredManualMCPTool: anchoredManualMCPTool{name: "mcp_sample_lookup"}, status: tools.DiscoveryUnavailable}},
			{name: "denied", candidate: deferredTestTool{anchoredManualMCPTool: anchoredManualMCPTool{name: "mcp_sample_lookup"}}, rules: permission.Ruleset{{Permission: "mcp_*", Pattern: "*", Action: permission.ActionDeny}}},
			{name: "ask remains discoverable", candidate: deferredTestTool{anchoredManualMCPTool: anchoredManualMCPTool{name: "mcp_sample_lookup"}}, rules: permission.Ruleset{{Permission: "mcp_*", Pattern: "*", Action: permission.ActionAsk}}, want: true},
			{name: "available", candidate: deferredTestTool{anchoredManualMCPTool: anchoredManualMCPTool{name: "mcp_sample_lookup"}}, want: true},
		} {
			role := "main"
			if worker {
				role = "worker"
			}
			t.Run(role+"/"+tc.name, func(t *testing.T) {
				registry := tools.NewRegistry()
				if tc.candidate != nil {
					registry.Register(tc.candidate)
				}
				a := &MainAgent{tools: registry, ruleset: tc.rules}
				s := &SubAgent{tools: registry}
				s.setRuleset(tc.rules)
				var backend tools.ToolSearchBackend = a
				if worker {
					backend = s
				}
				registry.Register(tools.NewToolSearchTool(backend))
				// Presence in the actual model surface, rather than registry membership,
				// must reflect whether this agent can discover at least one tool.
				visible := visibleLLMTools(registry, tc.rules, func(string) bool { return false }, toolPermissionContext{})
				if got := hasToolDefinition(llmToolDefinitionsFromVisibleTools(visible), tools.NameToolSearch); got != tc.want {
					t.Fatalf("visible=%v want=%v", got, tc.want)
				}
			})
		}
	}
}

func TestToolDiscoveryVisibilityChangesWithoutLosingCatalog(t *testing.T) {
	registry := tools.NewRegistry()
	tool := deferredTestTool{name: "mcp_sample_lookup"}
	registry.Register(tool)
	a := &MainAgent{tools: registry}
	search := tools.NewToolSearchTool(a)
	registry.Register(search)
	if !search.IsAvailable() {
		t.Fatal("available tool was hidden")
	}
	tool.status = tools.DiscoveryDisabled
	registry.Register(tool)
	if search.IsAvailable() {
		t.Fatal("disabled catalog kept discovery visible")
	}
	if !a.HasDeferredTools() {
		t.Fatal("disabled tool lost its catalog identity")
	}
	tool.status = ""
	registry.Register(tool)
	if !search.IsAvailable() {
		t.Fatal("re-enabled tool stayed hidden")
	}
	a.ruleset = permission.Ruleset{{Permission: "mcp_*", Pattern: "*", Action: permission.ActionDeny}}
	if search.IsAvailable() {
		t.Fatal("revoked candidate kept discovery visible")
	}
}
