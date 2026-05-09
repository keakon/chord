package tools

import (
	"fmt"
	"strings"
	"unicode"
)

type InteractiveCommandFinding struct {
	Command string
	Reason  string
	Hint    string
}

func (f *InteractiveCommandFinding) Error() error {
	if f == nil {
		return nil
	}
	if f.Hint == "" {
		return fmt.Errorf("interactive command rejected: %s", f.Reason)
	}
	return fmt.Errorf("interactive command rejected: %s. %s", f.Reason, f.Hint)
}

func DetectInteractiveShellCommand(command string) *InteractiveCommandFinding {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil
	}
	tokens := shellTokens(trimmed)
	if len(tokens) == 0 {
		return nil
	}
	if hasDirectTTYRedirection(tokens) {
		return interactiveFinding("/dev/tty", "direct /dev/tty redirection requires a controlling terminal", "Bash and Spawn run without an interactive TTY; remove /dev/tty redirection and provide input explicitly.")
	}
	if containsToken(tokens, "stty") {
		return interactiveFinding("stty", "stty requires a terminal", "Run terminal configuration commands manually in a real terminal.")
	}
	commands := splitShellCommandTokens(tokens)
	for _, cmd := range commands {
		if finding := detectInteractiveCommandTokens(cmd); finding != nil {
			return finding
		}
	}
	return nil
}

func detectInteractiveCommandTokens(tokens []string) *InteractiveCommandFinding {
	if len(tokens) == 0 {
		return nil
	}
	first := tokens[0]
	if isAssignment(first) && len(tokens) > 1 {
		return detectInteractiveCommandTokens(tokens[1:])
	}
	name := commandBase(first)
	if name == "" {
		return nil
	}

	if isFullScreenCommand(name) {
		return interactiveFinding(name, fmt.Sprintf("`%s` requires an interactive terminal UI", name), "Run this manually in a terminal, or use a non-interactive alternative such as cat/grep/sed where appropriate.")
	}

	switch name {
	case "sudo":
		if hasOption(tokens[1:], "-n", "") || hasOption(tokens[1:], "--non-interactive", "") {
			return nil
		}
		return interactiveFinding("sudo", "`sudo` may prompt for a password", "Use `sudo -n` for non-interactive failure, configure passwordless automation, or run this manually in a terminal.")
	case "ssh", "sftp", "ftp", "telnet", "su", "passwd":
		return interactiveFinding(name, fmt.Sprintf("`%s` may require login, password, or terminal interaction", name), "Run this manually in a terminal or use a non-interactive authentication method.")
	case "git":
		return detectInteractiveGit(tokens)
	case "docker", "podman":
		return detectInteractiveContainerCommand(name, tokens)
	case "kubectl":
		return detectInteractiveKubectl(tokens)
	case "gh":
		if len(tokens) >= 3 && tokens[1] == "auth" && tokens[2] == "login" {
			return interactiveFinding("gh auth login", "`gh auth login` starts an authentication wizard", "Run it manually in a terminal, or provide authentication non-interactively via environment/token configuration.")
		}
	case "gcloud":
		if len(tokens) >= 3 && tokens[1] == "auth" && tokens[2] == "login" {
			return interactiveFinding("gcloud auth login", "`gcloud auth login` starts an authentication wizard", "Run it manually in a terminal or use non-interactive service-account authentication.")
		}
	case "az":
		if len(tokens) >= 2 && tokens[1] == "login" {
			return interactiveFinding("az login", "`az login` starts an authentication wizard", "Run it manually in a terminal or use a non-interactive service-principal/device-code flow outside Bash/Spawn.")
		}
	case "aws":
		if len(tokens) >= 2 && tokens[1] == "configure" {
			return interactiveFinding("aws configure", "`aws configure` prompts for credentials and configuration", "Set AWS_* environment variables or write config files explicitly instead.")
		}
	case "npm", "pnpm", "yarn":
		if len(tokens) >= 2 && tokens[1] == "init" && !hasYesFlag(tokens[2:]) {
			return interactiveFinding(name+" init", fmt.Sprintf("`%s init` may prompt for package metadata", name), fmt.Sprintf("Use `%s init -y`/`%s init --yes` or provide all required options explicitly.", name, name))
		}
	case "bun":
		if len(tokens) >= 2 && tokens[1] == "init" {
			return interactiveFinding("bun init", "`bun init` may prompt for project setup", "Run it manually in a terminal or use a non-interactive project template/setup command.")
		}
	case "cargo":
		if len(tokens) >= 2 && tokens[1] == "login" {
			return interactiveFinding("cargo login", "`cargo login` may prompt for a token", "Use `cargo login <token>` only when the token is provided explicitly and safely, or run it manually in a terminal.")
		}
	case "read", "select":
		return interactiveFinding(name, fmt.Sprintf("shell builtin `%s` waits for user input", name), "Provide input explicitly with a pipe or here-doc, or rewrite the command to avoid prompting.")
	}
	return nil
}

