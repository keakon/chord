package requesttrim

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/shellanalysis"
	"github.com/keakon/chord/internal/tools"
)

// compactListingHeadLines bounds how many leading records a listing summary
// keeps.
const compactListingHeadLines = 8

// reduceRecordListingOutputSummary summarizes output that the command line
// identified as one record per line. Marker-based selection is wrong for this
// shape: the records are peers, so "salient" is not a property any single line
// has, and the substrings that would be scored (`updated`, `created`,
// `completed`) are ordinary words in commit subjects and branch names. Position
// carries the meaning instead — these listings print newest or current first —
// so the head is kept and the drop is stated as a count rather than inferred
// from what survived.
func reduceRecordListingOutputSummary(ctx Context) string {
	lines := make([]string, 0, compactListingHeadLines)
	total := 0
	forEachLine(ctx.Content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		total++
		if len(lines) < compactListingHeadLines {
			lines = append(lines, "- "+TextSnippet(trimmed, SummaryLineSnippetChars))
		}
		return true
	})
	if len(lines) == 0 {
		lines = append(lines, "- (no records preserved)")
	}
	if omitted := total - len(lines); omitted > 0 {
		noun := "records"
		if omitted == 1 {
			noun = "record"
		}
		lines = append(lines, fmt.Sprintf("- ... (%d later %s omitted) ...", omitted, noun))
	}
	return fmt.Sprintf("[Older %s listing summarized for this request to save context; bytes=%d records=%d]\n%s",
		ToolNameOrUnknown(ctx.ToolName), len(ctx.Content), total, strings.Join(lines, "\n"))
}

func reduceShellSuccessOutputSummary(ctx Context) string {
	if summary, ok := reduceGoTestSuccessOutputSummary(ctx); ok {
		return summary
	}
	summary := summarizeShellSuccess(ctx.Content, 4)
	if len(summary.Lines) == 0 {
		summary.Lines = []string{"- (no salient output lines preserved)"}
	}
	return fmt.Sprintf("[Older %s success summarized for this request to save context; bytes=%d lines=%d]\n%s", tools.NameShell, len(ctx.Content), summary.MeaningfulLines, strings.Join(summary.Lines, "\n"))
}

type goTestSuccessSummary struct {
	PackagesOK  int
	Cached      int
	NoTestFiles int
	Coverage    int
	Failed      bool
	Lines       []string
}

func reduceGoTestSuccessOutputSummary(ctx Context) (string, bool) {
	if message.IsToolResultUnsuccessfulStatus(ctx.ToolStatus) || !isDirectGoTestCommandMemo(ctx.Memo, ctx.ToolCallID, ctx.Meta.Args) {
		return "", false
	}
	summary := summarizeGoTestSuccess(ctx.Content, 4)
	if summary.PackagesOK == 0 || summary.Failed {
		return "", false
	}
	if len(summary.Lines) == 0 {
		summary.Lines = []string{"- (no package result lines preserved)"}
	}
	return fmt.Sprintf(
		"[Older %s go test success summarized for this request to save context; bytes=%d lines=%d packages_ok=%d cached=%d no_test_files=%d coverage_reports=%d]\n%s",
		tools.NameShell,
		len(ctx.Content),
		countMeaningfulLines(ctx.Content),
		summary.PackagesOK,
		summary.Cached,
		summary.NoTestFiles,
		summary.Coverage,
		strings.Join(summary.Lines, "\n"),
	), true
}

// commandDerivedShellShape is the gate-agnostic wrapper each shape branch
// consults. It is deliberately not hoisted above the branches: the three of
// them apply different age, size and tool-count gates, and the command may only
// decide *which* summary an output gets, never *whether* it is summarized at
// all. Hoisting it would silently widen the reduction surface.
func commandDerivedShellShape(ctx Context) (Class, bool) {
	if ctx.ToolName != tools.NameShell {
		return None, false
	}
	return shellOutputShapeFromCommandMemo(ctx.Memo, ctx.ToolCallID, ctx.Meta.Args)
}

