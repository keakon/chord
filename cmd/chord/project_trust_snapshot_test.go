package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/trust"
)

func writeTrustDeclaration(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectTrustPermissionEffectiveWidening(t *testing.T) {
	for _, tc := range []struct {
		name, global, project, tool, argument string
	}{
		{"removed restriction", "", `permission: {"*": allow}`, toolname.Delete, "file.md"},
		{"reordered tool rules", "permission:\n  \"*\": allow\n  shell: deny\n", "permission:\n  shell: deny\n  \"*\": allow\n", toolname.Shell, "echo ok"},
		{"deny to ask", "permission: {delete: deny}", "permission: {delete: ask}", toolname.Delete, "file.md"},
		{"path broadened", "permission:\n  write:\n    \"docs/*\": allow\n", "permission: {write: allow}", toolname.Write, "file.md"},
		{"narrow shell ask overrides deny", "permission:\n  shell:\n    \"*\": deny\n    \"git *\": allow\n", "permission:\n  shell:\n    \"*\": deny\n    \"git *\": ask\n", toolname.Shell, "git status; echo ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configHome, root := riskFixture(t)
			if tc.global != "" {
				writeTrustDeclaration(t, filepath.Join(configHome, "agents", "builder.yaml"), tc.global)
			}
			projectPath := filepath.Join(root, ".chord", "agents", "builder.yaml")
			writeTrustDeclaration(t, projectPath, tc.project)
			base, err := config.ResolveAgentConfigs("", filepath.Join(configHome, "agents"))
			if err != nil {
				t.Fatal(err)
			}
			applied, err := config.ResolveAgentConfigs(filepath.Dir(projectPath), filepath.Join(configHome, "agents"))
			if err != nil {
				t.Fatal(err)
			}
			before := permission.ParsePermission(&base["builder"].Permission).Evaluate(tc.tool, tc.argument)
			after := permission.ParsePermission(&applied["builder"].Permission).Evaluate(tc.tool, tc.argument)
			if before == after || permission.StricterAction(before, after) != before {
				t.Fatalf("fixture does not widen permission: %s -> %s", before, after)
			}
			if _, _, err := failClosedGate().resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), root, root); err == nil {
				t.Fatalf("effective permission widening %s -> %s did not require trust", before, after)
			}
		})
	}
}

func TestProjectTrustLSPNoOpRemainsLowRisk(t *testing.T) {
	for _, declaration := range []string{"{}\n", "log_level: debug\n", "proxy: \"\"\n"} {
		t.Run(strings.TrimSpace(declaration), func(t *testing.T) {
			_, root := riskFixture(t)
			globalPath := globalConfigPathForTrustTest(t)
			data := []byte("providers:\n  openai:\n    preset: openai\nmodel_pools:\n  default: [openai/gpt-6.1-sol]\n")
			writeTrustDeclaration(t, globalPath, string(data))
			writeProjectConfigForTrustTest(t, root, declaration)
			plan, projectData, err := failClosedGate().resolve(globalPath, data, root, root)
			if err != nil || !plan.LowRisk || !plan.ApplyProject || plan.Authorized {
				t.Fatalf("plan=%+v error=%v", plan, err)
			}
			resolved, err := config.LoadResolvedConfigData(globalPath, data, config.ProjectConfigPath(root), projectData)
			if err != nil {
				t.Fatal(err)
			}
			if len(resolved.Global.LSP) != 0 || len(resolved.Config.LSP) != 0 {
				t.Fatal("fixture unexpectedly declares LSP servers")
			}
		})
	}
}

func TestProjectTrustUsesCallerGlobalSnapshot(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", t.TempDir())
	_, root := riskFixture(t)
	setProjectTrustFlagsForTest(t, false, false)
	globalPath := globalConfigPathForTrustTest(t)
	original := globalConfigDataForTrustTest(t)
	const declaration = "proxy: http://example.invalid:8080\n"
	writeProjectConfigForTrustTest(t, root, declaration)
	writeTrustDeclaration(t, globalPath, declaration)
	if _, _, err := resolveProjectConfigForUse(context.Background(), root, "headless", globalPath, original); err == nil {
		t.Fatal("risk assessment ignored the caller's global snapshot")
	}
	resolved, decision, err := resolveProjectConfigForUse(context.Background(), root, "headless", globalPath, []byte(declaration))
	if err != nil || !decision.Plan.LowRisk || resolved.Config.Proxy != resolved.Global.Proxy {
		t.Fatalf("current snapshot no-op resolution=%+v decision=%+v error=%v", resolved, decision, err)
	}
}

