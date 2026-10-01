package config

import (
	"strings"
	"testing"
)

// collectTestAdvisories writes content as a global config and returns its
// advisories, failing if any of them leaked into the issue channel.
func collectTestAdvisories(t *testing.T, content string) string {
	t.Helper()
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", content)
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	advisories := Advisories(cfg)
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %q, want advisories kept out of the issue channel", issues)
	}
	return strings.Join(advisories, "\n")
}

// A chat-completions model that relies on a native_thinking selector it does
// not set is reported as an advisory: the value still loads, only the request
// will not carry what the config asked for.
func TestAdvisoriesNativeThinkingSelector(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		want    []string
		wantNot bool
	}{
		{
			name: "gemini 3 without thinking block is reported",
			config: `providers:
  sample:
    type: chat-completions
    models:
      google/gemini-3-pro-preview:
`,
			want: []string{`"google/gemini-3-pro-preview"`, "rejects follow-up tool calls", "native_thinking: gemini-3"},
		},
		{
			name: "chat type inferred from api_url is reported",
			config: `providers:
  sample:
    api_url: https://example.invalid/v1/chat/completions
    models:
      gemini-3-flash:
`,
			want: []string{`"gemini-3-flash"`, "native_thinking: gemini-3"},
		},
		{
			name: "claude with thinking is reported",
			config: `providers:
  sample:
    type: chat-completions
    models:
      claude-model-1:
        thinking:
          type: adaptive
`,
			want: []string{`"claude-model-1"`, "left out of the request", "native_thinking: anthropic"},
		},
		{
			name: "unknown family with thinking in a variant is reported",
			config: `providers:
  sample:
    type: chat-completions
    models:
      qwen3-plus:
        variants:
          think:
            thinking:
              type: enabled
`,
			want: []string{`"qwen3-plus"`, "left out of the request"},
		},
		{
			name: "model without thinking block stays quiet",
			config: `providers:
  sample:
    type: chat-completions
    models:
      qwen3-plus:
      kimi-k3:
      alibaba-model-1:
`,
			wantNot: true,
		},
		{
			name: "disabled thinking stays quiet",
			config: `providers:
  sample:
    type: chat-completions
    models:
      claude-model-1:
        thinking:
          type: disabled
`,
			wantNot: true,
		},
		{
			name: "provider-level selector stays quiet",
			config: `providers:
  sample:
    type: chat-completions
    compat:
      chat_completions:
        native_thinking: gemini-3
    models:
      gemini-3-flash:
`,
			wantNot: true,
		},
		{
			name: "deepseek name keeps its shortcut",
			config: `providers:
  sample:
    type: chat-completions
    models:
      deepseek-v4-flash:
        thinking:
          type: enabled
`,
			wantNot: true,
		},
		{
			name: "explicit deepseek contract on an alias stays quiet",
			config: `providers:
  sample:
    type: chat-completions
    compat:
      reasoning_continuity:
        contract: deepseek
    models:
      deployment-a:
        thinking:
          type: enabled
`,
			wantNot: true,
		},
		{
			name: "deepseek name opted out of the contract is reported",
			config: `providers:
  sample:
    type: chat-completions
    compat:
      reasoning_continuity:
        contract: deepseek
    models:
      deepseek-v4-flash:
        thinking:
          type: enabled
        compat:
          reasoning_continuity:
            contract: none
`,
			want: []string{`"deepseek-v4-flash"`, "left out of the request"},
		},
		{
			name: "native endpoint needs no selector",
			config: `providers:
  sample:
    api_url: https://example.invalid/v1beta/models
    models:
      gemini-3-flash:
        thinking:
          level: high
`,
			wantNot: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			joined := collectTestAdvisories(t, tt.config)
			if tt.wantNot {
				if joined != "" {
					t.Fatalf("advisories = %q, want none", joined)
				}
				return
			}
			for _, want := range tt.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("advisories = %q, want %q", joined, want)
				}
			}
		})
	}
}

