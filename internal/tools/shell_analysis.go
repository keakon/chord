package tools

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ShellAnalysis describes the static shape of a Shell command string.
type ShellAnalysis struct {
	RawCommand  string
	Subcommands []ShellSubcommand
	ParseMode   string
}

// ShellSubcommand is one atomic simple command extracted from a Shell command.
type ShellSubcommand struct {
	// Source is the text the permission layer reviews for this subcommand: the
	// command word and arguments plus every assignment it carries — its own
	// prefix assignments, and assignment statements earlier in the command
	// string. No assignment is filtered by variable name, because the name is
	// what decides whether a command resolves to a different binary (PATH) or
	// runs injected code (LD_PRELOAD), and a rule written for the bare command
	// text never reviewed the assignments, so a narrow allow rule must match
	// Source.
	Source string
	// CommandSource drops those assignments, leaving the command word and
	// arguments as a rule names the command itself. A deny rule anchors here so
	// an assignment cannot push a denied command into a broader fallback rule.
	CommandSource string
	LiteralArgs   []string
	Kind          string
	Index         int
}

// AnalyzeShellCommand parses a Shell command and extracts simple subcommands in
// source order. Function declaration bodies are extracted like any other
// subcommand: a body that runs in the same command string is executed content,
// and permission matching must see it so a narrow allow rule can never
// auto-allow an unreviewed subcommand hidden behind a definition.
//
// Assignments stay in the reviewed source whatever the variable they set: a
// shell assignment changes which binary a command name resolves to (PATH),
// injects code into it (LD_PRELOAD), or steers its own configuration
// (GIT_EXEC_PATH), and no rule can tell a harmless name from a decisive one, so
// a narrow allow rule that only covers the bare command text must not decide
// the outcome on its own.
func AnalyzeShellCommand(command string) (ShellAnalysis, error) {
	analysis := ShellAnalysis{
		RawCommand: command,
		ParseMode:  "fallback",
	}
	if strings.TrimSpace(command) == "" {
		return analysis, fmt.Errorf("shell command is empty")
	}

	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return analysis, fmt.Errorf("parse shell command: %w", err)
	}

	envStart := shellEnvironmentStart(file)

	subcommands := make([]ShellSubcommand, 0, 4)
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				return true
			}
			source := shellSubcommandSource(command, n, envStart)
			if source == "" {
				return true
			}
			literalArgs := make([]string, 0, len(n.Args))
			for _, word := range n.Args {
				literal := word.Lit()
				if literal == "" {
					break
				}
				literalArgs = append(literalArgs, literal)
			}
			subcommands = append(subcommands, ShellSubcommand{
				Source:        source,
				CommandSource: shellCommandText(command, n),
				LiteralArgs:   literalArgs,
				Kind:          "simple",
				Index:         len(subcommands),
			})
		}
		return true
	})

	if len(subcommands) == 0 {
		return analysis, fmt.Errorf("no simple shell subcommands found")
	}
	analysis.Subcommands = subcommands
	analysis.ParseMode = "parsed"
	return analysis, nil
}

// shellEnvironmentStart returns the offset of the earliest assignment the shell
// applies to the commands after it, or -1 when none does: a bare assignment
// statement, and the assignments of a declaration clause (export, declare,
// local, ...). A prefix assignment (FOO=bar cmd) applies to its own command
// only, so it is not one of these — except when that command is a declaration
// builtin, because the shell keeps it (PATH=/tmp/evil export PATH).
func shellEnvironmentStart(file *syntax.File) int {
	start := -1
	syntax.Walk(file, func(node syntax.Node) bool {
		offset, ok := shellEnvironmentAssignment(node)
		if ok && (start < 0 || offset < start) {
			start = offset
		}
		return true
	})
	return start
}

func shellEnvironmentAssignment(node syntax.Node) (int, bool) {
	switch n := node.(type) {
	case *syntax.CallExpr:
		if len(n.Assigns) == 0 {
			return 0, false
		}
		if len(n.Args) == 0 || shellDeclarationBuiltin(n.Args[0].Lit()) {
			return int(n.Pos().Offset()), true
		}
	case *syntax.DeclClause:
		for _, arg := range n.Args {
			if arg != nil && arg.Name != nil && (arg.Value != nil || arg.Array != nil) {
				return int(n.Pos().Offset()), true
			}
		}
	}
	return 0, false
}

func shellDeclarationBuiltin(name string) bool {
	switch name {
	case "export", "declare", "local", "readonly", "typeset", "nameref":
		return true
	}
	return false
}

// shellSubcommandSource returns the text from the earliest assignment that
// precedes this command through its last argument: the command's own prefix
// assignments, and any assignment statement the shell applied before it.
// Neither is filtered by variable name, because the assignment decides which
// binary the command name resolves to and how it runs, so it must stay inside
// the matched source: otherwise `PATH=/tmp/evil; git status` reads as a plain
// `git status` and a narrow `git *` allow rule would auto-allow whatever binary
// the assignment selected.
func shellSubcommandSource(command string, expr *syntax.CallExpr, envStart int) string {
	if expr == nil || len(expr.Args) == 0 {
		return ""
	}
	start := int(expr.Args[0].Pos().Offset())
	if envStart >= 0 && envStart < start {
		start = envStart
	}
	for _, assign := range expr.Assigns {
		if assign == nil {
			continue
		}
		if offset := int(assign.Pos().Offset()); offset >= 0 && offset < start {
			start = offset
		}
	}
	return shellCommandSlice(command, expr, start)
}

// shellCommandText returns the command word through its last argument, the way
// a rule names the command itself.
func shellCommandText(command string, expr *syntax.CallExpr) string {
	if expr == nil || len(expr.Args) == 0 {
		return ""
	}
	return shellCommandSlice(command, expr, int(expr.Args[0].Pos().Offset()))
}

func shellCommandSlice(command string, expr *syntax.CallExpr, start int) string {
	end := int(expr.Args[len(expr.Args)-1].End().Offset())
	if start < 0 || end < start || end > len(command) {
		return ""
	}
	return strings.TrimSpace(command[start:end])
}