// shellOutputShapeFromCommandMemo returns the reduction class a shell result takes
// when the command that produced it settles the shape, and false when it does
// not. Reading the command beats sniffing the bytes it printed: the reduction
// layer holds the exact command line, while the shape rules below it inspect
// the output, where a payload can imitate a shape it does not have. `git log
// --oneline` is the standing example — a couple of commit subjects containing
// "fail" are all looksLikeBuildLikeLog needs to route a whole commit listing
// through the failure-signal log summary and keep one line of it. A command
// name cannot be imitated that way.
//
// The table stays deliberately small: only commands whose output shape is
// unambiguous, and only when the command is a single invocation, so a pipeline
// that reshapes the output (`... | jq`) keeps the heuristics. Everything
// unmapped falls through unchanged, which makes this a subtraction from the
// sniffing surface rather than a replacement for it.
func shellOutputShapeFromCommandMemo(memo *Memo, toolCallID, argsJSON string) (Class, bool) {
	literal, ok := memo.shellInvocationLiteralArgs(toolCallID, argsJSON, "git")
	if !ok || literal[0] != "git" {
		return None, false
	}
	sub, ok := GitSubcommand(literal)
	if !ok {
		return None, false
	}
	switch sub {
	// Listing and status subcommands print one record per line: durable
	// evidence the model reads as a whole, and never a build log, a search
	// result or a JSON document. `git log -p` and `git show` do emit a diff,
	// but the diff rules run ahead of the shell branch and claim them first.
	case "log", "status", "branch", "tag", "remote", "rev-parse", "stash":
		return requestReductionListing, true
	}
	return None, false
}

// GitSubcommand returns the subcommand of a git invocation, skipping the global
// options that may precede it (`git -C dir log`, `git --no-pager log`). An
// unrecognised option reports false rather than guessing: mistaking an option
// for the subcommand would reintroduce exactly the misreading the command table
// exists to remove.
func GitSubcommand(literal []string) (string, bool) {
	for i := 1; i < len(literal); {
		arg := literal[i]
		if !strings.HasPrefix(arg, "-") {
			return arg, true
		}
		switch {
		case arg == "-C" || arg == "-c" || arg == "--git-dir" || arg == "--work-tree" ||
			arg == "--namespace" || arg == "--exec-path":
			i += 2 // option and its separate value
		case arg == "--no-pager" || arg == "-p" || arg == "--paginate" || arg == "--bare" ||
			arg == "--no-replace-objects" || arg == "--literal-pathspecs" ||
			arg == "--no-optional-locks" || strings.HasPrefix(arg, "--git-dir=") ||
			strings.HasPrefix(arg, "--work-tree=") || strings.HasPrefix(arg, "--namespace=") ||
			strings.HasPrefix(arg, "--exec-path=") ||
			(len(arg) > 2 && (strings.HasPrefix(arg, "-C") || strings.HasPrefix(arg, "-c"))):
			i++
		default:
			return "", false
		}
	}
	return "", false
}

// singleShellInvocationLiteralArgs returns the literal argv of a shell tool call
// that is exactly one invocation, so a pipeline or a substitution that reshapes
// the output never reaches a command-derived rule. program is the name the
// caller is looking for: it is checked as a plain substring first, which is a
// necessary condition and lets a command line that never mentions it skip the
// AST parse — that parse runs for every shell result on every request.
func singleShellInvocationLiteralArgs(argsJSON, program string) ([]string, bool) {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(argsJSON), &args) != nil {
		return nil, false
	}
	if !strings.Contains(args.Command, program) {
		return nil, false
	}
	analysis, err := shellanalysis.Analyze(args.Command)
	if err != nil || len(analysis.Subcommands) != 1 {
		return nil, false
	}
	literal := analysis.Subcommands[0].LiteralArgs
	if len(literal) < 2 {
		return nil, false
	}
	return literal, true
}

func isDirectGoTestCommandMemo(memo *Memo, toolCallID, argsJSON string) bool {
	literal, ok := memo.shellInvocationLiteralArgs(toolCallID, argsJSON, "go")
	if !ok || literal[0] != "go" || literal[1] != "test" {
		return false
	}
	for _, arg := range literal[2:] {
		if arg == "-json" || strings.HasPrefix(arg, "-json=") {
			return false
		}
	}
	return true
}