func detectInteractiveGit(tokens []string) *InteractiveCommandFinding {
	sub, args, ok := gitSubcommand(tokens)
	if !ok {
		return nil
	}
	switch sub {
	case "commit":
		if hasOption(args, "-p", "--patch") || hasOption(args, "", "--interactive") {
			return interactiveFinding("git commit --patch", "`git commit --patch` is an interactive patch workflow", "Run it manually in a terminal or commit explicit pathspecs/options non-interactively.")
		}
		if !gitCommitAvoidsEditor(args) {
			return interactiveFinding("git commit", "`git commit` without an explicit message or no-edit/reuse-message option opens an editor", "Use `git commit -m <message>`, `git commit -F <file>`, `git commit --amend --no-edit`, or `git commit -C <commit>`.")
		}
	case "rebase":
		if hasOption(args, "-i", "--interactive") || hasOption(args, "", "--edit-todo") {
			return interactiveFinding("git rebase -i", "`git rebase -i` requires an editor", "Run it manually in a terminal, or use non-interactive git commands.")
		}
	case "add", "checkout", "restore", "reset", "stash":
		if hasOption(args, "-p", "--patch") {
			return interactiveFinding("git "+sub+" -p", fmt.Sprintf("`git %s -p` is an interactive patch workflow", sub), "Run it manually in a terminal or use non-interactive pathspecs/options.")
		}
		if sub == "add" && hasOption(args, "-i", "--interactive") {
			return interactiveFinding("git add -i", "`git add -i` is interactive", "Run it manually in a terminal or use non-interactive pathspecs/options.")
		}
	case "clean":
		if hasOption(args, "-i", "--interactive") {
			return interactiveFinding("git clean -i", "`git clean -i` is interactive", "Run it manually in a terminal or use explicit non-interactive clean options.")
		}
	case "difftool", "mergetool":
		return interactiveFinding("git "+sub, fmt.Sprintf("`git %s` launches an interactive tool", sub), "Run it manually in a terminal or use plain git diff/merge commands.")
	}
	return nil
}

func detectInteractiveContainerCommand(name string, tokens []string) *InteractiveCommandFinding {
	sub, args, ok := commandSubcommand(tokens, containerGlobalOptionsWithValue)
	if !ok {
		return nil
	}
	switch sub {
	case "exec", "run", "start":
		if hasTTYOptionBeforeContainerCommand(args) {
			return interactiveFinding(name+" "+sub+" -t", fmt.Sprintf("`%s %s -t` allocates a TTY", name, sub), "Remove -t/--tty for non-interactive execution, or run the command manually in a terminal.")
		}
	case "login":
		return interactiveFinding(name+" login", fmt.Sprintf("`%s login` may prompt for credentials", name), "Use non-interactive credential input such as --password-stdin where supported, or run it manually in a terminal.")
	}
	return nil
}

func detectInteractiveKubectl(tokens []string) *InteractiveCommandFinding {
	sub, args, ok := commandSubcommand(tokens, kubectlGlobalOptionsWithValue)
	if !ok {
		return nil
	}
	if (sub == "exec" || sub == "run" || sub == "attach") && hasTTYOption(args) {
		return interactiveFinding("kubectl "+sub+" -t", fmt.Sprintf("`kubectl %s -t` allocates a TTY", sub), "Remove -t/--tty for non-interactive execution, or run the command manually in a terminal.")
	}
	return nil
}
func gitSubcommand(tokens []string) (string, []string, bool) {
	return commandSubcommand(tokens, gitGlobalOptionsWithValue)
}

