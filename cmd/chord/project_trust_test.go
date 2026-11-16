package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/trust"
)

// globalConfigPathForTrustTest resolves the global config path after the
// config home has been redirected for the test.
func globalConfigPathForTrustTest(t *testing.T) string {
	t.Helper()
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatalf("resolve config path: %v", err)
	}
	return path
}

func globalConfigDataForTrustTest(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(globalConfigPathForTrustTest(t))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// trustOnceGate applies the current project layer without consulting the
// trust store. Tests of trusted-path merge semantics use it so their
// fixtures keep exercising the merged config exactly as before the gate.
func trustOnceGate() *projectTrustGate {
	return newProjectTrustGate(projectTrustPolicyTrustOnce)
}

func skipProjectGate() *projectTrustGate {
	return newProjectTrustGate(projectTrustPolicySkipProject)
}

func failClosedGate() *projectTrustGate {
	return newProjectTrustGate(projectTrustPolicyFailClosed)
}

func setProjectTrustFlagsForTest(t *testing.T, skip, trustOnce bool) {
	t.Helper()
	oldSkip, oldTrustOnce := flagSkipProjectConfig, flagTrustProjectConfig
	oldDecision := flagWorktreeTrustDecision
	flagSkipProjectConfig, flagTrustProjectConfig = skip, trustOnce
	t.Cleanup(func() {
		flagSkipProjectConfig, flagTrustProjectConfig = oldSkip, oldTrustOnce
		flagWorktreeTrustDecision = oldDecision
	})
}

// authorizedProjectTrustPlan is the plan a manually built AppContext uses
// when its fixture simulates a startup where the project layer applied.
func authorizedProjectTrustPlan() *projectTrustPlan {
	return &projectTrustPlan{ApplyProject: true}
}

// writeGlobalConfigForTrustTest writes a minimal valid global config into an
// isolated config home and points CHORD_CONFIG_HOME at it.
func writeGlobalConfigForTrustTest(t *testing.T) string {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte(""), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	return configHome
}

func writeProjectConfigForTrustTest(t *testing.T, projectRoot, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir project .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".chord", "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
}

func TestDiscoverProjectTrustFindsAllSources(t *testing.T) {
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "mcp:\n  fetch:\n    command: /bin/fetcher\n")
	files := map[string]string{
		".chord/agents/reviewer.yaml":     "name: reviewer\n",
		".chord/commands/deploy.md":       "deploy the sample app\n",
		".chord/skills/commit/SKILL.md":   "---\nname: commit\ndescription: commit helper\n---\nbody\n",
		".agents/skills/explore/SKILL.md": "---\nname: explore\ndescription: explore helper\n---\nbody\n",
	}
	for rel, content := range files {
		path := filepath.Join(projectRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discoverProjectTrust: %v", err)
	}
	if discovery.ConfigData == nil {
		t.Fatal("project config bytes must be captured for the merge snapshot")
	}
	roots := map[string]int{}
	for _, input := range discovery.Inputs {
		roots[input.Root] = len(input.Files)
	}
	canonical := canonicalProjectTrustRoot(projectRoot)
	if roots[canonical] != 5 { // config + agent + command + two skills
		t.Fatalf("content root must contribute 5 files, got %d (inputs %+v)", roots[canonical], discovery.Inputs)
	}
	if !strings.Contains(discovery.Sources[0].Items[0], `mcp server "fetch"`) {
		t.Fatalf("disclosure must describe the mcp server, got %+v", discovery.Sources)
	}
	if discovery.Inputs.Empty() {
		t.Fatal("inputs must not be empty")
	}
}

func TestDiscoverProjectTrustWithoutInputs(t *testing.T) {
	projectRoot := t.TempDir()
	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discoverProjectTrust: %v", err)
	}
	if !discovery.Inputs.Empty() {
		t.Fatalf("empty project must yield empty inputs, got %+v", discovery.Inputs)
	}
	if discovery.ConfigData != nil {
		t.Fatal("no project config bytes expected")
	}
}

