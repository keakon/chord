package tools

import "testing"

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