// An invalid selector is reported as an issue and reset to unset; the
// effective config then lacks a selector, so the advisory names the one to set.
func TestAdvisoriesNameSelectorAfterInvalidReset(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", `providers:
  sample:
    type: chat-completions
    models:
      gemini-3-flash:
        compat:
          chat_completions:
            native_thinking: auto
`)
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if joined := strings.Join(Advisories(cfg), "\n"); !strings.Contains(joined, "native_thinking: gemini-3") {
		t.Fatalf("advisories = %q, want the gemini-3 selector named", joined)
	}
}

// A gemini-3 contract only activates on the native generate-content endpoint
// or on a chat-completions route that also pins native_thinking to gemini-3;
// a placement that can never activate it is an advisory, not a dropped value.
func TestAdvisoriesInertGemini3Contract(t *testing.T) {
	t.Run("messages provider is reported", func(t *testing.T) {
		joined := collectTestAdvisories(t, `providers:
  sample:
    type: messages
    models:
      model-1:
        compat:
          reasoning_continuity:
            contract: gemini-3
`)
		if !strings.Contains(joined, `reasoning_continuity contract "gemini-3"`) || !strings.Contains(joined, "never activates") {
			t.Fatalf("advisories = %q, want the inert gemini-3 contract reported", joined)
		}
	})

	t.Run("messages type inferred from api_url is reported", func(t *testing.T) {
		joined := collectTestAdvisories(t, `providers:
  sample:
    api_url: https://example.invalid/v1/messages
    models:
      model-1:
        compat:
          reasoning_continuity:
            contract: gemini-3
`)
		if !strings.Contains(joined, "never activates") {
			t.Fatalf("advisories = %q, want the inert gemini-3 contract reported", joined)
		}
	})

	t.Run("chat-completions with the gemini-3 pin is accepted", func(t *testing.T) {
		joined := collectTestAdvisories(t, `providers:
  sample:
    type: chat-completions
    compat:
      reasoning_continuity:
        contract: gemini-3
    models:
      model-1:
        compat:
          chat_completions:
            native_thinking: gemini-3
`)
		if joined != "" {
			t.Fatalf("advisories = %q, want none for the pinned chat route", joined)
		}
	})

	t.Run("generate-content provider is accepted", func(t *testing.T) {
		joined := collectTestAdvisories(t, `providers:
  sample:
    type: generate-content
    models:
      model-1:
        compat:
          reasoning_continuity:
            contract: gemini-3
`)
		if joined != "" {
			t.Fatalf("advisories = %q, want none for the native endpoint", joined)
		}
	})
}

// Advisories read the effective config: a selector or provider type set in the
// global layer decides what a model the project layer adds needs.
func TestAdvisoriesReadMergedLayers(t *testing.T) {
	dir := t.TempDir()
	advise := func(t *testing.T, global, project string) string {
		t.Helper()
		cfg, err := LoadConfigFromPath(writeIssueTestConfig(t, dir, "global.yaml", global))
		if err != nil {
			t.Fatalf("LoadConfigFromPath: %v", err)
		}
		_, merged, err := MergeProjectConfig(cfg, writeIssueTestConfig(t, dir, "project.yaml", project))
		if err != nil {
			t.Fatalf("MergeProjectConfig: %v", err)
		}
		return strings.Join(Advisories(merged), "\n")
	}

	t.Run("global selector covers a project model", func(t *testing.T) {
		got := advise(t, `providers:
  sample:
    type: chat-completions
    compat:
      chat_completions:
        native_thinking: gemini-3
`, `providers:
  sample:
    type: chat-completions
    models:
      gemini-3-flash:
`)
		if got != "" {
			t.Fatalf("advisories = %q, want none when the global provider sets the selector", got)
		}
	})

	t.Run("global type applies to a project model", func(t *testing.T) {
		got := advise(t, `providers:
  sample:
    type: chat-completions
`, `providers:
  sample:
    models:
      gemini-3-flash:
`)
		if !strings.Contains(got, `"gemini-3-flash"`) || !strings.Contains(got, "native_thinking: gemini-3") {
			t.Fatalf("advisories = %q, want the project model reported under the global chat type", got)
		}
	})
}