func TestGateTrustOnceAppliesWithoutStore(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: \"\"\n")

	gate := trustOnceGate()
	plan, projectData, err := gate.resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !plan.ApplyProject || plan.Authorized {
		t.Fatalf("trust-once must apply without an authorization, got %+v", plan)
	}
	if projectData == nil {
		t.Fatal("trust-once must return the discovery snapshot bytes")
	}
	if _, err := os.Stat(trust.Path(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("trust-once must not persist a record: %v", err)
	}
}

func TestGateReusesPreflightDeclineWithoutPromptingAgain(t *testing.T) {
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: \"\"\n")
	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	gate := newProjectTrustGate(projectTrustPolicyInteractive)
	gate.preflight = &projectTrustDecision{
		ContentRoot: projectRoot,
		Plan: &projectTrustPlan{
			Inputs: discovery.Inputs,
			Notice: "project configuration was declined",
		},
	}
	prompted := false
	gate.prompt = func(*projectTrustDiscovery, string) (bool, error) {
		prompted = true
		return true, nil
	}

	plan, projectData, err := gate.resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if prompted {
		t.Fatal("an unchanged preflight decline must not prompt again")
	}
	if plan.ApplyProject || projectData != nil || plan.Notice != "project configuration was declined" {
		t.Fatalf("preflight decline was not reused: plan=%+v projectData=%q", plan, projectData)
	}
}

func TestGateRejectsChangedInputsAfterPreflightTrustOnce(t *testing.T) {
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: old\n")
	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if writeErr := os.WriteFile(config.ProjectConfigPath(projectRoot), []byte("proxy: new\n"), 0o600); writeErr != nil {
		t.Fatalf("change project config: %v", writeErr)
	}
	gate := trustOnceGate()
	gate.preflight = &projectTrustDecision{
		ContentRoot: projectRoot,
		Plan: &projectTrustPlan{
			ApplyProject: true,
			Inputs:       discovery.Inputs,
		},
		ConfigData: discovery.ConfigData,
	}

	_, _, err = gate.resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err == nil || !strings.Contains(err.Error(), "changed after worktree setup") {
		t.Fatalf("resolve error = %v, want changed preflight input rejection", err)
	}
}

func TestLocalGateConfirmsChangedInputsAfterPreflightTrustOnce(t *testing.T) {
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: old\n")
	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if writeErr := os.WriteFile(config.ProjectConfigPath(projectRoot), []byte("proxy: new\n"), 0o600); writeErr != nil {
		t.Fatalf("change project config: %v", writeErr)
	}
	gate := trustOnceGate()
	gate.allowTrustOncePreflightPrompt = true
	gate.preflight = &projectTrustDecision{
		ContentRoot: projectRoot,
		Plan: &projectTrustPlan{
			ApplyProject: true,
			Inputs:       discovery.Inputs,
		},
		ConfigData: discovery.ConfigData,
	}
	prompted := false
	gate.prompt = func(*projectTrustDiscovery, string) (bool, error) {
		prompted = true
		return true, nil
	}

	plan, projectData, err := gate.resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !prompted || !plan.ApplyProject || string(projectData) != "proxy: new\n" {
		t.Fatalf("changed preflight was not confirmed once: prompted=%v plan=%+v data=%q", prompted, plan, projectData)
	}
}

func TestGateSkipProjectPolicy(t *testing.T) {
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: \"\"\n")

	plan, projectData, err := skipProjectGate().resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.ApplyProject || projectData != nil {
		t.Fatalf("skip policy must not apply the project layer, got %+v", plan)
	}
	if plan.Notice == "" {
		t.Fatal("skip policy must explain the skipped layer")
	}
}

func TestGateFailClosedOnUnauthorizedInputs(t *testing.T) {
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: http://example.invalid:8080\n")

	_, _, err := failClosedGate().resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err == nil {
		t.Fatal("fail-closed must refuse to start with unauthorized inputs")
	}
	if !strings.Contains(err.Error(), "--skip-project-config") || !strings.Contains(err.Error(), "--trust-project-config") {
		t.Fatalf("diagnostic must name the escape hatches, got %v", err)
	}
}