func commandSubcommand(tokens []string, optionsWithValue map[string]bool) (string, []string, bool) {
	for i := 1; i < len(tokens); i++ {
		arg := tokens[i]
		if arg == "--" {
			if i+1 < len(tokens) {
				return tokens[i+1], tokens[i+2:], true
			}
			return "", nil, false
		}
		if optionsWithValue[arg] {
			i++
			continue
		}
		if optionHasInlineValue(arg, optionsWithValue) {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg, tokens[i+1:], true
	}
	return "", nil, false
}

var gitGlobalOptionsWithValue = map[string]bool{
	"-C":          true,
	"-c":          true,
	"--exec-path": true,
	"--git-dir":   true,
	"--work-tree": true,
	"--namespace": true,
}

var containerGlobalOptionsWithValue = map[string]bool{
	"-c":           true,
	"--config":     true,
	"--context":    true,
	"-H":           true,
	"--host":       true,
	"--log-level":  true,
	"--tlscacert":  true,
	"--tlscert":    true,
	"--tlskey":     true,
	"--connection": true,
	"--url":        true,
	"--identity":   true,
}

var kubectlGlobalOptionsWithValue = map[string]bool{
	"--as":                    true,
	"--as-group":              true,
	"--as-uid":                true,
	"--cache-dir":             true,
	"--certificate-authority": true,
	"--client-certificate":    true,
	"--client-key":            true,
	"--cluster":               true,
	"--context":               true,
	"--kubeconfig":            true,
	"--log-flush-frequency":   true,
	"--match-server-version":  true,
	"-n":                      true,
	"--namespace":             true,
	"--password":              true,
	"--profile":               true,
	"--profile-output":        true,
	"--request-timeout":       true,
	"-s":                      true,
	"--server":                true,
	"--tls-server-name":       true,
	"--token":                 true,
	"--user":                  true,
	"--username":              true,
}

func optionHasInlineValue(arg string, optionsWithValue map[string]bool) bool {
	for opt := range optionsWithValue {
		if strings.HasPrefix(opt, "--") {
			if strings.HasPrefix(arg, opt+"=") {
				return true
			}
			continue
		}
		if strings.HasPrefix(arg, opt) && len(arg) > len(opt) {
			return true
		}
	}
	return false
}

func interactiveFinding(command, reason, hint string) *InteractiveCommandFinding {
	return &InteractiveCommandFinding{Command: command, Reason: reason, Hint: hint}
}

func isFullScreenCommand(name string) bool {
	switch name {
	case "vi", "vim", "nvim", "nano", "emacs", "less", "more", "man", "top", "htop", "watch", "fzf":
		return true
	default:
		return false
	}
}

func commandBase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func containsToken(tokens []string, want string) bool {
	for _, tok := range tokens {
		if commandBase(tok) == want {
			return true
		}
	}
	return false
}

func gitCommitAvoidsEditor(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "-m" || arg == "-F" || arg == "--message" || arg == "--file" || arg == "-C" || arg == "--reuse-message" || arg == "--no-edit" {
			return true
		}
		if strings.HasPrefix(arg, "-m") && len(arg) > 2 {
			return true
		}
		if strings.HasPrefix(arg, "-F") && len(arg) > 2 {
			return true
		}
		if strings.HasPrefix(arg, "-C") && len(arg) > 2 {
			return true
		}
		if strings.HasPrefix(arg, "--message=") || strings.HasPrefix(arg, "--file=") || strings.HasPrefix(arg, "--reuse-message=") {
			return true
		}
	}
	return false
}

func hasTTYOptionBeforeContainerCommand(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if isTTYOption(arg) {
			return true
		}
		if strings.HasPrefix(arg, "-") {
			if containerOptionConsumesValue(arg) && !strings.Contains(arg, "=") {
				i++
			}
			continue
		}
		return false
	}
	return false
}

func hasTTYOption(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if isTTYOption(arg) {
			return true
		}
	}
	return false
}

func isTTYOption(arg string) bool {
	if arg == "--tty" || strings.HasPrefix(arg, "--tty=") {
		return true
	}
	if arg == "--interactive" || strings.HasPrefix(arg, "--interactive=") {
		return false
	}
	if strings.HasPrefix(arg, "--") {
		return false
	}
	return strings.HasPrefix(arg, "-") && strings.Contains(arg[1:], "t")
}

func containerOptionConsumesValue(arg string) bool {
	return arg == "--add-host" || arg == "--annotation" || arg == "--attach" || arg == "-a" ||
		arg == "--blkio-weight" || arg == "--blkio-weight-device" || arg == "--cap-add" || arg == "--cap-drop" ||
		arg == "--cgroup-parent" || arg == "--cidfile" || arg == "--cpu-period" || arg == "--cpu-quota" ||
		arg == "--cpuset-cpus" || arg == "--cpuset-mems" || arg == "--cpu-shares" || arg == "--detach-keys" ||
		arg == "--device" || arg == "--device-cgroup-rule" || arg == "--device-read-bps" || arg == "--device-read-iops" ||
		arg == "--device-write-bps" || arg == "--device-write-iops" || arg == "--dns" || arg == "--dns-option" ||
		arg == "--dns-search" || arg == "--entrypoint" || arg == "--env" || arg == "-e" || arg == "--env-file" ||
		arg == "--expose" || arg == "--gpus" || arg == "--group-add" || arg == "--health-cmd" || arg == "--health-interval" ||
		arg == "--health-retries" || arg == "--health-start-interval" || arg == "--health-start-period" || arg == "--health-timeout" ||
		arg == "--hostname" || arg == "-h" || arg == "--init-path" || arg == "--io-maxbandwidth" || arg == "--io-maxiops" ||
		arg == "--ip" || arg == "--ip6" || arg == "--ipc" || arg == "--isolation" || arg == "--kernel-memory" ||
		arg == "--label" || arg == "-l" || arg == "--label-file" || arg == "--link" || arg == "--link-local-ip" ||
		arg == "--log-driver" || arg == "--log-opt" || arg == "--mac-address" || arg == "--memory" || arg == "-m" ||
		arg == "--memory-reservation" || arg == "--memory-swap" || arg == "--memory-swappiness" || arg == "--mount" ||
		arg == "--name" || arg == "--network" || arg == "--network-alias" || arg == "--oom-score-adj" ||
		arg == "--pid" || arg == "--platform" || arg == "--publish" || arg == "-p" || arg == "--pull" || arg == "--restart" ||
		arg == "--runtime" || arg == "--security-opt" || arg == "--shm-size" || arg == "--stop-signal" ||
		arg == "--stop-timeout" || arg == "--storage-opt" || arg == "--sysctl" || arg == "--tmpfs" || arg == "--ulimit" ||
		arg == "--user" || arg == "-u" || arg == "--userns" || arg == "--uts" || arg == "--volume" || arg == "-v" ||
		arg == "--volumes-from" || arg == "--workdir" || arg == "-w"
}

