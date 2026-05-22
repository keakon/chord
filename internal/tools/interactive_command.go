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
	trimmed = stripHereDocBodies(trimmed)
	tokens := shellTokens(trimmed)
	if len(tokens) == 0 {
		return nil
	}
	if hasDirectTTYRedirection(tokens) {
		return interactiveFinding("/dev/tty", "direct /dev/tty redirection requires a controlling terminal", "Shell and Spawn run without an interactive TTY; remove /dev/tty redirection and provide input explicitly.")
	}
	commands := splitShellCommandTokensWithContext(tokens)
	for _, cmd := range commands {
		if finding := detectInteractiveCommandTokens(cmd.tokens, cmd.hasPipelineInput); finding != nil {
			return finding
		}
	}
	return nil
}

func detectInteractiveCommandTokens(tokens []string, hasPipelineInput bool) *InteractiveCommandFinding {
	if len(tokens) == 0 {
		return nil
	}
	env := make(map[string]string)
	for len(tokens) > 0 && isAssignment(tokens[0]) {
		parts := strings.SplitN(tokens[0], "=", 2)
		env[parts[0]] = parts[1]
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return nil
	}
	first := tokens[0]
	name := commandBase(first)
	if name == "" {
		return nil
	}

	if isFullScreenCommand(name) {
		return interactiveFinding(name, fmt.Sprintf("`%s` requires an interactive terminal UI", name), "Run this manually in a terminal, or use a non-interactive alternative such as cat/grep/sed where appropriate.")
	}

	switch name {
	case "stty":
		return interactiveFinding("stty", "stty requires a terminal", "Run terminal configuration commands manually in a real terminal.")
	case "tput":
		return interactiveFinding("tput", "tput requires a terminal", "Use explicit terminal capability values in a real terminal, or avoid terminal-dependent queries in Shell/Spawn.")
	case "sudo":
		if hasOption(tokens[1:], "-n", "") || hasOption(tokens[1:], "--non-interactive", "") {
			return nil
		}
		return interactiveFinding("sudo", "`sudo` may prompt for a password", "Use `sudo -n` for non-interactive failure, configure passwordless automation, or run this manually in a terminal.")
	case "ssh":
		return detectInteractiveSSH(tokens)
	case "sftp", "ftp", "telnet", "su", "passwd":
		return interactiveFinding(name, fmt.Sprintf("`%s` may require login, password, or terminal interaction", name), "Run this manually in a terminal or use a non-interactive authentication method.")
	case "git":
		return detectInteractiveGit(tokens, env, hasPipelineInput)
	case "docker", "podman":
		return detectInteractiveContainerCommand(name, tokens)
	case "kubectl":
		return detectInteractiveKubectl(tokens)
	case "gh":
		if len(tokens) >= 3 && tokens[1] == "auth" && tokens[2] == "login" && !hasOption(tokens[3:], "", "--with-token") {
			return interactiveFinding("gh auth login", "`gh auth login` starts an authentication wizard", "Run it manually in a terminal, or provide authentication non-interactively via environment/token configuration.")
		}
	case "gcloud":
		if len(tokens) >= 3 && tokens[1] == "auth" && tokens[2] == "login" {
			return interactiveFinding("gcloud auth login", "`gcloud auth login` starts an authentication wizard", "Run it manually in a terminal or use non-interactive service-account authentication.")
		}
	case "az":
		if len(tokens) >= 2 && tokens[1] == "login" && !hasOption(tokens[2:], "", "--service-principal") {
			return interactiveFinding("az login", "`az login` starts an authentication wizard", "Run it manually in a terminal or use a non-interactive service-principal/device-code flow outside Shell/Spawn.")
		}
	case "aws":
		if len(tokens) == 2 && tokens[1] == "configure" {
			return interactiveFinding("aws configure", "`aws configure` prompts for credentials and configuration", "Set AWS_* environment variables or write config files explicitly instead.")
		}
	}
	return nil
}

