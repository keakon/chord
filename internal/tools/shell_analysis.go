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
	Source string
	Kind   string
	Index  int
}

// AnalyzeShellCommand parses a Shell command and extracts simple subcommands in
// source order. Function declaration bodies are skipped so permission matching
// does not treat dormant helper definitions as immediately executing commands.
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
		case *syntax.FuncDecl:
			return false
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				return true
			}
			source := shellSubcommandSource(command, n)
			if source == "" {
				return true
			}
			subcommands = append(subcommands, ShellSubcommand{
				Source: source,
				Kind:   "simple",
				Index:  len(subcommands),
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
	end := int(expr.Args[len(expr.Args)-1].End().Offset())
	if start < 0 || end < start || end > len(command) {
		return ""
	}
	return strings.TrimSpace(command[start:end])
}