func hasYesFlag(args []string) bool {
	return hasOption(args, "-y", "--yes")
}

func hasOption(args []string, short, long string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if short != "" {
			if arg == short || strings.HasPrefix(arg, short) && len(arg) > len(short) && len(short) == 2 && strings.HasPrefix(short, "-") && !strings.HasPrefix(arg, "--") {
				return true
			}
		}
		if long != "" && (arg == long || strings.HasPrefix(arg, long+"=")) {
			return true
		}
	}
	return false
}

func splitShellCommandTokens(tokens []string) [][]string {
	var commands [][]string
	var current []string
	skipNext := false
	for _, tok := range tokens {
		if skipNext {
			skipNext = false
			continue
		}
		switch tok {
		case "|", "&&", "||", ";", "&", "(", ")":
			if len(current) > 0 {
				commands = append(commands, current)
				current = nil
			}
		case "<", ">", ">>", "2>", "2>>", "&>", "&>>", "<<<", "<<":
			if len(current) > 0 {
				commands = append(commands, current)
			}
			current = nil
			skipNext = true
		default:
			if isRedirectionToken(tok) {
				if len(current) > 0 {
					commands = append(commands, current)
				}
				current = nil
				continue
			}
			current = append(current, tok)
		}
	}
	if len(current) > 0 {
		commands = append(commands, current)
	}
	return commands
}

func isRedirectionToken(tok string) bool {
	return strings.HasPrefix(tok, "<") || strings.HasPrefix(tok, ">") || strings.HasPrefix(tok, "2>") || strings.HasPrefix(tok, "&>")
}

func hasDirectTTYRedirection(tokens []string) bool {
	for i := 1; i < len(tokens); i++ {
		if tokens[i] != "/dev/tty" {
			continue
		}
		if isTTYRedirectionOperator(tokens[i-1]) {
			return true
		}
	}
	return false
}

func isTTYRedirectionOperator(tok string) bool {
	switch tok {
	case "<", ">", ">>", "&>", "&>>":
		return true
	}
	if tok == "" {
		return false
	}
	i := 0
	for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
		i++
	}
	if i == 0 || i == len(tok) {
		return false
	}
	op := tok[i:]
	switch op {
	case "<", ">", ">>":
		return true
	default:
		return false
	}
}

func isAssignment(tok string) bool {
	if tok == "" || tok[0] == '=' {
		return false
	}
	idx := strings.IndexByte(tok, '=')
	if idx <= 0 {
		return false
	}
	for i, r := range tok[:idx] {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func shellTokens(s string) []string {
	var tokens []string
	var b strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if quote == 0 && r == '\\' {
			escaped = true
			continue
		}
		if quote == 0 && unicode.IsSpace(r) {
			flush()
			continue
		}
		if quote == 0 && isShellOperatorRune(r) {
			flush()
			tokens = append(tokens, string(r))
			continue
		}
		if quote == 0 && (r == '\'' || r == '"') {
			quote = r
			continue
		}
		if quote != 0 && r == quote {
			quote = 0
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		b.WriteRune('\\')
	}
	flush()
	return combineShellOperators(tokens)
}

func isShellOperatorRune(r rune) bool {
	switch r {
	case '|', '&', ';', '<', '>', '(', ')':
		return true
	default:
		return false
	}
}

func combineShellOperators(tokens []string) []string {
	if len(tokens) < 2 {
		return tokens
	}
	out := make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		if i+1 < len(tokens) {
			two := tokens[i] + tokens[i+1]
			switch two {
			case "&&", "||", ">>", "<<", "&>":
				out = append(out, two)
				i++
				continue
			}
		}
		out = append(out, tokens[i])
	}
	return out
}
