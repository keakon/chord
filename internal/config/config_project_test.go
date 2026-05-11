package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigOverrideFromPathDoesNotApplyDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.yaml")
	writeTestFile(t, path, "commands:\n  /review: summarize changes\n")

	cfg, err := LoadConfigOverrideFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigOverrideFromPath: %v", err)
	}
	if cfg.Context.Compaction.Profile != "" {
		t.Fatalf("project override should not inherit default compaction profile, got %q", cfg.Context.Compaction.Profile)
	}
	if got := cfg.Commands["/review"]; got != "summarize changes" {
		t.Fatalf("commands[/review] = %q, want summarize changes", got)
	}
}

func TestMergeProjectConfigMergesProjectScopedKeysAndIgnoresGlobalOnlyKeys(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, `providers:
  global:
    type: responses
    api_url: https://global.example/v1/responses
    models:
      gpt-global:
        limit:
          context: 8192
          output: 1024
context:
  auto_compact: true
  compaction:
    profile: archival
    reserved: 128
skills:
  paths: [/global-skill]
confirm_timeout: 45
desktop_notification: false
prevent_sleep: false
commands:
  /global: global command
hooks:
  on_idle:
    - command: ["echo", "global"]
web_fetch:
  user_agent: GlobalUA/1.0
proxy: https://global-proxy.example
worktree:
  branch_prefix: global/
paths:
  state_dir: /global-state
maintenance:
  size_check_on_startup: true
max_output_tokens: 4096
stream_retry_rounds: 0
`)
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}

	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, `providers:
  project:
    type: responses
    api_url: https://project.example/v1/responses
    models:
      gpt-project:
        limit:
          context: 4096
          output: 512
  global:
    models:
      gpt-global:
        limit:
          output: 2048
context:
  auto_compact: false
  compaction:
    profile: continuation
    reserved: 0
skills:
  paths: [/project-skill]
confirm_timeout: 0
desktop_notification: true
prevent_sleep: true
commands:
  /project: project command
hooks:
  on_idle:
    - command: ["echo", "project"]
web_fetch:
  user_agent: ""
proxy: socks5://project-proxy.example:1080
worktree:
  branch_prefix: project
paths:
  state_dir: /project-state
maintenance:
  size_check_on_startup: false
max_output_tokens: 2048
stream_retry_rounds: 2
keymap:
  cancel: [ctrl+c]
`)

	projectCfg, mergedCfg, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if projectCfg == nil {
		t.Fatal("expected project config to be returned")
	}
	if projectCfg.Context.Compaction.Profile != CompactionProfileContinuation {
		t.Fatalf("project compaction profile = %q, want %q", projectCfg.Context.Compaction.Profile, CompactionProfileContinuation)
	}
	if projectCfg.Context.CompactThreshold != 0 {
		t.Fatalf("project compact_threshold = %v, want zero unset value", projectCfg.Context.CompactThreshold)
	}
	if mergedCfg.ConfirmTimeout != 0 {
		t.Fatalf("merged confirm_timeout = %d, want 0", mergedCfg.ConfirmTimeout)
	}
	if mergedCfg.Context.AutoCompact {
		t.Fatal("expected project auto_compact=false to override global true")
	}
	if mergedCfg.Context.Compaction.Profile != CompactionProfileContinuation {
		t.Fatalf("merged compaction profile = %q, want %q", mergedCfg.Context.Compaction.Profile, CompactionProfileContinuation)
	}
	if mergedCfg.Context.Compaction.Reserved != 0 {
		t.Fatalf("merged reserved = %d, want explicit project override 0", mergedCfg.Context.Compaction.Reserved)
	}
	if len(mergedCfg.Skills.Paths) != 2 || mergedCfg.Skills.Paths[0] != "/global-skill" || mergedCfg.Skills.Paths[1] != "/project-skill" {
		t.Fatalf("merged skills.paths = %#v, want global then project", mergedCfg.Skills.Paths)
	}
	if mergedCfg.DesktopNotification == nil || !*mergedCfg.DesktopNotification {
		t.Fatal("expected desktop_notification project override to apply")
	}
	if mergedCfg.PreventSleep == nil || !*mergedCfg.PreventSleep {
		t.Fatal("expected prevent_sleep project override to apply")
	}
	if mergedCfg.WebFetch.UserAgent == nil || *mergedCfg.WebFetch.UserAgent != "" {
		t.Fatalf("merged web_fetch.user_agent = %#v, want explicit empty string", mergedCfg.WebFetch.UserAgent)
	}
	if mergedCfg.Proxy != "socks5://project-proxy.example:1080" {
		t.Fatalf("merged proxy = %q", mergedCfg.Proxy)
	}
	if mergedCfg.Worktree.BranchPrefix != "project" {
		t.Fatalf("merged worktree.branch_prefix = %q, want project", mergedCfg.Worktree.BranchPrefix)
	}
	if mergedCfg.Paths.StateDir != "/global-state" {
		t.Fatalf("project paths.state_dir should be ignored, got %q", mergedCfg.Paths.StateDir)
	}
	if !mergedCfg.Maintenance.SizeCheckOnStartup {
		t.Fatal("project maintenance override should be ignored for project config")
	}
	if mergedCfg.MaxOutputTokens != 2048 {
		t.Fatalf("merged max_output_tokens = %d, want 2048", mergedCfg.MaxOutputTokens)
	}
	if mergedCfg.StreamRetryRounds != 2 {
		t.Fatalf("merged stream_retry_rounds = %d, want 2", mergedCfg.StreamRetryRounds)
	}
	if got := mergedCfg.Commands["/global"]; got != "global command" {
		t.Fatalf("merged global command = %q", got)
	}
	if got := mergedCfg.Commands["/project"]; got != "project command" {
		t.Fatalf("merged project command = %q", got)
	}
	if len(mergedCfg.Hooks.OnIdle) != 2 {
		t.Fatalf("len(merged hooks.on_idle) = %d, want 2", len(mergedCfg.Hooks.OnIdle))
	}
	if got := mergedCfg.Hooks.OnIdle[0].Command.Args; len(got) != 2 || got[1] != "global" {
		t.Fatalf("first hook args = %#v, want global hook first", got)
	}
	if got := mergedCfg.Hooks.OnIdle[1].Command.Args; len(got) != 2 || got[1] != "project" {
		t.Fatalf("second hook args = %#v, want project hook second", got)
	}
	if _, ok := mergedCfg.Providers["project"]; !ok {
		t.Fatal("expected project provider to be merged in")
	}
	if got := mergedCfg.Providers["global"].Models["gpt-global"].Limit.Output; got != 2048 {
		t.Fatalf("merged provider override output = %d, want 2048", got)
	}
}

func TestMergeProjectConfigReturnsParseError(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "hooks: [\n")

	_, _, err := MergeProjectConfig(DefaultConfig(), projectPath)
	if err == nil {
		t.Fatal("expected parse error for malformed project config")
	}
	if got := err.Error(); got == "" || !containsAll(got, "parse config", projectPath) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
