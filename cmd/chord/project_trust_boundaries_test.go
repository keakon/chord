package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/skill"
)

func writeTrustSkill(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTrustDisclosureHidesCredentialsAndTerminalControls(t *testing.T) {
	root := t.TempDir()
	writeProjectConfigForTrustTest(t, root, `mcp:
  sample:
    command: "sample\u001b[2J\u001b[H"
    args: [--api-key, fixture-secret]
proxy: https://sample:fixture-password@example.invalid?token=fixture-token
hooks:
  on_session_start:
    - command: "sample --password fixture-shell-secret"
`)
	discovery, err := discoverProjectTrust(root, root)
	if err != nil {
		t.Fatal(err)
	}
	discovery.Sources[0].File = "source\x1b]52;c;sample\a\nforged"
	for _, render := range []func(*bytes.Buffer){
		func(out *bytes.Buffer) { renderProjectTrustAuthorization(out, discovery, root+"\x1b[H") },
		func(out *bytes.Buffer) { renderProjectTrustSources(out, discovery.Sources) },
		func(out *bytes.Buffer) { renderProjectTrustConfig(out, discovery.ConfigData) },
	} {
		var out bytes.Buffer
		render(&out)
		for _, secret := range []string{"fixture-secret", "fixture-password", "fixture-token", "fixture-shell-secret", "\x1b", "\a"} {
			if strings.Contains(out.String(), secret) {
				t.Fatalf("unsafe disclosure %q in %q", secret, out.String())
			}
		}
		if !strings.Contains(out.String(), "example.invalid") {
			t.Fatalf("endpoint identity missing: %q", out.String())
		}
	}
}

func TestProjectExtraSkillDirectoryRequiresAuthorizationAfterAddition(t *testing.T) {
	root, extra, global := t.TempDir(), t.TempDir(), t.TempDir()
	writeProjectConfigForTrustTest(t, root, "skills:\n  paths:\n    - "+extra+"\n")
	discovery, err := discoverProjectTrust(root, root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(extra, "sample", "SKILL.md")
	body := "---\nname: sample\ndescription: sample helper\n---\nbody\n"
	writeTrustSkill(t, path, body)
	writeTrustSkill(t, filepath.Join(global, "global", "SKILL.md"), strings.Replace(body, "name: sample", "name: global", 1))
	ac := &AppContext{
		ContentRoot: root, WorkDir: root,
		Cfg:       &config.Config{Skills: config.SkillsConfig{Paths: []string{extra, global}}},
		TrustPlan: &projectTrustPlan{ApplyProject: true, Inputs: discovery.Inputs},
	}
	dirs, blocked := trustedSkillDirsForWorkDir(ac, root)
	if len(blocked) != 1 || blocked[0] != extra {
		t.Fatalf("blocked=%v", blocked)
	}
	metas, err := skill.NewLoader(SkillDirPaths(dirs)).ScanMeta()
	if err != nil || len(metas) != 1 || metas[0].Name != "global" {
		t.Fatalf("global skill contract changed: metas=%v err=%v", metas, err)
	}
	meta, err := skill.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newProjectSkillLoader(discovery.Inputs, "authorized")(meta); err == nil {
		t.Fatal("new declaration in controlled empty root was loaded")
	}
}

func TestProjectSkillSymlinkSnapshotAndTargetBinding(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	body := "---\nname: sample\ndescription: sample helper\n---\nbody\n"
	targetMD := filepath.Join(target, "SKILL.md")
	writeTrustSkill(t, targetMD, body)
	writeTrustSkill(t, filepath.Join(target, "chord.yaml"), "context: inline\n")
	declaration := filepath.Join(root, ".agents", "skills", "sample", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(declaration), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetMD, declaration); err != nil {
		t.Fatal(err)
	}
	discovery, err := discoverProjectTrust(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyProjectTrustInputs(discovery.Inputs, root, root); err != nil {
		t.Fatal(err)
	}
	meta, err := skill.LoadMeta(declaration)
	if err != nil {
		t.Fatal(err)
	}
	load := newProjectSkillLoader(discovery.Inputs, "authorized")
	loaded, err := load(meta)
	if err != nil || loaded.Context != "inline" {
		t.Fatalf("unchanged authorized symlink: loaded=%v err=%v", loaded, err)
	}
	writeTrustSkill(t, filepath.Join(target, "chord.yaml"), "context: fork\n")
	if _, err := load(meta); err == nil {
		t.Fatal("changed target sidecar was accepted")
	}
	writeTrustSkill(t, filepath.Join(target, "chord.yaml"), "context: inline\n")
	other := filepath.Join(t.TempDir(), "SKILL.md")
	writeTrustSkill(t, other, body)
	if err := os.Remove(declaration); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, declaration); err != nil {
		t.Fatal(err)
	}
	if _, err := load(meta); err == nil {
		t.Fatal("same-content target replacement was accepted")
	}
}

func TestProjectExtraDirectorySymlinkRetainsOwnership(t *testing.T) {
	root, target, replacement := t.TempDir(), t.TempDir(), t.TempDir()
	extra := filepath.Join(t.TempDir(), "skills")
	if err := os.Symlink(target, extra); err != nil {
		t.Fatal(err)
	}
	writeProjectConfigForTrustTest(t, root, "skills:\n  paths:\n    - "+extra+"\n")
	discovery, err := discoverProjectTrust(root, root)
	if err != nil {
		t.Fatal(err)
	}
	writeTrustSkill(t, filepath.Join(replacement, "sample", "SKILL.md"), "---\nname: sample\ndescription: sample helper\n---\nbody\n")
	if err = os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(replacement, extra); err != nil {
		t.Fatal(err)
	}
	ac := &AppContext{
		ContentRoot: root, WorkDir: root,
		Cfg:       &config.Config{Skills: config.SkillsConfig{Paths: []string{extra}}},
		TrustPlan: &projectTrustPlan{ApplyProject: true, Inputs: discovery.Inputs},
	}
	_, blocked := trustedSkillDirsForWorkDir(ac, root)
	if len(blocked) != 1 || blocked[0] != extra {
		t.Fatalf("retargeted directory lost project ownership: %v", blocked)
	}
	meta, err := skill.LoadMeta(filepath.Join(extra, "sample", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newProjectSkillLoader(discovery.Inputs, "authorized")(meta); err == nil {
		t.Fatal("retargeted empty project directory bypassed authorization")
	}
}

func TestAgentSkillLoadConsumesAuthorizedSnapshot(t *testing.T) {
	ac := newTestAppContext(t)
	path := filepath.Join(ac.ContentRoot, ".agents", "skills", "sample", "SKILL.md")
	body := "---\nname: sample\ndescription: original helper\n---\noriginal body\n"
	writeTrustSkill(t, path, body)
	discovery, err := discoverProjectTrust(ac.ContentRoot, ac.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := skill.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	ac.MainAgent.SetSkills([]*skill.Meta{meta})
	load := newProjectSkillLoader(discovery.Inputs, "authorized")
	ac.MainAgent.SetProjectSkillLoader(func(meta *skill.Meta) (*skill.Skill, error) {
		loaded, err := load(meta)
		if err != nil {
			return nil, err
		}
		writeTrustSkill(t, path, "---\nname: sample\ndescription: changed helper\ncontext: fork\n---\nchanged body\n")
		return loaded, nil
	})
	loaded, err := ac.MainAgent.LoadSkill("sample")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Description != "original helper" || loaded.Content != "original body\n" || loaded.Context != "inline" {
		t.Fatalf("agent reread the verified snapshot: %+v", loaded)
	}
}

func TestProjectExtraSkillWithinContentRootLoads(t *testing.T) {
	root := t.TempDir()
	extra := filepath.Join(root, "skills")
	path := filepath.Join(extra, "sample", "SKILL.md")
	writeProjectConfigForTrustTest(t, root, "skills:\n  paths:\n    - "+extra+"\n")
	writeTrustSkill(t, path, "---\nname: sample\ndescription: sample helper\n---\nbody\n")
	discovery, err := discoverProjectTrust(root, root)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := skill.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newProjectSkillLoader(discovery.Inputs, "authorized")(meta); err != nil {
		t.Fatalf("authorized nested root must load: %v", err)
	}
}
