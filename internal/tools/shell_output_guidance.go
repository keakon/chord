package tools

import (
	"path/filepath"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"github.com/keakon/chord/internal/shell"
)

// Only annotate nontrivial executions. This is guidance about a command's
// shape, never an interpretation of its exit status or a reason to retry it.
const shellOutputFilterNoteMin = 5 * time.Second

// appendShellOutputFilterNote appends a note to the output of a nontrivial
// execution whose pipeline filters its output through tail/head/grep.
func appendShellOutputFilterNote(output, command, shellType string, elapsed time.Duration) string {
	if elapsed < shellOutputFilterNoteMin || !shellContainsOutputFilter(command, shellType) {
		return output
	}
	return output + "\n(output note: filters may hide failures and the producer's exit code. Read any existing unfiltered log before rerunning. If another execution is necessary, run it without the filter: long output is saved with a bounded preview, so read or search the saved output for other views.)"
}

// PowerShell pipelines are not POSIX text filters.
func shellSupportsTextFilters(shellType string) bool {
	return shell.ParseShellType(shellType) != shell.ShellPowerShell
}

// shellContainsOutputFilter reports whether command pipes into tail, head or
// grep outside a function definition.
func shellContainsOutputFilter(command, shellType string) bool {
	if !shellSupportsTextFilters(shellType) {
		return false
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return false
	}
	found := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if found {
			return false
		}
		if _, declaration := node.(*syntax.FuncDecl); declaration {
			return false
		}
		pipe, ok := node.(*syntax.BinaryCmd)
		if !ok || (pipe.Op != syntax.Pipe && pipe.Op != syntax.PipeAll) {
			return true
		}
		call, ok := pipe.Y.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		switch filepath.Base(call.Args[0].Lit()) {
		case "tail", "head", "grep":
			found = true
		}
		return !found
	})
	return found
}
