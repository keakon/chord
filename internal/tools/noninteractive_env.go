package tools

import "os"

var nonInteractiveEnv = []string{
	"GIT_TERMINAL_PROMPT=0",
	"GIT_EDITOR=true",
	"VISUAL=true",
	"EDITOR=true",
	"PAGER=cat",
	"MANPAGER=cat",
	"GH_PROMPT_DISABLED=1",
}

// appendNonInteractiveEnv returns env with low-risk prompt/editor/pager settings
// that reinforce the non-interactive contract of Bash and Spawn. The process
// stdin is intentionally not wired from the TUI; commands that need input should
// fail fast or be rewritten with explicit non-interactive flags/input.
func appendNonInteractiveEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := append([]string{}, env...)
	for _, kv := range nonInteractiveEnv {
		key := envKey(kv)
		out = appendEnvOverride(out, key, kv)
	}
	return out
}

func appendEnvOverride(env []string, key, kv string) []string {
	prefix := key + "="
	for i, existing := range env {
		if len(existing) >= len(prefix) && existing[:len(prefix)] == prefix {
			env[i] = kv
			return env
		}
	}
	return append(env, kv)
}

func envKey(kv string) string {
	for i, r := range kv {
		if r == '=' {
			return kv[:i]
		}
	}
	return kv
}
