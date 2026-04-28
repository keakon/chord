package tools

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// BashAnalysis describes the static shape of a Bash command string.
type BashAnalysis struct {
	RawCommand  string
	Subcommands []BashSubcommand
	ParseMode   string
}

// BashSubcommand is one atomic simple command extracted from a Bash command.
type BashSubcommand struct {
	Source string
	Kind   string
	Index  int
}

// AnalyzeBashCommand parses a Bash command and extracts simple subcommands in
// source order. Function declaration bodies are skipped so permission matching
// does not treat dormant helper definitions as immediately executing commands.
func AnalyzeBashCommand(command string) (BashAnalysis, error) {
	analysis := BashAnalysis{
		RawCommand: command,
		ParseMode:  "fallback",
	}
	if strings.TrimSpace(command) == "" {
		return analysis, fmt.Errorf("bash command is empty")
	}

	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return analysis, fmt.Errorf("parse bash command: %w", err)
	}

	subcommands := make([]BashSubcommand, 0, 4)
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.FuncDecl:
			return false
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				return true
			}
			source := bashSubcommandSource(command, n)
			if source == "" {
				return true
			}
			subcommands = append(subcommands, BashSubcommand{
				Source: source,
				Kind:   "simple",
				Index:  len(subcommands),
			})
		}
		return true
	})

	if len(subcommands) == 0 {
		return analysis, fmt.Errorf("no simple bash subcommands found")
	}
	analysis.Subcommands = subcommands
	analysis.ParseMode = "parsed"
	return analysis, nil
}

func bashSubcommandSource(command string, expr *syntax.CallExpr) string {
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
