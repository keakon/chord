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
	if len(tokens) < 2 {
		return nil
	}
	sub := tokens[1]
	switch sub {
	case "commit":
		if !gitCommitHasMessage(tokens[2:]) {
			return interactiveFinding("git commit", "`git commit` without -m/-F opens an editor", "Use `git commit -m <message>` or `git commit -F <file>`.")
		}
	case "rebase":
		if hasOption(tokens[2:], "-i", "--interactive") {
			return interactiveFinding("git rebase -i", "`git rebase -i` requires an editor", "Run it manually in a terminal, or use non-interactive git commands.")
		}
	case "add", "checkout", "restore", "reset":
		if hasOption(tokens[2:], "-p", "--patch") {
			return interactiveFinding("git "+sub+" -p", fmt.Sprintf("`git %s -p` is an interactive patch workflow", sub), "Run it manually in a terminal or use non-interactive pathspecs/options.")
		}
	case "clean":
		if hasOption(tokens[2:], "-i", "--interactive") {
			return interactiveFinding("git clean -i", "`git clean -i` is interactive", "Run it manually in a terminal or use explicit non-interactive clean options.")
		}
	case "difftool", "mergetool":
		return interactiveFinding("git "+sub, fmt.Sprintf("`git %s` launches an interactive tool", sub), "Run it manually in a terminal or use plain git diff/merge commands.")
	}
	return nil
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

func gitCommitHasMessage(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "-m" || arg == "-F" || arg == "--message" || arg == "--file" {
			return true
		}
		if strings.HasPrefix(arg, "-m") && len(arg) > 2 {
			return true
		}
		if strings.HasPrefix(arg, "-F") && len(arg) > 2 {
			return true
		}
		if strings.HasPrefix(arg, "--message=") || strings.HasPrefix(arg, "--file=") {
			return true
		}
	}
	return false
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
	for _, tok := range tokens {
		switch tok {
		case "|", "&&", "||", ";", "&", "(", ")":
			if len(current) > 0 {
				commands = append(commands, current)
				current = nil
			}
		case "<", ">", ">>", "2>", "2>>", "&>", "&>>", "<<<", "<<":
			if len(current) > 0 {
				commands = append(commands, current)
				current = nil
			}
		default:
			if isRedirectionToken(tok) {
				if len(current) > 0 {
					commands = append(commands, current)
					current = nil
				}
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
