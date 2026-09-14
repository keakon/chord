package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestToolDefinitionsKeepLocalContractsOnRestrictedSurfaces(t *testing.T) {
	reg := tools.NewRegistry()
	for _, tool := range []tools.Tool{
		tools.ReadTool{}, tools.GrepTool{}, tools.GlobTool{}, tools.WriteTool{}, tools.DeleteTool{},
		tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "worker", Description: "Inspect source"}}}),
	} {
		reg.Register(tool)
	}
	cases := []struct {
		name   string
		want   string
		absent []string
	}{
		{tools.NameRead, "offset/limit cannot split a single line", []string{tools.NameGrep, tools.NameLsp, tools.NameShell}},
		{tools.NameGrep, "Returns matching lines with file paths and line numbers", []string{tools.NameLsp}},
		{tools.NameGlob, "patterns are path globs", []string{tools.NameRead, tools.NameGrep, tools.NameLsp}},
		{tools.NameWrite, "Empty content truncates the file to zero bytes", []string{tools.NameEdit, tools.NameDelete}},
		{tools.NameDelete, "Does not delete directories or wildcard patterns", []string{tools.NameWrite}},
		{tools.NameDelegate, "delivered asynchronously", []string{tools.NameRead, tools.NameGrep, tools.NameShell, tools.NameNotify, tools.NameCancel}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &MainAgent{
				tools: reg,
				ruleset: permission.Ruleset{
					{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
					{Permission: tc.name, Pattern: "*", Action: permission.ActionAllow},
				},
			}
			defs := llmToolDefinitionsFromVisibleTools(a.mainVisibleLLMTools())
			if len(defs) != 1 || defs[0].Name != tc.name {
				t.Fatalf("visible definitions = %+v, want only %s", defs, tc.name)
			}
			if !strings.Contains(defs[0].Description, tc.want) {
				t.Fatalf("%s lost its local contract %q", tc.name, tc.want)
			}
			schema, err := json.Marshal(defs[0].InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			text := strings.ToLower(defs[0].Description + "\n" + string(schema))
			for _, absent := range tc.absent {
				for _, directive := range []string{
					"use " + absent, "using " + absent, "prefer " + absent,
					"via " + absent, absent + " tool", toolPromptName(absent),
				} {
					if strings.Contains(text, directive) {
						t.Errorf("%s definition routes to absent %s via %q: %s", tc.name, absent, directive, text)
					}
				}
			}
		})
	}
}

func TestCompleteToolDefinitionsFollowCoordinationVisibility(t *testing.T) {
	for _, notify := range []bool{false, true} {
		for _, escalate := range []bool{false, true} {
			name := "complete only"
			if notify {
				name += " with notify"
			}
			if escalate {
				name += " with escalate"
			}
			t.Run(name, func(t *testing.T) {
				reg := tools.NewRegistry()
				reg.Register(tools.CompleteTool{})
				reg.Register(tools.NewNotifyTool(nil, nil, true, false))
				reg.Register(tools.NewEscalateTool(nil))
				reg.Register(tools.NewShellTool("bash"))
				rules := permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}}
				if notify {
					rules = append(rules, permission.Rule{Permission: tools.NameNotify, Pattern: "*", Action: permission.ActionAllow})
				}
				if escalate {
					rules = append(rules, permission.Rule{Permission: tools.NameEscalate, Pattern: "*", Action: permission.ActionAllow})
				}
				s := &SubAgent{tools: reg}
				s.setRuleset(rules)
				defs := llmToolDefinitionsFromVisibleTools(s.filteredVisibleTools())
				found := false
				for _, def := range defs {
					if def.Name == tools.NameShell {
						t.Fatal("denied shell must not be visible")
					}
					if def.Name != tools.NameComplete {
						continue
					}
					found = true
					schema, err := json.Marshal(def.InputSchema)
					if err != nil {
						t.Fatal(err)
					}
					for _, text := range []string{def.Description, string(schema)} {
						if !strings.Contains(text, "SubAgent Coordination section") {
							t.Fatalf("completion contract must defer blocker routing: %s", text)
						}
						for _, absent := range []string{tools.NameNotify, tools.NameEscalate, tools.NameSaveArtifact} {
							if strings.Contains(text, absent) {
								t.Fatalf("completion contract must not own %s routing: %s", absent, text)
							}
						}
					}
					if !strings.Contains(def.Description, "exactly one of result") || strings.Contains(def.Description, "all of them or none") {
						t.Fatalf("completion result pairing is ambiguous: %s", def.Description)
					}
				}
				if !found {
					t.Fatal("complete must survive wildcard denial")
				}

				prompt := s.buildSystemPrompt()
				wantRoute := "explain the blocker clearly in assistant text and wait for owner follow-up"
				if escalate {
					wantRoute = "Call `escalate` when owner-agent intervention"
				} else if notify {
					wantRoute = "use `notify` to surface blockers or owner-agent decisions"
				}
				if !strings.Contains(prompt, wantRoute) {
					t.Fatalf("missing blocker route %q: %s", wantRoute, prompt)
				}
				if !strings.Contains(prompt, "Execution-based verification is the owner agent's responsibility") {
					t.Fatal("worker without shell lost its verification boundary")
				}
			})
		}
	}
}