func TestGateNoInputsNeedsNoAuthorization(t *testing.T) {
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()

	for _, gate := range []*projectTrustGate{failClosedGate(), skipProjectGate(), trustOnceGate()} {
		plan, projectData, err := gate.resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
		if err != nil {
			t.Fatalf("resolve with no inputs: %v", err)
		}
		if plan.ApplyProject || plan.Notice != "" || projectData != nil {
			t.Fatalf("no project inputs must pass untouched, got %+v data=%v", plan, projectData)
		}
	}
}

func TestGateStoredAuthorizationMatches(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: \"\"\n")

	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := trust.Authorize(trust.Path(stateDir), canonicalProjectTrustRoot(projectRoot), discovery.Inputs, time.Now()); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	plan, projectData, err := failClosedGate().resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("resolve with a matching authorization: %v", err)
	}
	if !plan.ApplyProject || !plan.Authorized {
		t.Fatalf("matching authorization must apply, got %+v", plan)
	}
	if projectData == nil {
		t.Fatal("authorized run must return the discovery snapshot bytes")
	}
}

func TestGateRejectsChangedProjectConfig(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: \"\"\n")

	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := trust.Authorize(trust.Path(stateDir), canonicalProjectTrustRoot(projectRoot), discovery.Inputs, time.Now()); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	// Change the project config after the authorization: the fingerprint no
	// longer matches, so every non-interactive policy must refuse.
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: \"\"\nmcp:\n  evil:\n    command: /bin/evil\n")
	_, _, err = failClosedGate().resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err == nil {
		t.Fatal("changed inputs must fail closed")
	}
	plan, projectData, err := skipProjectGate().resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err != nil || plan.ApplyProject || projectData != nil {
		t.Fatalf("changed inputs under skip policy must skip the layer, got %+v data=%v err=%v", plan, projectData, err)
	}
}

func TestGatePromptDeclineSkipsLayer(t *testing.T) {
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: http://example.invalid:8080\n")

	gate := newProjectTrustGate(projectTrustPolicyInteractive)
	gate.prompt = func(*projectTrustDiscovery, string) (bool, error) { return false, nil }
	plan, projectData, err := gate.resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.ApplyProject || projectData != nil || plan.Notice == "" {
		t.Fatalf("declined authorization must skip the layer with a notice, got %+v data=%v", plan, projectData)
	}
}

func TestGatePromptErrorAbortsStartup(t *testing.T) {
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: http://example.invalid:8080\n")

	gate := newProjectTrustGate(projectTrustPolicyInteractive)
	gate.prompt = func(*projectTrustDiscovery, string) (bool, error) {
		return false, errors.New("interactive terminal unavailable")
	}
	if _, _, err := gate.resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot); err == nil {
		t.Fatal("a prompt error must abort startup, not silently skip the layer")
	}
}

func TestGateSaveFailureNeverAuthorizes(t *testing.T) {
	// Point CHORD_STATE_DIR at a file so the store cannot be created.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHORD_STATE_DIR", blocked)
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "proxy: http://example.invalid:8080\n")

	gate := newProjectTrustGate(projectTrustPolicyInteractive)
	gate.prompt = func(*projectTrustDiscovery, string) (bool, error) { return true, nil }
	plan, projectData, err := gate.resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.ApplyProject || projectData != nil {
		t.Fatalf("a failed save must not land as authorized, got %+v data=%v", plan, projectData)
	}
	if plan.Notice == "" {
		t.Fatal("a failed save must be explained to the user")
	}
}

