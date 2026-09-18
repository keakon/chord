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
	Source      string
	LiteralArgs []string
	Kind        string
	Index       int
}

// AnalyzeShellCommand parses a Shell command and extracts simple subcommands in
// source order. Function declaration bodies are extracted like any other
// subcommand: a body that runs in the same command string is executed content,
// and permission matching must see it so a narrow allow rule can never
// auto-allow an unreviewed subcommand hidden behind a definition.
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

	subcommands := make([]ShellSubcommand, 0, 4)
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				return true
			}
			source := shellSubcommandSource(command, n)
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
				Source:      source,
				LiteralArgs: literalArgs,
				Kind:        "simple",
				Index:       len(subcommands),
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

func shellSubcommandSource(command string, expr *syntax.CallExpr) string {
	if expr == nil || len(expr.Args) == 0 {
		return ""
	}
	start := int(expr.Args[0].Pos().Offset())
	// A leading PATH assignment decides which binary the command name resolves
	// to, so it must stay inside the matched source: otherwise
	// `PATH=/tmp/evil git status` reads as a plain `git status` and a narrow
	// `git *` allow rule would auto-allow an attacker-controlled binary.
	// Assignments that only set an environment value stay stripped, so existing
	// `FOO=bar cmd` matching is unchanged.
	for _, assign := range expr.Assigns {
		if assign == nil || assign.Name == nil || assign.Name.Value != "PATH" {
			continue
		}
		if offset := int(assign.Pos().Offset()); offset >= 0 && offset < start {
			start = offset
		}
	}
	end := int(expr.Args[len(expr.Args)-1].End().Offset())
	if start < 0 || end < start || end > len(command) {
		return ""
	}
	return strings.TrimSpace(command[start:end])
}
