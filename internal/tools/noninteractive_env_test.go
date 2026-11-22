package tools

import "testing"

func TestStripBashDynamicEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin:/bin",
		"GIT_EDITOR=vim",
		// Modern bash exported function: BASH_FUNC_<name>%% key.
		"BASH_FUNC_chordtest%%=() { echo shadowed; }",
		// Legacy bash 3.2 exported function: raw body as the value.
		"chordlegacy=() { echo legacy; }",
		// Sourced before every non-interactive command.
		"BASH_ENV=/tmp/chord-bashenv",
		"SHELLOPTS=xtrace:errexit:xtrace:pipefail:xtrace",
		// Lookalikes that must survive stripping.
		"BASH_ENVIRON=keep",
		"BASH_FUNC=keep",
		"PARTIAL=() {",
		"OTHERFUNC=plain value",
	}
	got := stripBashDynamicEnv(env)
	gotMap := envMap(got)
	for _, key := range []string{"BASH_FUNC_chordtest%%", "chordlegacy", "BASH_ENV", "PARTIAL"} {
		if _, ok := gotMap[key]; ok {
			t.Fatalf("stripBashDynamicEnv kept %q: %#v", key, got)
		}
	}
	want := map[string]string{
		"PATH":       "/usr/bin:/bin",
		"GIT_EDITOR": "vim",
		// The existing GIT_EDITOR-style override behavior is untouched.
		"BASH_ENVIRON": "keep",
		"BASH_FUNC":    "keep",
		"OTHERFUNC":    "plain value",
		"SHELLOPTS":    "errexit:pipefail",
	}
	for key, value := range want {
		if gotValue, ok := gotMap[key]; !ok || gotValue != value {
			t.Fatalf("env[%s] = %q, %v; want %q in %#v", key, gotValue, ok, value, got)
		}
	}
	// Entries without a separator pass through unchanged.
	if got := stripBashDynamicEnv([]string{"NOVALUE"}); len(got) != 1 || got[0] != "NOVALUE" {
		t.Fatalf("stripBashDynamicEnv({NOVALUE}) = %#v, want it unchanged", got)
	}
}

func TestShellSpawnEnvStripsBashDynamicEnvOnlyForReadOnly(t *testing.T) {
	inherited := map[string]string{
		bashEnvKey:                   "/tmp/chord-bashenv",
		bashFuncKeyPrefix + "test%%": "() { echo modern; }",
		"chordlegacy":                "() { echo legacy; }",
		bashShellOptionsKey:          "xtrace",
		bashPS4Key:                   "trace: ",
	}
	for key, value := range inherited {
		t.Setenv(key, value)
	}

	kept := envMap(shellSpawnEnv(false))
	stripped := envMap(shellSpawnEnv(true))
	for key, value := range inherited {
		if got, ok := kept[key]; !ok || got != value {
			t.Fatalf("non-read-only spawn did not preserve %q", key)
		}
		if key == bashShellOptionsKey {
			if got, ok := stripped[key]; !ok || got != "" {
				t.Fatalf("read-only spawn kept nonempty options: %q", got)
			}
			continue
		}
		if _, ok := stripped[key]; ok {
			t.Fatalf("read-only spawn kept %q", key)
		}
	}
	// The non-interactive overrides survive the stripping step.
	for key, value := range map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_PAGER": "cat", "EDITOR": "true"} {
		if gotValue, ok := stripped[key]; !ok || gotValue != value {
			t.Fatalf("readonly spawn env[%s] = %q, %v; want %q", key, gotValue, ok, value)
		}
	}
}

func TestAppendNonInteractiveEnvOverridesPromptEditorPager(t *testing.T) {
	got := appendNonInteractiveEnv([]string{
		"PATH=/bin",
		"GIT_EDITOR=vim",
		"PAGER=less",
	})
	want := map[string]string{
		"PATH":                "/bin",
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_EDITOR":          "true",
		"GIT_PAGER":           "cat",
		"VISUAL":              "true",
		"EDITOR":              "true",
		"PAGER":               "cat",
		"MANPAGER":            "cat",
		"GH_PROMPT_DISABLED":  "1",
	}
	for key, value := range want {
		if gotValue, ok := envMap(got)[key]; !ok || gotValue != value {
			t.Fatalf("env[%s] = %q, %v; want %q, true in %#v", key, gotValue, ok, value, got)
		}
	}
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		key := envKey(kv)
		out[key] = kv[len(key)+1:]
	}
	return out
}