func TestPlanInitAppStartupSkipsUnauthorizedProjectLayer(t *testing.T) {
	withTestStateDir(t)
	if err := os.WriteFile(filepath.Join(flagConfigHome, "config.yaml"), []byte(""), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "mcp:\n  evil:\n    command: /bin/evil\nhooks:\n  on_session_start:\n    - command: /bin/evil-hook\n")
	chdirForTest(t, projectRoot)

	plan, err := planInitAppStartup(projectRoot, projectRoot, skipProjectGate())
	if err != nil {
		t.Fatalf("planInitAppStartup with skipped project layer: %v", err)
	}
	if plan.ProjectConfig != nil {
		t.Fatalf("project config must not load when the layer is skipped, got %+v", plan.ProjectConfig)
	}
	if len(plan.Config.MCP) != 0 {
		t.Fatalf("project mcp servers must not reach the effective config, got %+v", plan.Config.MCP)
	}
	if plan.TrustPlan == nil || plan.TrustPlan.ApplyProject || plan.TrustPlan.Notice == "" {
		t.Fatalf("trust plan must record the skipped layer, got %+v", plan.TrustPlan)
	}
	if !strings.Contains(strings.Join(startupConfigIssues(plan), "\n"), "--skip-project-config") {
		t.Fatalf("startup issues must surface the trust notice, got %v", startupConfigIssues(plan))
	}
}

func TestPlanInitAppStartupFailsClosedOnUnauthorizedProjectLayer(t *testing.T) {
	withTestStateDir(t)
	if err := os.WriteFile(filepath.Join(flagConfigHome, "config.yaml"), []byte(""), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "mcp:\n  evil:\n    command: /bin/evil\n")
	chdirForTest(t, projectRoot)

	_, err := planInitAppStartup(projectRoot, projectRoot, failClosedGate())
	if err == nil {
		t.Fatal("fail-closed must refuse startup with unauthorized project inputs")
	}
	if strings.Contains(err.Error(), "parse config") {
		t.Fatalf("the refusal must happen before any project parse, got %v", err)
	}
}

func TestPlanInitAppStartupSkipsMalformedProjectConfigWhenUnauthorized(t *testing.T) {
	withTestStateDir(t)
	if err := os.WriteFile(filepath.Join(flagConfigHome, "config.yaml"), []byte(""), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "hooks: [\n")
	chdirForTest(t, projectRoot)

	// The project layer is not applied, so a malformed project file cannot
	// block startup: the gate refuses it without parsing it into effect.
	plan, err := planInitAppStartup(projectRoot, projectRoot, skipProjectGate())
	if err != nil {
		t.Fatalf("untrusted malformed project config must not block startup: %v", err)
	}
	if plan.ProjectConfig != nil {
		t.Fatal("malformed project config must not load")
	}
}