func summarizeGoTestSuccess(content string, limit int) goTestSuccessSummary {
	var summary goTestSuccessSummary
	if limit <= 0 {
		return summary
	}
	resultLines := make([]string, 0, limit)
	appendResult := func(line string) {
		line = TextSnippet(line, SummaryLineSnippetChars)
		if slices.Contains(resultLines, line) {
			return
		}
		if len(resultLines) < limit {
			resultLines = append(resultLines, line)
			return
		}
		copy(resultLines, resultLines[1:])
		resultLines[len(resultLines)-1] = line
	}
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if trimmed == "FAIL" || strings.HasPrefix(trimmed, "FAIL\t") || strings.HasPrefix(trimmed, "--- FAIL:") || strings.HasPrefix(trimmed, "panic:") {
			summary.Failed = true
			return true
		}
		if len(fields) >= 2 && fields[0] == "ok" {
			summary.PackagesOK++
			if strings.Contains(trimmed, "(cached)") {
				summary.Cached++
			}
			if strings.Contains(strings.ToLower(trimmed), "coverage:") {
				summary.Coverage++
			}
			appendResult(trimmed)
			return true
		}
		if len(fields) >= 3 && fields[0] == "?" && strings.Contains(trimmed, "[no test files]") {
			summary.NoTestFiles++
			return true
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "coverage:") {
			summary.Coverage++
			appendResult(trimmed)
		}
		return true
	})
	for _, line := range resultLines {
		summary.Lines = append(summary.Lines, "- "+strings.ReplaceAll(line, "\n", " "))
	}
	return summary
}

type shellSuccessSummary struct {
	Lines           []string
	MeaningfulLines int
}

func summarizeShellSuccess(content string, limit int) shellSuccessSummary {
	if limit <= 0 {
		return shellSuccessSummary{}
	}
	weakLimit := max(1, limit-1)
	tailLimit := min(3, limit)
	strong := make([]string, 0, limit)
	weak := make([]string, 0, weakLimit)
	tail := make([]string, 0, tailLimit)
	summary := shellSuccessSummary{}

	appendSignal := func(lines []string, line string, lineLimit int) []string {
		if slices.Contains(lines, line) {
			return lines
		}
		if len(lines) < lineLimit {
			return append(lines, line)
		}
		copy(lines, lines[1:])
		lines[lineLimit-1] = line
		return lines
	}
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		summary.MeaningfulLines++
		lineKey := TextSnippet(trimmed, SummaryLineSnippetChars)
		if isStrongShellSuccessSummaryLine(trimmed) {
			strong = appendSignal(strong, lineKey, limit)
		}
		if isWeakShellSuccessSummaryLine(trimmed) {
			weak = appendSignal(weak, lineKey, weakLimit)
		}
		if len(tail) < tailLimit {
			tail = append(tail, lineKey)
		} else {
			copy(tail, tail[1:])
			tail[tailLimit-1] = lineKey
		}
		return true
	})

	renderLines := func(lines []string) []string {
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			out = append(out, "- "+strings.ReplaceAll(line, "\n", " "))
		}
		return out
	}
	appendTailFallback := func(lines []string) []string {
		if len(lines) >= limit || len(tail) == 0 {
			return lines
		}
		lastTail := "- " + strings.ReplaceAll(tail[len(tail)-1], "\n", " ")
		if slices.Contains(lines, lastTail) {
			return lines
		}
		return append(lines, lastTail)
	}

	if len(strong) > 0 {
		summary.Lines = appendTailFallback(renderLines(strong))
		return summary
	}
	if len(weak) > 0 {
		summary.Lines = appendTailFallback(renderLines(weak))
		return summary
	}
	if omitted := summary.MeaningfulLines - len(tail); omitted > 0 {
		noun := "lines"
		if omitted == 1 {
			noun = "line"
		}
		summary.Lines = append(summary.Lines, fmt.Sprintf("- ... (%d %s omitted) ...", omitted, noun))
	}
	summary.Lines = append(summary.Lines, renderLines(tail)...)
	return summary
}

func isStrongShellSuccessSummaryLine(trimmed string) bool {
	lower := strings.ToLower(strings.TrimSpace(trimmed))
	if strings.Contains(lower, "coverage") || strings.Contains(lower, "benchmark") {
		return true
	}
	return strings.HasPrefix(lower, "pass ") ||
		strings.HasPrefix(lower, "pass:") ||
		strings.HasPrefix(lower, "ok ") ||
		strings.HasPrefix(lower, "done") ||
		strings.HasPrefix(lower, "success ") ||
		strings.HasPrefix(lower, "success:") ||
		strings.HasPrefix(lower, "successfully ")
}

func isWeakShellSuccessSummaryLine(trimmed string) bool {
	lower := strings.ToLower(strings.TrimSpace(trimmed))
	return containsAnyMarker(lower, shellSuccessWeakLineMarkers)
}