func TestProjectTrustStoreUsesCallerGlobalSnapshot(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", "")
	_, root := riskFixture(t)
	globalPath := globalConfigPathForTrustTest(t)
	capturedState, changedState := t.TempDir(), t.TempDir()
	data := []byte("paths:\n  state_dir: " + capturedState + "\n")
	writeTrustDeclaration(t, globalPath, "paths:\n  state_dir: "+changedState+"\n")
	writeProjectConfigForTrustTest(t, root, "proxy: http://example.invalid:8080\n")
	gate := newProjectTrustGate(projectTrustPolicyInteractive)
	gate.prompt = func(*projectTrustDiscovery, string) (bool, error) { return true, nil }
	plan, _, err := gate.resolve(globalPath, data, root, root)
	if err != nil || !plan.Authorized {
		t.Fatalf("plan=%+v error=%v", plan, err)
	}
	if _, err := os.Stat(trust.Path(capturedState)); err != nil {
		t.Fatalf("captured state was not used: %v", err)
	}
	if _, err := os.Stat(trust.Path(changedState)); !os.IsNotExist(err) {
		t.Fatalf("changed global state was used: %v", err)
	}
}

func TestProjectTrustCommandUsesAuthorizedSnapshot(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", t.TempDir())
	configHome, root := riskFixture(t)
	writeTrustDeclaration(t, filepath.Join(configHome, "commands", "review.md"), "Global instruction.")
	path := filepath.Join(root, ".chord", "commands", "review.md")
	writeTrustDeclaration(t, path, "Captured instruction.")
	gate := newProjectTrustGate(projectTrustPolicyInteractive)
	gate.prompt = func(*projectTrustDiscovery, string) (bool, error) { return true, nil }
	plan, _, err := gate.resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), root, root)
	if err != nil || !plan.Authorized {
		t.Fatalf("plan=%+v error=%v", plan, err)
	}
	if err := verifyProjectTrustInputs(plan.Inputs, root, root); err != nil {
		t.Fatal(err)
	}
	writeTrustDeclaration(t, path, "Changed instruction.")
	writeTrustDeclaration(t, filepath.Join(root, ".chord", "commands", "added.md"), "New instruction.")
	ac := newTestAppContext(t)
	ac.ContentRoot, ac.WorkDir, ac.ConfigHome = root, root, configHome
	ac.GlobalCfg, ac.TrustPlan = &config.Config{}, plan
	loadCustomCommands(ac)
	if len(ac.LoadedCommands) != 1 || ac.LoadedCommands[0].Template != "Captured instruction." {
		t.Fatalf("commands=%+v", ac.LoadedCommands)
	}
}

func TestProjectTrustAssessmentUsesDeclarationSnapshots(t *testing.T) {
	_, root := riskFixture(t)
	agentPath := filepath.Join(root, ".chord", "agents", "builder.yaml")
	writeTrustDeclaration(t, agentPath, "permission: deny\n")
	writeRiskSkill(t, root, "helper", "---\nname: helper\ndescription: A helper.\n---\nCaptured content.\n")
	discovery, err := discoverProjectTrust(root, root)
	if err != nil {
		t.Fatal(err)
	}
	writeTrustDeclaration(t, agentPath, "permission: allow\n")
	writeRiskSkill(t, root, "helper", "---\nname: helper\ndescription: A helper.\ncontext: fork\n---\nChanged content.\n")
	risk, err := assessProjectTrustRisk(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), root, root, discovery)
	if err != nil {
		t.Fatal(err)
	}
	if containsHazard(risk.Hazards, "skill \"helper\"") {
		t.Fatalf("assessment reread changed skill: %v", risk.Hazards)
	}
	agents, err := config.ResolveAgentConfigsData(discovery.AgentFiles, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := permission.ParsePermission(&agents["builder"].Permission).Evaluate(toolname.Shell, "echo ok"); got != permission.ActionDeny {
		t.Fatalf("agent snapshot changed: %s", got)
	}
	if err := verifyProjectTrustInputs(discovery.Inputs, root, root); err == nil {
		t.Fatal("verification did not reject changed declarations")
	}
}

func TestProjectTrustExternalBackendLaunchRisk(t *testing.T) {
	for _, tc := range []struct {
		name, base, project string
		want                bool
	}{
		{"new backend", "", "command: sample-agent\n", true},
		{"changed command", "command: sample-agent\n", "command: other-agent\n", true},
		{"changed args", "command: sample-agent\n", "command: sample-agent\n  launch_args: [--verbose]\n", true},
		{"changed env", "command: sample-agent\n", "command: sample-agent\n  env: {SAMPLE_MODE: test}\n", true},
		{"same declaration", "command: sample-agent\n", "command: sample-agent\n", false},
		{"explicit default", "", "command: claude\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configHome, root := riskFixture(t)
			prefix := "name: helper\nmode: subagent\npermission: deny\nbackend:\n  kind: claude-code\n  "
			if tc.name != "new backend" {
				writeTrustDeclaration(t, filepath.Join(configHome, "agents", "helper.yaml"), strings.TrimSuffix(prefix+tc.base, "  "))
			}
			writeTrustDeclaration(t, filepath.Join(root, ".chord", "agents", "helper.yaml"), prefix+tc.project)
			risk := assessForTest(t, root)
			if risk.requiresTrust() != tc.want {
				t.Fatalf("risk=%+v, want trust=%v", risk, tc.want)
			}
		})
	}
}