func TestDiscoverProjectTrustWithoutConfigFile(t *testing.T) {
	projectRoot := t.TempDir()
	files := map[string]string{
		".chord/agents/reviewer.yaml":   "name: reviewer\n",
		".chord/commands/deploy.md":     "deploy\n",
		".chord/skills/commit/SKILL.md": "---\nname: commit\ndescription: commit helper\n---\nbody\n",
	}
	for rel, content := range files {
		path := filepath.Join(projectRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if discovery.Inputs.Empty() || discovery.ConfigData != nil {
		t.Fatalf("agents/commands/skills must gate without a project config file, inputs empty=%v", discovery.Inputs.Empty())
	}
	if len(discovery.Inputs[0].Files) != 3 {
		t.Fatalf("expected three declaration files, got %+v", discovery.Inputs)
	}
}

func TestGateSubdirectoryLaunchWithNewSkillNeedsReauthorization(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	writeGlobalConfigForTrustTest(t)
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, ".agents", "skills", "root-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: root-skill\ndescription: root helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := trust.Authorize(trust.Path(stateDir), canonicalProjectTrustRoot(projectRoot), discovery.Inputs, time.Now()); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	// Launching from a subdirectory without new declaration files keeps the
	// authorization: the same input set hashes to the same fingerprint.
	subDir := filepath.Join(projectRoot, "pkg", "service")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plan, _, err := failClosedGate().resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, subDir)
	if err != nil {
		t.Fatalf("subdirectory launch must stay authorized: %v", err)
	}
	if !plan.ApplyProject {
		t.Fatal("authorized layer must still apply")
	}

	// A new plain skill under the subdirectory is a new input but carries no
	// hazard, so it applies low-risk without requiring re-authorization.
	newSkill := filepath.Join(subDir, ".agents", "skills", "sub-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(newSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newSkill, []byte("---\nname: sub-skill\ndescription: sub helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, _, err = failClosedGate().resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, subDir)
	if err != nil {
		t.Fatalf("a plain new skill must apply low-risk: %v", err)
	}
	if !plan.LowRisk {
		t.Fatal("a plain new skill must be reported as low-risk")
	}

	// A skill that changes execution context is a hazard and must re-gate.
	forkSkill := filepath.Join(subDir, ".agents", "skills", "fork-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(forkSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(forkSkill, []byte("---\nname: fork-skill\ndescription: fork helper\ncontext: fork\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := failClosedGate().resolve(globalConfigPathForTrustTest(t), globalConfigDataForTrustTest(t), projectRoot, subDir); err == nil {
		t.Fatal("a fork-context skill must require re-authorization")
	}
}

func TestFilterSkillDirsByTrustBlocksUncoveredDirs(t *testing.T) {
	rootA := t.TempDir()
	skillA := filepath.Join(rootA, ".chord", "skills", "alpha", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillA, []byte("---\nname: alpha\ndescription: alpha helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	authorized, err := discoverProjectTrust(rootA, rootA)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	// rootA's own directory stays covered; a second checkout with a new
	// skill is blocked until re-authorized.
	rootB := t.TempDir()
	skillB := filepath.Join(rootB, ".chord", "skills", "beta", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillB), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillB, []byte("---\nname: beta\ndescription: beta helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirs := []SkillDir{
		{Path: filepath.Join(rootA, ".chord", "skills"), Scope: skillDirScopeCheckout, Root: rootA},
		{Path: filepath.Join(rootB, ".chord", "skills"), Scope: skillDirScopeCheckout, Root: rootB},
	}
	allowed, blocked := filterSkillDirsByTrust(dirs, authorized.Inputs)
	if len(allowed) != 1 || allowed[0].Path != dirs[0].Path {
		t.Fatalf("covered directory must stay, got allowed=%v", allowed)
	}
	if len(blocked) != 1 || blocked[0] != dirs[1].Path {
		t.Fatalf("uncovered directory must be blocked, got %v", blocked)
	}
}

func TestProjectSkillLoaderChecksDeclarationChanges(t *testing.T) {
	projectRoot := t.TempDir()
	skillMD := filepath.Join(projectRoot, ".chord", "skills", "commit", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillMD), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillMD, []byte("---\nname: commit\ndescription: commit helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	load := newProjectSkillLoader(discovery.Inputs, "authorized")
	meta, err := skill.LoadMeta(skillMD)
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if _, err := load(meta); err != nil {
		t.Fatalf("unchanged declaration must pass: %v", err)
	}

	if err := os.WriteFile(skillMD, []byte("---\nname: commit\ndescription: changed helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(meta); err == nil {
		t.Fatal("a changed declaration must fail the loader")
	}

	// A skill outside every authorized root is not project-controlled.
	globalMeta := &skill.Meta{Name: "global", Location: filepath.Join(t.TempDir(), "SKILL.md")}
	if err := os.WriteFile(globalMeta.Location, []byte("---\nname: global\ndescription: sample helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(globalMeta); err != nil {
		t.Fatalf("non-project skill must pass: %v", err)
	}
}

func TestRefreshSkillsForWorkDirSkipsUnauthorizedDirs(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Cfg = &config.Config{}
	ac.ConfigHome = t.TempDir()
	projectRoot := t.TempDir()
	ac.ContentRoot = projectRoot
	ac.WorkDir = projectRoot

	skillMD := filepath.Join(projectRoot, ".chord", "skills", "alpha", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillMD), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillMD, []byte("---\nname: alpha\ndescription: alpha helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	ac.TrustPlan = &projectTrustPlan{ApplyProject: true, Authorized: true, Inputs: discovery.Inputs}

	// A new checkout-level skill arrives after the authorization.
	subDir := filepath.Join(projectRoot, "pkg")
	betaMD := filepath.Join(subDir, ".agents", "skills", "beta", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(betaMD), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(betaMD, []byte("---\nname: beta\ndescription: beta helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	refreshSkillsForWorkDir(ac, subDir)

	// The scan runs off the main path; wait for it with a bound so a hung
	// scan fails the test instead of deadlocking. Poll the exported
	// lock-guarded catalog: SetSkills writes loadedSkills under skillsMu from
	// the scan goroutine, so unsynchronized AppContext fields race here.
	deadline := time.Now().Add(5 * time.Second)
	for {
		metas := ac.MainAgent.CallableSkills()
		names := make([]string, 0, len(metas))
		for _, meta := range metas {
			names = append(names, meta.Name)
		}
		sort.Strings(names)
		if len(names) == 1 && names[0] == "alpha" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("skill scan did not settle to the authorized catalog: %v", names)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRefreshSkillsForWorkDirSkipsUncoveredDirsUnderTrustOnce(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Cfg = &config.Config{}
	ac.ConfigHome = t.TempDir()
	projectRoot := t.TempDir()
	ac.ContentRoot = projectRoot
	ac.WorkDir = projectRoot

	skillMD := filepath.Join(projectRoot, ".chord", "skills", "alpha", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillMD), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillMD, []byte("---\nname: alpha\ndescription: alpha helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	// One-shot trust covers exactly the startup-discovered inputs, so a
	// switched checkout's new skill directories stay out of the catalog.
	ac.TrustPlan = &projectTrustPlan{ApplyProject: true, Authorized: false, Inputs: discovery.Inputs}

	subDir := filepath.Join(projectRoot, "pkg")
	betaMD := filepath.Join(subDir, ".agents", "skills", "beta", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(betaMD), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(betaMD, []byte("---\nname: beta\ndescription: beta helper\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	refreshSkillsForWorkDir(ac, subDir)

	deadline := time.Now().Add(5 * time.Second)
	for {
		metas := ac.MainAgent.CallableSkills()
		names := make([]string, 0, len(metas))
		for _, meta := range metas {
			names = append(names, meta.Name)
		}
		sort.Strings(names)
		if len(names) == 1 && names[0] == "alpha" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("skill scan did not settle to the startup-trusted catalog: %v", names)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDoctorConfigShowsProjectTrustStatus(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	setupDoctorConfigHome(t, "providers:\n  sample:\n    type: responses\n")
	projectRoot := t.TempDir()
	writeProjectConfigForTrustTest(t, projectRoot, "mcp:\n  fetch:\n    command: /bin/fetcher\n")
	t.Chdir(projectRoot)

	var out bytes.Buffer
	if err := runDoctorConfig(context.Background(), doctorConfigOptions{Out: &out}); err != nil {
		t.Fatalf("runDoctorConfig: %v", err)
	}
	if !strings.Contains(out.String(), "project configuration trust: not authorized") {
		t.Fatalf("expected unauthorized status, got %s", out.String())
	}
	if !strings.Contains(out.String(), `mcp server "fetch"`) {
		t.Fatalf("expected the disclosed mcp server, got %s", out.String())
	}

	discovery, err := discoverProjectTrust(projectRoot, projectRoot)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := trust.Authorize(trust.Path(stateDir), canonicalProjectTrustRoot(projectRoot), discovery.Inputs, time.Now()); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	out.Reset()
	if err := runDoctorConfig(context.Background(), doctorConfigOptions{Out: &out}); err != nil {
		t.Fatalf("runDoctorConfig: %v", err)
	}
	if !strings.Contains(out.String(), "project configuration trust: authorized") {
		t.Fatalf("expected authorized status, got %s", out.String())
	}

	out.Reset()
	if err := runDoctorConfig(context.Background(), doctorConfigOptions{Out: &out, RevokeTrust: true}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !strings.Contains(out.String(), "Revoked") {
		t.Fatalf("expected revoke confirmation, got %s", out.String())
	}
	out.Reset()
	if err := runDoctorConfig(context.Background(), doctorConfigOptions{Out: &out, RevokeTrust: true}); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if !strings.Contains(out.String(), "No project configuration authorization") {
		t.Fatalf("expected no-record notice, got %s", out.String())
	}
}
