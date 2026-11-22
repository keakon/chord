package tools

import (
	"os"
	"slices"
	"strings"
)

var nonInteractiveEnv = []string{
	"GIT_TERMINAL_PROMPT=0",
	"GIT_EDITOR=true",
	"GIT_PAGER=cat",
	"VISUAL=true",
	"EDITOR=true",
	"PAGER=cat",
	"MANPAGER=cat",
	"GH_PROMPT_DISABLED=1",
}

// appendNonInteractiveEnv returns env with low-risk prompt/editor/pager settings
// that reinforce the non-interactive contract of the shell tool. The process
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

// bashFuncKeyPrefix is the environment-key prefix modern bash uses for an
// exported function (BASH_FUNC_<name>%%). Older bash instead exports the raw
// function body under the plain variable name, matched by value below.
const bashFuncKeyPrefix = "BASH_FUNC_"

// bashEnvKey names the file bash sources before running a non-interactive
// command; the command text alone therefore does not describe everything the
// invocation executes.
const bashEnvKey = "BASH_ENV"

const (
	bashShellOptionsKey = "SHELLOPTS"
	bashPS4Key          = "PS4"
)

// bashFuncValuePrefix is how an exported function's value starts, in both the
// modern BASH_FUNC_ encoding and the legacy raw-value form.
const bashFuncValuePrefix = "() {"

// stripBashDynamicEnv removes environment entries that could make a bash
// command execute something other than the program its text names:
//
//   - an exported function (BASH_FUNC_<name>%% on modern bash, or any entry
//     whose value starts with "() {" on bash 3.2) shadows the PATH binary of
//     the same name the moment bash imports the environment, so the command
//     text no longer describes what runs;
//   - BASH_ENV names a file bash sources before the command;
//   - SHELLOPTS=xtrace makes Bash expand PS4 before each traced command, and
//     that expansion can execute command substitutions.
//
// Read-only scheduling still assumes that PATH and other program-specific
// configuration are trusted. This filter is not a sandbox.
func stripBashDynamicEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, value, hasValue := strings.Cut(kv, "=")
		if strings.HasPrefix(key, bashFuncKeyPrefix) || key == bashEnvKey || key == bashPS4Key ||
			strings.HasPrefix(value, bashFuncValuePrefix) {
			continue
		}
		if key == bashShellOptionsKey && hasValue {
			options := slices.DeleteFunc(strings.Split(value, ":"), func(option string) bool { return option == "xtrace" })
			kv = key + "=" + strings.Join(options, ":")
		}
		out = append(out, kv)
	}
	return out
}

// shellSpawnEnv builds the child environment for one shell command spawn.
// readOnly marks an invocation the read-only classifier proved free of writes
// and process side effects (ShellTool.ConcurrencyPolicy); only such commands
// may run concurrently with other reads, so their environment additionally
// drops the bash dynamic-env vectors that could break the link between the
// reviewed command text and the executed program (see stripBashDynamicEnv).
func shellSpawnEnv(readOnly bool) []string {
	env := appendNonInteractiveEnv(nil)
	if readOnly {
		env = stripBashDynamicEnv(env)
	}
	return env
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
