package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMergeProjectConfigDoesNotApplyDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.yaml")
	writeTestFile(t, path, "commands:\n  /review: summarize changes\n")

	cfg, _, err := MergeProjectConfig(DefaultConfig(), path)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if cfg.Context.Compaction.Profile != "" {
		t.Fatalf("project override should not inherit default compaction profile, got %q", cfg.Context.Compaction.Profile)
	}
	if got := cfg.Commands["/review"]; got != "summarize changes" {
		t.Fatalf("commands[/review] = %q, want summarize changes", got)
	}
}

func TestMergeProjectConfigNormalizesContextReductionTrue(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "context:\n  reduction: true\n")

	_, mergedCfg, err := MergeProjectConfig(DefaultConfig(), projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	defaults := DefaultConfig().Context.Reduction
	if mergedCfg.Context.Reduction != defaults {
		t.Fatalf("merged context.reduction = %+v, want defaults %+v", mergedCfg.Context.Reduction, defaults)
	}
}

func TestMergeProjectConfigContextReductionTrueOverridesGlobalFalse(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, "context:\n  reduction: false\n")
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "context:\n  reduction: true\n")

	projectCfg, mergedCfg, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if projectCfg.Context.Reduction.DisabledValue() {
		t.Fatal("project context.reduction: true should explicitly enable reduction")
	}
	if enabled, explicit := projectCfg.Context.Reduction.ExplicitEnabledValue(); !explicit || !enabled {
		t.Fatalf("project context.reduction mode = (%v, %v), want explicitly enabled", enabled, explicit)
	}
	if mergedCfg.Context.Reduction.DisabledValue() {
		t.Fatal("project context.reduction: true should override global false")
	}
}

func TestMergeProjectConfigDerivesModelContext(t *testing.T) {
	base := DefaultConfig()
	base.Providers = map[string]ProviderConfig{
		"relay": {Models: map[string]ModelConfig{
			"text": {Limit: ModelLimit{Input: 200000, Output: 64000}},
		}},
	}
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "commands:\n  /review: summarize changes\n")

	_, merged, err := MergeProjectConfig(base, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if got := merged.Providers["relay"].Models["text"].Limit.Context; got != 264000 {
		t.Fatalf("derived context after merge = %d, want 264000", got)
	}
}

func TestMergeProjectConfigPerModelCompactionOverrides(t *testing.T) {
	// A project may tune compaction per model by overriding the compaction
	// block on the model definition itself; absent subfields inherit the
	// global model-level values, and models without a project override keep
	// their global definitions untouched.
	global := float64(0.3)
	reminder := float64(0.25)
	base := DefaultConfig()
	base.Providers = map[string]ProviderConfig{
		"openai": {Models: map[string]ModelConfig{
			"gpt-5.6-luna": {Compaction: &ModelCompactionConfig{Threshold: &global, Reminder: &reminder}},
		}},
	}
	base.Context.Compaction.Threshold = 0.65
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "providers:\n  openai:\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 0.4\n      gpt-5.6-sol:\n        compaction:\n          threshold: 0.7\n")

	_, merged, err := MergeProjectConfig(base, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	luna := merged.Providers["openai"].Models["gpt-5.6-luna"]
	if luna.Compaction == nil || luna.Compaction.Threshold == nil || *luna.Compaction.Threshold != 0.4 {
		t.Fatalf("project threshold override did not apply for luna: %#v", luna.Compaction)
	}
	if luna.Compaction.Reminder == nil || *luna.Compaction.Reminder != 0.25 {
		t.Fatalf("absent project reminder must inherit the global model value for luna, got %#v", luna.Compaction.Reminder)
	}
	sol := merged.Providers["openai"].Models["gpt-5.6-sol"]
	if sol.Compaction == nil || sol.Compaction.Threshold == nil || *sol.Compaction.Threshold != 0.7 {
		t.Fatalf("project-added model compaction did not merge for sol: %#v", sol.Compaction)
	}
}

func TestLoadConfigParsesResponseHeaderTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, `providers:
  openai:
    type: responses
    response_header_timeout: 180
`)

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if got := cfg.Providers["openai"].ResponseHeaderTimeout; got != 180 {
		t.Fatalf("response_header_timeout = %d, want 180", got)
	}
}

func TestLoadConfigResponseHeaderTimeoutAllowsExplicitZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, `providers:
  openai:
    type: responses
    response_header_timeout: 0
`)

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	provider := cfg.Providers["openai"]
	if got := provider.ResponseHeaderTimeout; got != 0 {
		t.Fatalf("response_header_timeout = %d, want explicit 0", got)
	}
}

