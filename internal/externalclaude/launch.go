package externalclaude

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

var managedTools = []string{
	"Read", "Edit", "Write", "NotebookEdit", "Bash", "Glob", "Grep",
	"WebFetch", "WebSearch", "TodoWrite", "Agent", "AskUserQuestion", "ExitPlanMode",
}

func ManagedTool(name string) bool { return slices.Contains(managedTools, name) }

func ManagedToolNames() []string { return slices.Clone(managedTools) }

type launchArguments struct {
	args       []string
	disallowed []string
}

// ValidateLaunchArgs accepts only classified options; values are never included
// in errors because they can contain credentials.
func ValidateLaunchArgs(args []string) error {
	_, err := parseLaunchArgs(args)
	return err
}

func parseLaunchArgs(args []string) (launchArguments, error) {
	var parsed launchArguments
	for i := 0; i < len(args); i++ {
		name, value, inline := strings.Cut(args[i], "=")
		switch name {
		case "--include-partial-messages", "--replay-user-messages":
			if inline {
				return parsed, fmt.Errorf("launch option %s does not take a value", name)
			}
			parsed.args = append(parsed.args, name)
			continue
		case "--model", "--effort", "--max-turns", "--max-budget-usd",
			"--disallowedTools", "--disallowed-tools":
		default:
			return parsed, fmt.Errorf("launch option %s is not supported by the managed transport", optionName(name))
		}
		if !inline {
			i++
			if i == len(args) || strings.HasPrefix(args[i], "-") {
				return parsed, fmt.Errorf("launch option %s requires a value", name)
			}
			value = args[i]
		}
		if value == "" || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "-") {
			// The leading-dash check applies to `--flag=value` too: otherwise a
			// value could smuggle a second option into the argv BuildArgs
			// appends, where the CLI would read it as its own flag.
			return parsed, fmt.Errorf("launch option %s has an invalid value", name)
		}
		if name == "--disallowedTools" || name == "--disallowed-tools" {
			parsed.disallowed = append(parsed.disallowed, strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })...)
		} else {
			parsed.args = append(parsed.args, name, value)
		}
	}
	return parsed, nil
}

func optionName(name string) string {
	if !strings.HasPrefix(name, "-") {
		return "(positional argument)"
	}
	return name
}

func managedSettings() string {
	settings := struct {
		Permissions struct {
			Ask []string `json:"ask"`
		} `json:"permissions"`
		Sandbox struct {
			Enabled   bool `json:"enabled"`
			AutoAllow bool `json:"autoAllowBashIfSandboxed"`
		} `json:"sandbox"`
		DisableAllHooks bool `json:"disableAllHooks"`
	}{DisableAllHooks: true}
	settings.Permissions.Ask = managedTools
	data, _ := json.Marshal(settings)
	return string(data)
}

// ValidateEnvironment limits overrides to authentication, model and network
// settings. Runtime injection and automatic interrupted-turn replay are not
// ordinary model configuration.
func ValidateEnvironment(env map[string]string) error {
	for key, value := range env {
		if !safeEnvironmentKey(key) {
			return fmt.Errorf("environment override %s is not supported by the managed transport", key)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("environment override %s contains NUL", key)
		}
	}
	return nil
}

func safeEnvironmentKey(key string) bool {
	return slices.Contains([]string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
		"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "CLAUDE_CODE_OAUTH_TOKEN",
		"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY",
		"https_proxy", "http_proxy", "all_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	}, key)
}

func processEnvironment(inherited []string, overrides map[string]string) []string {
	env := make([]string, 0, len(inherited)+len(overrides))
	for _, entry := range inherited {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; replaced {
			continue
		}
		if strings.HasPrefix(key, "CLAUDE_") && !safeEnvironmentKey(key) ||
			strings.HasPrefix(key, "ANTHROPIC_") && !safeEnvironmentKey(key) ||
			strings.HasPrefix(key, "DYLD_") || strings.HasPrefix(key, "LD_") ||
			slices.Contains([]string{"NODE_OPTIONS", "BASH_ENV", "ENV"}, key) {
			continue
		}
		env = append(env, entry)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	env = append(env, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1")
	return env
}