func TestDelegationPromptsRouteOnlyToVisibleControls(t *testing.T) {
	for _, controls := range []bool{false, true} {
		name := "controls denied"
		if controls {
			name = "controls available"
		}
		t.Run(name, func(t *testing.T) {
			reg := tools.NewRegistry()
			reg.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "worker", Description: "Inspect source"}}}))
			reg.Register(tools.NewNotifyTool(nil, nil, true, true))
			reg.Register(tools.NewCancelTool(nil))
			reg.Register(tools.ReadTool{})
			rules := permission.Ruleset{
				{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
				{Permission: tools.NameDelegate, Pattern: "*", Action: permission.ActionAllow},
			}
			if controls {
				for _, name := range []string{tools.NameNotify, tools.NameCancel} {
					rules = append(rules, permission.Rule{Permission: name, Pattern: "*", Action: permission.ActionAllow})
				}
			}
			a := &MainAgent{
				tools: reg, ruleset: rules,
				agentConfigs: map[string]*config.AgentConfig{
					"worker": {Name: "worker", Mode: config.AgentModeSubAgent},
				},
			}
			a.rebuildCachedSubAgents()
			s := &SubAgent{tools: reg, parent: a}
			s.setRuleset(rules)
			for _, prompt := range []string{a.subAgentWorkflowPromptBlock(), s.delegationPromptBlock(s.visibleToolNames())} {
				if !strings.Contains(prompt, "substantial, independent sub-work") {
					t.Fatalf("delegation strategy missing: %s", prompt)
				}
				for _, name := range []string{tools.NameNotify, tools.NameCancel} {
					if got := strings.Contains(prompt, toolPromptName(name)); got != controls {
						t.Errorf("%s reference = %v, want %v: %s", name, got, controls, prompt)
					}
				}
				if !controls && !strings.Contains(prompt, "cannot send follow-up messages to existing workers") {
					t.Fatalf("missing coordination limitation: %s", prompt)
				}
			}
		})
	}
}

func TestToolSelectionOwnsVisibilityAwareNavigation(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadTool{})
	reg.Register(tools.GrepTool{})
	reg.Register(tools.LspTool{LSP: lsp.NewManager(&config.Config{}, t.TempDir(), nil)})
	for _, navigation := range []bool{false, true} {
		rules := permission.Ruleset{
			{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
			{Permission: tools.NameRead, Pattern: "*", Action: permission.ActionAllow},
		}
		if navigation {
			for _, name := range []string{tools.NameGrep, tools.NameLsp} {
				rules = append(rules, permission.Rule{Permission: name, Pattern: "*", Action: permission.ActionAllow})
			}
		}
		a := &MainAgent{tools: reg, ruleset: rules}
		prompt := a.mainAgentCapabilityPromptBlock()
		for _, want := range []string{
			"prefer `lsp` when the file type has LSP coverage",
			"use `grep` for targeted text matches",
		} {
			if got := strings.Contains(prompt, want); got != navigation {
				t.Errorf("navigation rule %q present = %v, want %v", want, got, navigation)
			}
		}
	}
}