func detectInteractiveSSH(tokens []string) *InteractiveCommandFinding {
	if hasOption(tokens[1:], "-t", "") {
		return interactiveFinding("ssh -t", "`ssh -t` allocates a TTY", "Remove TTY allocation for non-interactive execution, or run the command manually in a terminal.")
	}
	hostIndex := -1
	for i := 1; i < len(tokens); i++ {
		arg := tokens[i]
		if arg == "--" {
			if i+1 < len(tokens) {
				hostIndex = i + 1
			}
			break
		}
		if sshOptionConsumesValue(arg) && !strings.Contains(arg, "=") {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		hostIndex = i
		break
	}
	if hostIndex >= 0 && hostIndex == len(tokens)-1 {
		return interactiveFinding("ssh", "`ssh` without a remote command starts an interactive login session", "Run it manually in a terminal, or provide a remote command and non-interactive authentication/options.")
	}
	return nil
}

func sshOptionConsumesValue(arg string) bool {
	return arg == "-b" || arg == "-c" || arg == "-D" || arg == "-E" || arg == "-e" || arg == "-F" || arg == "-I" || arg == "-i" ||
		arg == "-J" || arg == "-L" || arg == "-l" || arg == "-m" || arg == "-O" || arg == "-o" || arg == "-p" || arg == "-Q" ||
		arg == "-R" || arg == "-S" || arg == "-W" || arg == "-w"
}

func detectInteractiveGit(tokens []string, env map[string]string, hasPipelineInput bool) *InteractiveCommandFinding {
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
			if gitEditorAvoidsPrompt(env) {
				return nil
			}
			return interactiveFinding("git rebase -i", "`git rebase -i` requires an editor", "Run it manually in a terminal, or use non-interactive git commands.")
		}
	case "add", "checkout", "restore", "reset", "stash":
		if hasOption(args, "-p", "--patch") && !hasPipelineInput {
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

func gitCommitAvoidsEditor(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "-m" || arg == "-F" || arg == "--message" || arg == "--file" || arg == "-C" || arg == "--reuse-message" || arg == "--no-edit" {
			return true
		}
		if arg == "--fixup" {
			if i+1 < len(args) && isNonInteractiveGitFixupValue(args[i+1]) {
				return true
			}
			continue
		}
		if strings.HasPrefix(arg, "--fixup=") {
			if isNonInteractiveGitFixupValue(strings.TrimPrefix(arg, "--fixup=")) {
				return true
			}
		}
		if strings.HasPrefix(arg, "--message=") || strings.HasPrefix(arg, "--file=") || strings.HasPrefix(arg, "--reuse-message=") {
			return true
		}
		if len(arg) > 2 && strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
			short := arg[1:]
			if strings.ContainsRune(short, 'm') || strings.ContainsRune(short, 'F') || strings.ContainsRune(short, 'C') {
				return true
			}
		}
	}
	return false
}

func isNonInteractiveGitFixupValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(value, "amend:") && !strings.HasPrefix(value, "reword:")
}

func gitEditorAvoidsPrompt(env map[string]string) bool {
	for _, name := range []string{"GIT_SEQUENCE_EDITOR", "GIT_EDITOR"} {
		if shellCommandAvoidsPrompt(env[name]) {
			return true
		}
	}
	return false
}

func shellCommandAvoidsPrompt(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	value = stripHereDocBodies(value)
	tokens := shellTokens(value)
	if len(tokens) == 0 {
		return false
	}
	commands := splitShellCommandTokensWithContext(tokens)
	for _, cmd := range commands {
		if detectInteractiveCommandTokens(cmd.tokens, cmd.hasPipelineInput) != nil {
			return false
		}
	}
	return true
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

type shellCommandTokens struct {
	tokens           []string
	hasPipelineInput bool
}

func splitShellCommandTokens(tokens []string) [][]string {
	commandsWithContext := splitShellCommandTokensWithContext(tokens)
	commands := make([][]string, 0, len(commandsWithContext))
	for _, cmd := range commandsWithContext {
		commands = append(commands, cmd.tokens)
	}
	return commands
}

func splitShellCommandTokensWithContext(tokens []string) []shellCommandTokens {
	var commands []shellCommandTokens
	var current []string
	hasPipelineInput := false
	skipNext := false
	for _, tok := range tokens {
		if skipNext {
			skipNext = false
			continue
		}
		switch tok {
		case "|":
			if len(current) > 0 {
				commands = append(commands, shellCommandTokens{tokens: current, hasPipelineInput: hasPipelineInput})
				current = nil
			}
			hasPipelineInput = true
		case "&&", "||", ";", "&", "(", ")":
			if len(current) > 0 {
				commands = append(commands, shellCommandTokens{tokens: current, hasPipelineInput: hasPipelineInput})
				current = nil
			}
			hasPipelineInput = false
		case "<", ">", ">>", "2>", "2>>", "&>", "&>>", "<<<", "<<":
			if len(current) > 0 {
				commands = append(commands, shellCommandTokens{tokens: current, hasPipelineInput: hasPipelineInput})
			}
			current = nil
			skipNext = true
		default:
			if isRedirectionToken(tok) {
				if len(current) > 0 {
					commands = append(commands, shellCommandTokens{tokens: current, hasPipelineInput: hasPipelineInput})
				}
				current = nil
				continue
			}
			current = append(current, tok)
		}
	}
	if len(current) > 0 {
		commands = append(commands, shellCommandTokens{tokens: current, hasPipelineInput: hasPipelineInput})
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

func stripHereDocBodies(s string) string {
	lines := strings.SplitAfter(s, "\n")
	out := make([]string, 0, len(lines))
	pending := make([]string, 0, 1)
	for _, line := range lines {
		if len(pending) > 0 {
			trimmed := strings.TrimSpace(line)
			for len(pending) > 0 && trimmed == pending[0] {
				pending = pending[1:]
			}
			if len(pending) > 0 {
				continue
			}
			continue
		}
		out = append(out, line)
		pending = append(pending, hereDocDelimiters(line)...)
	}
	return strings.Join(out, "")
}

func hereDocDelimiters(line string) []string {
	tokens := shellTokens(line)
	var delimiters []string
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok != "<<" && !strings.HasPrefix(tok, "<<") {
			continue
		}
		var delimiter string
		if tok == "<<" {
			if i+1 >= len(tokens) {
				continue
			}
			delimiter = tokens[i+1]
			i++
		} else {
			delimiter = strings.TrimPrefix(tok, "<<")
		}
		delimiter = strings.TrimPrefix(delimiter, "-")
		delimiter = strings.Trim(delimiter, "'\"")
		if delimiter != "" {
			delimiters = append(delimiters, delimiter)
		}
	}
	return delimiters
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
