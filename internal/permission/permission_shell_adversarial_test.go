package permission

import "testing"

// Adversarial shell-permission tests: a narrow shell allow rule must never
// auto-allow a command that contains an unreviewed subcommand. Compound
// chaining (; && || | & newline), command substitution ($() nesting,
// backticks, including inside double quotes), and quote-parse failures must
// fall through to the broader rule instead of the narrow allow.
func TestEvaluate_ShellNarrowAllowAdversarial(t *testing.T) {
	narrow := Ruleset{
		{Permission: "shell", Pattern: "*", Action: ActionAsk},
		{Permission: "shell", Pattern: "git *", Action: ActionAllow},
		{Permission: "shell", Pattern: "echo *", Action: ActionAllow},
	}

	// Commands that must NOT be auto-allowed by the narrow git/echo rules:
	// the fallback "*" ask rule has to win.
	mustAsk := map[string]string{
		"semicolon":              "git status; rm -rf /tmp/x",
		"and":                    "git status && rm -rf /tmp/x",
		"or":                     "git status || rm -rf /tmp/x",
		"pipe":                   "git status | cat",
		"background":             "git status & rm -rf /tmp/x",
		"newline":                "git status\nrm -rf /tmp/x",
		"substitution":           "git status $(rm -rf /tmp/x)",
		"nested substitution":    "git status $(echo $(rm -rf /tmp/x))",
		"substitution dquoted":   `git status "$(rm -rf /tmp/x)"`,
		"echo substitution":      "echo $(rm -rf /tmp/x)",
		"backtick":               "git status `rm -rf /tmp/x`",
		"backtick dquoted":       "git status \"`rm -rf /tmp/x`\"",
		"unclosed single":        "git status 'unclosed",
		"unclosed double":        `git status "unclosed`,
		"unclosed subst":         "git status $(unclosed",
		"separator after unopen": "git status 'a;b",
		"trailing backslash":     `git status foo\`,
		"process substitution":   "diff <(rm -rf /tmp/x) <(git status)",
		"proc subst out":         "git status >(rm -rf /tmp/x)",
	}
	for name, command := range mustAsk {
		t.Run(name, func(t *testing.T) {
			if got := narrow.Evaluate("shell", command); got != ActionAsk {
				t.Fatalf("Evaluate(shell, %q) = %s, want ask", command, got)
			}
			match := narrow.LastEvaluatedMatch("shell", command)
			if !match.Found {
				t.Fatalf("LastEvaluatedMatch(shell, %q) found no rule, want fallback ask", command)
			}
			if match.Rule.Action != ActionAsk {
				t.Fatalf("LastEvaluatedMatch(shell, %q) = %s via %q, want ask", command, match.Rule.Action, match.Rule.Pattern)
			}
		})
	}

	// Quoted or escaped metacharacters are literal payload, not new commands:
	// the narrow allow still applies.
	mustAllow := map[string]string{
		"single quoted semi":  "git status 'a;b'",
		"double quoted semi":  `git status "a;b"`,
		"single quoted subst": "git status '$(echo hi)'",
		"escaped subst":       `git status \$(echo hi)`,
		"escaped semi":        `git status a\;b`,
		"quoted proc subst":   "git status '<(echo hi)'",
		"dquoted proc subst":  `git status "<(echo hi)"`,
		"plain redirect out":  "git status > /tmp/x",
		"plain redirect in":   "git status < /tmp/x",
	}
	for name, command := range mustAllow {
		t.Run(name, func(t *testing.T) {
			if got := narrow.Evaluate("shell", command); got != ActionAllow {
				t.Fatalf("Evaluate(shell, %q) = %s, want allow", command, got)
			}
		})
	}
}

func TestEvaluate_ShellWideAllowStillAllowsCompound(t *testing.T) {
	wide := Ruleset{
		{Permission: "shell", Pattern: "*", Action: ActionAllow},
	}
	for _, command := range []string{
		"git status; rm -rf /tmp/x",
		"git status && rm -rf /tmp/x",
		"git status $(rm -rf /tmp/x)",
		"git status `rm -rf /tmp/x`",
	} {
		if got := wide.Evaluate("shell", command); got != ActionAllow {
			t.Fatalf("Evaluate(shell, %q) = %s, want allow (wide allow is explicit)", command, got)
		}
	}
}

func TestEvaluate_ShellCompoundGuardIsShellOnly(t *testing.T) {
	rs := Ruleset{
		{Permission: "read", Pattern: "*", Action: ActionAsk},
		{Permission: "read", Pattern: "a;b", Action: ActionAllow},
	}
	if got := rs.Evaluate("read", "a;b"); got != ActionAllow {
		t.Fatalf("Evaluate(read, %q) = %s, want allow (guard must not apply to non-shell tools)", "a;b", got)
	}
}