func TestMergeProjectConfigResponseHeaderTimeoutZeroOverridesGlobal(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, `providers:
  openai:
    type: responses
    response_header_timeout: 180
`)
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}

	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, `providers:
  openai:
    response_header_timeout: 0
`)

	_, mergedCfg, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	provider := mergedCfg.Providers["openai"]
	if got := provider.ResponseHeaderTimeout; got != 0 {
		t.Fatalf("response_header_timeout = %d, want explicit project override 0", got)
	}
}

func TestMergeProjectConfigOverridesProviderRetrySettings(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, `providers:
  sample:
    type: responses
    retry_backoff: exponential
    retry_delay_ms: 2000
`)
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}

	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, `providers:
  sample:
    retry_backoff: fixed
    retry_delay_ms: 0
`)

	_, mergedCfg, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	provider := mergedCfg.Providers["sample"]
	if provider.RetryBackoff != RetryBackoffFixed || provider.RetryDelayMS == nil || *provider.RetryDelayMS != 0 {
		t.Fatalf("merged provider retry = %q/%v, want %q/0", provider.RetryBackoff, provider.RetryDelayMS, RetryBackoffFixed)
	}
}

func TestMergeProjectConfigMergesProjectScopedKeysAndIgnoresGlobalOnlyKeys(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, `providers:
  global:
    type: responses
    api_url: https://global.example/v1/responses
    response_header_timeout: 120
    stream_idle_timeout: 60
    stream_total_timeout: 600
    websocket_handshake_timeout: 30
    models:
      gpt-global:
        limit:
          context: 8192
          output: 1024
context:
  compaction:
    threshold: 0.8
    profile: archival
    reserved: 128
skills:
  paths: [/global-skill]
confirm_timeout: 45
question_timeout: 45
desktop_notification: false
desktop_notification_foreground: true
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
    response_header_timeout: 180
    stream_idle_timeout: 90
    stream_total_timeout: 900
    websocket_handshake_timeout: 45
    models:
      gpt-global:
        limit:
          output: 2048
context:
  compaction:
    threshold: 0
    profile: continuation
    reserved: 0
skills:
  paths: [/project-skill]
confirm_timeout: 0
question_timeout: 0
thinking_translation:
  target_language: zh-Hans
  model_pool: fast
  max_chars: 500
desktop_notification: true
desktop_notification_foreground: false
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
	if projectCfg.Context.Compaction.Threshold != 0 {
		t.Fatalf("project compaction.threshold = %v, want zero unset value", projectCfg.Context.Compaction.Threshold)
	}
	if mergedCfg.ConfirmTimeout != 0 {
		t.Fatalf("merged confirm_timeout = %d, want 0", mergedCfg.ConfirmTimeout)
	}
	if mergedCfg.QuestionTimeout != 0 {
		t.Fatalf("merged question_timeout = %d, want 0", mergedCfg.QuestionTimeout)
	}
	if mergedCfg.ThinkingTranslation.TargetLanguage != "zh-Hans" {
		t.Fatalf("merged thinking_translation.target_language = %q, want zh-Hans", mergedCfg.ThinkingTranslation.TargetLanguage)
	}
	if mergedCfg.ThinkingTranslation.ModelPool != "fast" {
		t.Fatalf("merged thinking_translation.model_pool = %q, want fast", mergedCfg.ThinkingTranslation.ModelPool)
	}
	if mergedCfg.ThinkingTranslation.MaxChars != 500 {
		t.Fatalf("merged thinking_translation.max_chars = %d, want 500", mergedCfg.ThinkingTranslation.MaxChars)
	}
	if mergedCfg.Context.Compaction.Threshold != 0 {
		t.Fatalf("merged compaction.threshold = %v, want explicit project override 0", mergedCfg.Context.Compaction.Threshold)
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
	if mergedCfg.DesktopNotificationForeground == nil || *mergedCfg.DesktopNotificationForeground {
		t.Fatal("expected desktop_notification_foreground project override to apply")
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
	if got := mergedCfg.Providers["global"].ResponseHeaderTimeout; got != 180 {
		t.Fatalf("merged provider response_header_timeout = %d, want 180", got)
	}
	if got := mergedCfg.Providers["global"].StreamIdleTimeout; got != 90 {
		t.Fatalf("merged provider stream_idle_timeout = %d, want 90", got)
	}
	if got := mergedCfg.Providers["global"].StreamTotalTimeout; got != 900 {
		t.Fatalf("merged provider stream_total_timeout = %d, want 900", got)
	}
	if got := mergedCfg.Providers["global"].WebSocketHandshakeTimeout; got != 45 {
		t.Fatalf("merged provider websocket_handshake_timeout = %d, want 45", got)
	}
}

func TestMergeProjectConfigMergesOrchestrationLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.yaml")
	writeTestFile(t, path, `orchestration:
  max_live_runtimes: 3
  max_borrowed_runtimes: 2
  max_active_llm_requests: 4
  provider_max_active_requests:
    openai: 1
`)
	global := DefaultConfig()
	global.Orchestration.MaxLiveRuntimes = 8
	global.Orchestration.ProviderMaxActiveRequests = map[string]int{"anthropic": 2}
	_, merged, err := MergeProjectConfig(global, path)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Orchestration.MaxLiveRuntimes != 3 || merged.Orchestration.MaxBorrowedRuntimes != 2 || merged.Orchestration.MaxActiveLLMRequests != 4 {
		t.Fatalf("orchestration = %+v", merged.Orchestration)
	}
	if merged.Orchestration.ProviderMaxActiveRequests["openai"] != 1 || merged.Orchestration.ProviderMaxActiveRequests["anthropic"] != 2 {
		t.Fatalf("provider limits = %#v", merged.Orchestration.ProviderMaxActiveRequests)
	}
}

func TestMergeProjectConfigReplacesSameNameMCPServerAtomically(t *testing.T) {
	globalCfg := DefaultConfig()
	globalCfg.MCP = MCPConfig{
		"exa": {
			Command:      "global-exa",
			Args:         []string{"--global"},
			Env:          []string{"EXA_KEY=global"},
			AllowedTools: []string{"global_search"},
			Manual:       true,
		},
		"shared": {URL: "https://global.example/mcp"},
	}
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, `mcp:
  exa:
    url: https://project.example/mcp
    allowed_tools: [project_search]
`)

	_, mergedCfg, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	exa := mergedCfg.MCP["exa"]
	if exa.URL != "https://project.example/mcp" || exa.Command != "" || len(exa.Args) != 0 || len(exa.Env) != 0 || exa.Manual {
		t.Fatalf("merged exa = %#v, want only project connection fields", exa)
	}
	if !reflect.DeepEqual(exa.AllowedTools, []string{"project_search"}) {
		t.Fatalf("merged exa allowed_tools = %#v, want project list", exa.AllowedTools)
	}
	if got := mergedCfg.MCP["shared"].URL; got != "https://global.example/mcp" {
		t.Fatalf("unrelated global MCP URL = %q, want preserved", got)
	}
}

func TestMergeProjectConfigMalformedProjectConfigFails(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "hooks: [\n")

	// Malformed YAML cannot be partially decoded: startup must fail instead of
	// silently applying only the global config.
	base := DefaultConfig()
	if _, _, err := MergeProjectConfig(base, projectPath); err == nil {
		t.Fatal("malformed project config should fail the merge")
	}
}

func TestMergeProjectConfigPreservesGlobalOnInvalidTypedLeaf(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, "providers:\n  sample:\n    type: responses\n    retry_delay_ms: 2000\n")
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}

	// The project value has the wrong type: it must be dropped so the global
	// 2000ms survives instead of being clobbered by a zero value.
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "providers:\n  sample:\n    retry_delay_ms: bogus\n")

	_, merged, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	p := merged.Providers["sample"]
	if p.RetryDelayMS == nil || *p.RetryDelayMS != 2000 {
		t.Fatalf("merged retry_delay_ms = %v, want global 2000 preserved", p.RetryDelayMS)
	}
}

func TestMergeProjectConfigDropsSemanticInvalidLeafKeepsSibling(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, "providers:\n  sample:\n    type: responses\n")
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}
	// retry_backoff is semantically invalid and must fall back to unset;
	// the sibling retry_delay_ms stays applied.
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "providers:\n  sample:\n    retry_backoff: linear\n    retry_delay_ms: 1500\n")

	_, merged, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	p := merged.Providers["sample"]
	if p.RetryBackoff != "" {
		t.Fatalf("merged retry_backoff = %q, want unset default", p.RetryBackoff)
	}
	if p.RetryDelayMS == nil || *p.RetryDelayMS != 1500 {
		t.Fatalf("merged retry_delay_ms = %v, want 1500 kept", p.RetryDelayMS)
	}
}

// Out-of-range compaction leaves (threshold above 1, NaN, etc.) are stripped
// from project overrides so the global line they overlay survives; individually
// valid siblings such as a per-model reminder stay applied.
func TestMergeProjectConfigDropsSemanticInvalidCompactionValues(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, "context:\n  compaction:\n    threshold: 0.65\n")
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "context:\n  compaction:\n    threshold: 1.5\nproviders:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 2\n          reminder: 0.2\n")

	_, merged, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if merged.Context.Compaction.Threshold != 0.65 {
		t.Fatalf("merged global threshold = %v, want global 0.65 preserved", merged.Context.Compaction.Threshold)
	}
	mc := merged.Providers["openai"].Models["gpt-5.6-luna"].Compaction
	if mc == nil {
		t.Fatal("model compaction block missing after merge")
	}
	if mc.Threshold != nil {
		t.Fatalf("merged model threshold = %v, want nil (inherit the global 0.65)", *mc.Threshold)
	}
	if mc.Reminder == nil || *mc.Reminder != 0.2 {
		t.Fatalf("merged model reminder = %v, want the valid 0.2 kept", mc.Reminder)
	}
}

// A project override may deliberately disable compaction (threshold 0) while a
// sibling reminder stays set; both values are individually valid, so the merge
// keeps them and only the doctor-style issue reporting flags the dead reminder.
func TestMergeProjectConfigKeepsIndividuallyValidDeadReminderCombination(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, "context:\n  compaction:\n    threshold: 0.65\n")
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "context:\n  compaction:\n    threshold: 0\n    reminder: 0.7\n")

	_, merged, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if merged.Context.Compaction.Threshold != 0 {
		t.Fatalf("merged threshold = %v, want the explicit project 0 (disable) preserved", merged.Context.Compaction.Threshold)
	}
	if merged.Context.Compaction.Reminder != 0.7 {
		t.Fatalf("merged reminder = %v, want 0.7 preserved", merged.Context.Compaction.Reminder)
	}
}

// An unknown key sitting on the first line of a nested mapping used to take the
// whole mapping with it: go-yaml reports a block container's Line as its first
// child's line, so the container looked like the offender.
func TestMergeProjectConfigDropsLeadingUnknownKeyKeepsSiblings(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, "providers:\n  sample:\n    type: responses\n")
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "providers:\n  sample:\n    bogus_field: 1\n    retry_delay_ms: 3000\n")

	cleaned, drops, err := stripTypeInvalidOverride(projectPath, []byte("providers:\n  sample:\n    bogus_field: 1\n    retry_delay_ms: 3000\n"))
	if err != nil {
		t.Fatalf("stripTypeInvalidOverride: %v", err)
	}
	if len(drops) != 1 || drops[0] != "providers.sample.bogus_field" {
		t.Fatalf("drops = %v, want only providers.sample.bogus_field", drops)
	}
	if !strings.Contains(string(cleaned), "retry_delay_ms") {
		t.Fatalf("cleaned config lost the valid sibling:\n%s", cleaned)
	}

	_, merged, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	p := merged.Providers["sample"]
	if p.RetryDelayMS == nil || *p.RetryDelayMS != 3000 {
		t.Fatalf("merged retry_delay_ms = %v, want 3000 kept", p.RetryDelayMS)
	}
	if p.Type != ProviderTypeResponses {
		t.Fatalf("merged provider type = %q, want the global responses preserved", p.Type)
	}
}

// A wrongly typed mapping value must still be dropped whole in one pass — the
// fix for leading unknown keys must not turn this into a per-child crawl.
func TestStripTypeInvalidOverrideDropsWronglyTypedContainerWhole(t *testing.T) {
	data := []byte("log_level: \n  a: 1\n  b: 2\n  c: 3\n  d: 4\n")
	cleaned, drops, err := stripTypeInvalidOverride("test.yaml", data)
	if err != nil {
		t.Fatalf("stripTypeInvalidOverride: %v", err)
	}
	if len(drops) != 1 || drops[0] != "log_level" {
		t.Fatalf("drops = %v, want only log_level", drops)
	}
	if strings.Contains(string(cleaned), "log_level") {
		t.Fatalf("cleaned config still carries log_level:\n%s", cleaned)
	}
}

// The sanitizer locates failing nodes by parsing go-yaml's error text, so it
// has to fail loudly whenever a message pins to nothing removable — a
// whole-document type error as here, or a future go-yaml wording change. The
// sanitized bytes in that case are an empty config, which would silently drop
// every project setting instead of just the invalid one, so the isolation
// failure must surface as an error rather than an emptied override.
func TestMergeProjectConfigFailsLoudWhenFailureCannotBeIsolated(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, "log_level: info\n")
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "- not\n- a mapping\n")

	_, merged, err := MergeProjectConfig(globalCfg, projectPath)
	if err == nil {
		t.Fatalf("MergeProjectConfig = %+v, want an error rather than an emptied override", merged)
	}
	if !strings.Contains(err.Error(), "unable to isolate invalid values") {
		t.Fatalf("err = %v, want the unresolvable-failure error", err)
	}
}

// A hook command with an undecodable value must be dropped like any other bad
// leaf. Its custom unmarshaler has to report a locatable TypeError, or the
// sanitizer cannot isolate anything and the whole project config aborts startup.
func TestMergeProjectConfigDropsInvalidHookCommandKeepsRest(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, "log_level: info\n")
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "hooks:\n  on_idle:\n    - name: bad\n      command:\n        shell: true\nconfirm_timeout: 42\n")

	_, merged, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if merged.ConfirmTimeout != 42 {
		t.Fatalf("merged confirm_timeout = %d, want 42 from the same project file", merged.ConfirmTimeout)
	}
}

// One unrecognized context.reduction key must not discard the valid tuning
// values sitting beside it in the same mapping.
func TestLoadConfigContextReductionUnknownKeyKeepsValidSiblings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, "context:\n  reduction:\n    high_pressure_usage: 0.8\n    shell_read_only_age_turns: 5\n")

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if cfg.Context.Reduction.ShellReadOnlyAgeTurns != 5 {
		t.Fatalf("shell_read_only_age_turns = %d, want 5 applied despite the unknown sibling", cfg.Context.Reduction.ShellReadOnlyAgeTurns)
	}
}

// The same tolerance has to survive the project-override sanitizer, which
// strips the unknown key by line before merging.
func TestMergeProjectConfigContextReductionUnknownKeyKeepsValidSiblings(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "global.yaml")
	writeTestFile(t, globalPath, "log_level: info\n")
	globalCfg, err := LoadConfigFromPath(globalPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath(global): %v", err)
	}
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "context:\n  reduction:\n    high_pressure_usage: 0.8\n    shell_read_only_age_turns: 5\n")

	_, merged, err := MergeProjectConfig(globalCfg, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if merged.Context.Reduction.ShellReadOnlyAgeTurns != 5 {
		t.Fatalf("merged shell_read_only_age_turns = %d, want 5", merged.Context.Reduction.ShellReadOnlyAgeTurns)
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

// TestMergeProjectConfigRederivesContextAfterOutputOverride pins the derived
// context total against going stale: the global config derived 264000 from
// input+output, and a project override raising output must re-derive the sum
// instead of keeping the frozen 264000 as if it were explicit.
func TestMergeProjectConfigRederivesContextAfterOutputOverride(t *testing.T) {
	base := DefaultConfig()
	base.Providers = map[string]ProviderConfig{
		"relay": {Models: map[string]ModelConfig{
			"text": {Limit: ModelLimit{Input: 200000, Output: 64000}},
		}},
	}
	normalizeModelLimits(base)
	if got := base.Providers["relay"].Models["text"].Limit.Context; got != 264000 {
		t.Fatalf("precondition: derived base context = %d, want 264000", got)
	}
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "providers:\n  relay:\n    models:\n      text:\n        limit:\n          output: 128000\n")

	_, merged, err := MergeProjectConfig(base, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if got := merged.Providers["relay"].Models["text"].Limit.Context; got != 328000 {
		t.Fatalf("re-derived context = %d, want 328000", got)
	}
}

// TestMergeProjectConfigKeepsExplicitContextAcrossOutputOverride: an explicit
// context (documented as allowed to differ from input+output) must survive a
// project output override untouched.
func TestMergeProjectConfigKeepsExplicitContextAcrossOutputOverride(t *testing.T) {
	base := DefaultConfig()
	base.Providers = map[string]ProviderConfig{
		"relay": {Models: map[string]ModelConfig{
			"text": {Limit: ModelLimit{Context: 300000, Input: 200000, Output: 64000}},
		}},
	}
	normalizeModelLimits(base)
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "providers:\n  relay:\n    models:\n      text:\n        limit:\n          output: 128000\n")

	_, merged, err := MergeProjectConfig(base, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if got := merged.Providers["relay"].Models["text"].Limit.Context; got != 300000 {
		t.Fatalf("explicit context after merge = %d, want 300000 preserved", got)
	}
	if got := merged.Providers["relay"].Models["text"].Limit.Output; got != 128000 {
		t.Fatalf("merged output = %d, want 128000", got)
	}
}

func TestMergeProjectConfigIgnoresGlobalOnlyKeysAndUnknown(t *testing.T) {
	ignored := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, ignored, "diagnostics:\n  enabled: true\nlog_level: debug\n")
	if _, _, err := MergeProjectConfig(DefaultConfig(), ignored); err != nil {
		t.Fatalf("global-only keys should be ignored, got %v", err)
	}

	base := DefaultConfig()
	base.Providers = map[string]ProviderConfig{
		"inner": {Type: ProviderTypeMessages},
	}
	typo := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, typo, "provder:\n  x: 1\nproviders:\n  inner:\n    type: messages\n")
	_, merged, err := MergeProjectConfig(base, typo)
	if err != nil {
		t.Fatalf("unknown top-level project key should be logged and dropped, got %v", err)
	}
	if got := merged.Providers["inner"].Type; got != ProviderTypeMessages {
		t.Fatalf("merged provider inner.type = %q, want messages (base preserved)", got)
	}
}

// TestProjectWhitelistCoversAllConfigKeys uses reflection to verify that every
// yaml-tagged top-level field in Config is accounted for by either
// projectScopedTopLevelKeys or projectIgnoredTopLevelKeys. This prevents a
// future field addition from silently falling through the guard in
// mergeConfigOverrideData.
func TestProjectWhitelistCoversAllConfigKeys(t *testing.T) {
	for f := range reflect.TypeFor[Config]().Fields() {
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		key, _, _ := strings.Cut(tag, ",")
		if key == "" {
			continue
		}
		if !projectScopedTopLevelKeys[key] && !projectIgnoredTopLevelKeys[key] {
			t.Errorf("Config yaml key %q is not in projectScopedTopLevelKeys or projectIgnoredTopLevelKeys; the project-layer whitelist guard will reject it at startup", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Orchestration load-time validation and project-merge semantics.
//
// New contract enforced by the loader:
//   - waiting_main_max_wait_sec explicitly below waiting_main_min_wait_sec is
//     a configuration error and fails the load. Both values used to be
//     silently coerced by the effective-value layer, hiding likely unit or
//     meaning mistakes.
//   - A single-sided waiting_main value or zero/negative scalars keep their
//     documented default-retaining semantics ("effective maximum is never
//     below the effective minimum", "zero or negative retain the inherited or
//     built-in default") and never fail the load.
//   - subagent_compact_usage outside (0,1) falls back to the default with a
//     visible issue instead of silently riding through.
//   - At the project layer, zero or negative orchestration scalar overrides
//     are stripped before the merge so the global line they overlay survives
//     (only positive project scalar values override per the docs); before
//     this fix a project max_live_runtimes: 0 clobbered the global value and
//     the effective-value layer fell back to the built-in default instead.
// ---------------------------------------------------------------------------

func TestLoadConfigRejectsOrchestrationWaitingMaxBelowMin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, "orchestration:\n  waiting_main_min_wait_sec: 600\n  waiting_main_max_wait_sec: 300\n")
	_, err := LoadConfigFromPath(path)
	if err == nil {
		t.Fatal("LoadConfigFromPath: want error for inverted waiting_main clocks")
	}
	for _, want := range []string{"waiting_main_max_wait_sec 300", "waiting_main_min_wait_sec 600"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("LoadConfigFromPath error = %q, want it mentioning %q", err, want)
		}
	}
}

func TestCollectConfigFileIssuesReportsOrchestrationWaitingInversion(t *testing.T) {
	// The same contradiction surfaces through the issue collector (doctor /
	// startup config issues), so it is visible even where the loader itself
	// does not run.
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, "orchestration:\n  waiting_main_min_wait_sec: 600\n  waiting_main_max_wait_sec: 300\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	if joined := strings.Join(issues, "\n"); !strings.Contains(joined, "waiting_main_max_wait_sec 300 is below waiting_main_min_wait_sec 600") {
		t.Fatalf("issues = %q, want the waiting_main clock inversion report", joined)
	}
}

func TestLoadConfigAllowsOrchestrationWaitingMaxAtOrAboveMin(t *testing.T) {
	// Equal clocks are coherent (guarded and unconditional expiry share one
	// bound); min below max is the normal arrangement. Neither may fail.
	for _, body := range []string{
		"orchestration:\n  waiting_main_min_wait_sec: 600\n  waiting_main_max_wait_sec: 600\n",
		"orchestration:\n  waiting_main_min_wait_sec: 300\n  waiting_main_max_wait_sec: 3600\n",
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		writeTestFile(t, path, body)
		cfg, err := LoadConfigFromPath(path)
		if err != nil {
			t.Fatalf("LoadConfigFromPath(%q): %v", body, err)
		}
		if cfg.Orchestration.WaitingMainMinWaitSec <= 0 || cfg.Orchestration.WaitingMainMaxWaitSec < cfg.Orchestration.WaitingMainMinWaitSec {
			t.Fatalf("loaded clocks = min %d max %d for %q, want both kept with max >= min", cfg.Orchestration.WaitingMainMinWaitSec, cfg.Orchestration.WaitingMainMaxWaitSec, body)
		}
	}
}

func TestLoadConfigOrchestrationZeroAndSingleSidedValuesKeepDocumentedSemantics(t *testing.T) {
	// Zero or negative scalars keep the built-in default, and a single-sided
	// waiting_main value relies on the documented fallback where the effective
	// maximum is never below the effective minimum (an explicit minimum above
	// the default maximum raises the effective maximum to match). None of
	// these may fail the load or be rewritten.
	body := "orchestration:\n  max_live_runtimes: 0\n  max_borrowed_runtimes: -1\n  max_bypass_runtimes: 0\n  subagent_compact_usage: 0\n  waiting_main_min_wait_sec: 7200\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, body)
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if got := cfg.Orchestration.EffectiveMaxLiveRuntimes(); got != DefaultMaxLiveRuntimes {
		t.Fatalf("EffectiveMaxLiveRuntimes = %d, want default %d", got, DefaultMaxLiveRuntimes)
	}
	if got := cfg.Orchestration.EffectiveMaxBorrowedRuntimes(); got != DefaultMaxBorrowedRuntimes {
		t.Fatalf("EffectiveMaxBorrowedRuntimes = %d, want default %d", got, DefaultMaxBorrowedRuntimes)
	}
	if got := cfg.Orchestration.EffectiveMaxBypassRuntimes(); got != DefaultMaxBypassRuntimes {
		t.Fatalf("EffectiveMaxBypassRuntimes = %d, want default %d", got, DefaultMaxBypassRuntimes)
	}
	if got := cfg.Orchestration.EffectiveSubAgentCompactUsage(); got != DefaultSubAgentCompactUsage {
		t.Fatalf("EffectiveSubAgentCompactUsage = %v, want default %v", got, DefaultSubAgentCompactUsage)
	}
	if got, want := cfg.Orchestration.EffectiveWaitingMainMinWait().Seconds(), float64(7200); got != want {
		t.Fatalf("EffectiveWaitingMainMinWait = %v, want %v", got, want)
	}
	if got, want := cfg.Orchestration.EffectiveWaitingMainMaxWait().Seconds(), float64(7200); got != want {
		t.Fatalf("EffectiveWaitingMainMaxWait = %v, want %v (effective max never below min)", got, want)
	}
}

func TestLoadConfigOrchestrationCompactUsageOutOfRangeFallsBackToDefault(t *testing.T) {
	// A subagent_compact_usage outside (0,1) — including exactly 1, which is
	// rejected here unlike context.compaction.threshold, and NaN/±Inf — is
	// reset to the unset state so the effective default applies, and reported
	// by the issue collector instead of silently riding through. The reset
	// also proves NaN/Inf did not survive: a NaN value compares unequal to 0.
	for _, value := range []string{"1.5", "1", "-0.5", ".nan", ".inf"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		writeTestFile(t, path, "orchestration:\n  subagent_compact_usage: "+value+"\n")
		cfg, err := LoadConfigFromPath(path)
		if err != nil {
			t.Fatalf("LoadConfigFromPath(usage %s): %v", value, err)
		}
		if cfg.Orchestration.SubAgentCompactUsage != 0 {
			t.Fatalf("subagent_compact_usage %s = %v after load, want reset to 0 (unset)", value, cfg.Orchestration.SubAgentCompactUsage)
		}
		if got := cfg.Orchestration.EffectiveSubAgentCompactUsage(); got != DefaultSubAgentCompactUsage {
			t.Fatalf("EffectiveSubAgentCompactUsage for %s = %v, want default %v", value, got, DefaultSubAgentCompactUsage)
		}
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, "orchestration:\n  subagent_compact_usage: 1.5\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	if joined := strings.Join(issues, "\n"); !strings.Contains(joined, "orchestration.subagent_compact_usage") {
		t.Fatalf("issues = %q, want an orchestration.subagent_compact_usage report", joined)
	}
}

func TestLoadConfigOrchestrationKeepsValidCompactUsage(t *testing.T) {
	for _, value := range []string{"0.4", "0.999", "0.001"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		writeTestFile(t, path, "orchestration:\n  subagent_compact_usage: "+value+"\n")
		cfg, err := LoadConfigFromPath(path)
		if err != nil {
			t.Fatalf("LoadConfigFromPath(usage %s): %v", value, err)
		}
		if got := cfg.Orchestration.SubAgentCompactUsage; got <= 0 || got >= 1 {
			t.Fatalf("subagent_compact_usage %s = %v after load, want it kept in (0,1)", value, got)
		}
	}
}

// A project layer zero on an orchestration scalar means "keep the inherited
// line". The project merge used to overlay it raw, so the global value was
// clobbered and the effective-value layer fell back to the built-in default.
func TestMergeProjectConfigOrchestrationZeroDoesNotClobberGlobalScalar(t *testing.T) {
	global := DefaultConfig()
	global.Orchestration.MaxLiveRuntimes = 8
	global.Orchestration.WaitingMainMaxWaitSec = 1800
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "orchestration:\n  max_live_runtimes: 0\n  waiting_main_max_wait_sec: 0\n")

	_, merged, err := MergeProjectConfig(global, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if got := merged.Orchestration.MaxLiveRuntimes; got != 8 {
		t.Fatalf("merged max_live_runtimes = %d, want the inherited global 8", got)
	}
	if got := merged.Orchestration.EffectiveMaxLiveRuntimes(); got != 8 {
		t.Fatalf("EffectiveMaxLiveRuntimes = %d, want inherited 8", got)
	}
	if got := merged.Orchestration.WaitingMainMaxWaitSec; got != 1800 {
		t.Fatalf("merged waiting_main_max_wait_sec = %d, want the inherited global 1800", got)
	}
}

func TestMergeProjectConfigOrchestrationNegativeScalarDoesNotClobberGlobal(t *testing.T) {
	global := DefaultConfig()
	global.Orchestration.MaxBypassRuntimes = 4
	global.Orchestration.MailboxMemoryMessages = 96
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "orchestration:\n  max_bypass_runtimes: -2\n  mailbox_memory_messages: -10\n")

	_, merged, err := MergeProjectConfig(global, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if got := merged.Orchestration.MaxBypassRuntimes; got != 4 {
		t.Fatalf("merged max_bypass_runtimes = %d, want inherited global 4", got)
	}
	if got := merged.Orchestration.MailboxMemoryMessages; got != 96 {
		t.Fatalf("merged mailbox_memory_messages = %d, want inherited global 96", got)
	}
}

func TestMergeProjectConfigOrchestrationStripsZeroLeafKeepsValidSibling(t *testing.T) {
	global := DefaultConfig()
	global.Orchestration.MaxLiveRuntimes = 8
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "orchestration:\n  max_live_runtimes: 0\n  max_borrowed_runtimes: 2\n")

	_, merged, err := MergeProjectConfig(global, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	if got := merged.Orchestration.MaxLiveRuntimes; got != 8 {
		t.Fatalf("merged max_live_runtimes = %d, want inherited global 8", got)
	}
	if got := merged.Orchestration.MaxBorrowedRuntimes; got != 2 {
		t.Fatalf("merged max_borrowed_runtimes = %d, want the valid project 2 kept", got)
	}
}

func TestMergeProjectConfigOrchestrationCompactUsageOverrideHandling(t *testing.T) {
	global := DefaultConfig()
	global.Orchestration.SubAgentCompactUsage = 0.6

	// A valid project fraction overrides the global line...
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "orchestration:\n  subagent_compact_usage: 0.4\n")
	_, merged, err := MergeProjectConfig(global, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig(valid): %v", err)
	}
	if got := merged.Orchestration.SubAgentCompactUsage; got != 0.4 {
		t.Fatalf("merged subagent_compact_usage = %v, want project 0.4", got)
	}

	// ...while out-of-range project fractions (0, 1.5, NaN) must not clobber
	// the global line: the runtime would otherwise silently fall back to the
	// built-in 0.8 instead of the inherited 0.6.
	for _, value := range []string{"0", "1.5", ".nan"} {
		projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
		writeTestFile(t, projectPath, "orchestration:\n  subagent_compact_usage: "+value+"\n")
		_, merged, err := MergeProjectConfig(global, projectPath)
		if err != nil {
			t.Fatalf("MergeProjectConfig(usage %s): %v", value, err)
		}
		if got := merged.Orchestration.SubAgentCompactUsage; got != 0.6 {
			t.Fatalf("merged subagent_compact_usage for %s = %v, want inherited global 0.6", value, got)
		}
	}
}

func TestMergeProjectConfigOrchestrationWaitingClockPairFails(t *testing.T) {
	// An inverted pair inside the project file is a contradiction on its own
	// and must fail the merge (like malformed project YAML) instead of being
	// silently clamped.
	projectPath := filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "orchestration:\n  waiting_main_min_wait_sec: 600\n  waiting_main_max_wait_sec: 300\n")
	_, _, err := MergeProjectConfig(DefaultConfig(), projectPath)
	if err == nil || !strings.Contains(err.Error(), "waiting_main_max_wait_sec 300 is below waiting_main_min_wait_sec 600") {
		t.Fatalf("MergeProjectConfig(inverted pair) error = %v, want the waiting_main clock inversion error", err)
	}

	// The inversion can also materialize only after merging, when the project
	// lowers the maximum below an explicit global minimum.
	global := DefaultConfig()
	global.Orchestration.WaitingMainMinWaitSec = 600
	projectPath = filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "orchestration:\n  waiting_main_max_wait_sec: 300\n")
	if _, _, err := MergeProjectConfig(global, projectPath); err == nil {
		t.Fatal("MergeProjectConfig(project max below global min) error = nil, want inversion error")
	}

	// The valid direction — a project raising the maximum above the global
	// minimum — keeps applying.
	projectPath = filepath.Join(t.TempDir(), ".chord", "config.yaml")
	writeTestFile(t, projectPath, "orchestration:\n  waiting_main_max_wait_sec: 3600\n")
	_, merged, err := MergeProjectConfig(global, projectPath)
	if err != nil {
		t.Fatalf("MergeProjectConfig(valid max): %v", err)
	}
	if got := merged.Orchestration.WaitingMainMaxWaitSec; got != 3600 {
		t.Fatalf("merged waiting_main_max_wait_sec = %d, want 3600", got)
	}
}
