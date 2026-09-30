package agent

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/worktree"
)

// Runtime-owned checkpoint sections. Status-like facts — which files were
// edited, which commands ran, which commits landed, where the repository
// stands — are extracted from the durable transcript and from a build-time git
// probe instead of being written by the model: prose the model froze into the
// checkpoint at submission time goes stale as the work continues, and a
// summarizer under pressure can drop it entirely. The model-owned sections
// retain semantic progress, decisions, open questions and next steps.
const (
	checkpointWorklogHeading         = "## Checkpoint Worklog"
	checkpointRepositoryStateHeading = "## Repository State"

	// compactWorklogMax* bound each worklog block. The blocks keep their tails:
	// the newest entries are what the continuation needs, and a long session
	// must not let its worklog crowd the checkpoint's continuation budget.
	compactWorklogMaxFiles    = 20
	compactWorklogMaxCommands = 12
	compactWorklogMaxCommits  = 8
	// compactWorklogItemChars bounds one rendered row (path, command, commit
	// line) so a single long command line cannot inflate the section.
	compactWorklogItemChars = 200
	// Machine facts have a separate token budget from model-authored state.
	compactWorklogMaxTokens           = 1536
	compactCheckpointMachineMaxTokens = 2048

	// checkpointGitProbeTimeout bounds the whole repository-state probe. The
	// checkpoint build runs on the compaction barrier, so a git process that
	// hangs (network filesystem, credential prompt) must not hold up the
	// reset: a timed-out probe degrades to no repository section, which one
	// git status recovers live.
	checkpointGitProbeTimeout = 5 * time.Second
)

// checkpointRunsGitCommit selects results worth scanning for git's success
// line. The shared shell and git parsers handle compound commands and global
// options with values; dynamic invocations are not guessed.
func checkpointRunsGitCommit(command string) bool {
	if !strings.Contains(command, "git") {
		return false
	}
	analysis, err := tools.AnalyzeShellCommand(command)
	if err != nil {
		return false
	}
	for _, invocation := range analysis.Subcommands {
		args := invocation.LiteralArgs
		if len(args) < 2 || args[0] != "git" {
			continue
		}
		if subcommand, ok := gitSubcommand(args); ok && subcommand == "commit" {
			return true
		}
	}
	return false
}

// checkpointGitCommitOutputLine matches git's commit success line, e.g.
// "[main 75e8edc8] subject" or "[main (root-commit) 75e8edc8] subject".
var checkpointGitCommitOutputLine = regexp.MustCompile(`(?m)^\[[^\]\n]+\s+[0-9a-f]{7,40}\][^\n]*`)

// checkpointWorklog is the machine-observable work performed since the
// previous checkpoint, in transcript order.
type checkpointWorklog struct {
	files    []checkpointWorklogFile
	commands []string
	commits  []string
}

// checkpointWorklogFile is one file operation: Verb is the tool name for
// write/edit/delete and the patch mutation kind for apply_patch.
type checkpointWorklogFile struct {
	Verb string
	Path string
}

// buildCheckpointWorklog extracts the worklog from the archived head. It is a
// pure function of the transcript — tool calls and their results, never model
// prose — so the same transcript always renders the same worklog. The segment
// starts after the newest durable checkpoint: everything before it was already
// accounted for when that checkpoint was built.
func buildCheckpointWorklog(archivedHead []message.Message) checkpointWorklog {
	var worklog checkpointWorklog
	segmentStart := 0
	for i, msg := range slices.Backward(archivedHead) {
		if msg.IsCompactionSummary {
			segmentStart = i + 1
			break
		}
	}
	calls := make(map[string]message.ToolCall)
	for _, msg := range archivedHead[segmentStart:] {
		if msg.Role == message.RoleAssistant {
			for _, call := range msg.ToolCalls {
				calls[call.ID] = call
			}
			continue
		}
		if msg.Role != message.RoleTool || msg.ToolRecoveryState == message.ToolRecoveryStateNotStarted {
			continue
		}
		call, ok := calls[msg.ToolCallID]
		if !ok {
			continue
		}
		name := tools.NormalizeName(call.Name)
		// FileState records committed side effects even when the batch failed.
		// Arguments describe intent and cannot prove that a file was changed.
		worklog.files = append(worklog.files, checkpointWorklogMutations(name, msg.FileState)...)
		if name != tools.NameShell {
			continue
		}
		args := call.Args
		if msg.Audit != nil && msg.Audit.EffectiveArgsJSON != "" {
			args = []byte(msg.Audit.EffectiveArgsJSON)
		}
		parsed, err := decodeShellCallArguments(args)
		if err != nil || parsed.Command == "" {
			continue
		}
		status := msg.ToolStatus
		if msg.ToolRecoveryState == message.ToolRecoveryStateOutcomeUnknown || status == "" {
			status = message.ToolRecoveryStateOutcomeUnknown
		}
		worklog.commands = append(worklog.commands, "["+status+"] "+parsed.Command)
		// A compound shell may commit successfully before a later command
		// fails. Its observed commit line survives that overall failure, but
		// a synthetic recovery result never proves a commit.
		if msg.ToolRecoveryState == "" && checkpointRunsGitCommit(parsed.Command) {
			for _, line := range checkpointGitCommitOutputLine.FindAllString(msg.Content, -1) {
				worklog.commits = append(worklog.commits, strings.TrimSpace(line))
			}
		}
	}
	worklog.files = latestCheckpointWorklogEntries(worklog.files)
	worklog.commands = latestCheckpointWorklogEntries(worklog.commands)
	worklog.commits = latestCheckpointWorklogEntries(worklog.commits)
	return worklog
}

// checkpointWorklogMutations renders only paths whose side effects were
// persisted at tool completion, including partial failures and moves.
func checkpointWorklogMutations(name string, state *message.ToolFileState) []checkpointWorklogFile {
	if state == nil {
		return nil
	}
	var files []checkpointWorklogFile
	seen := make(map[string]bool)
	for _, change := range state.Changes {
		if change.TargetPath != "" {
			files = append(files, checkpointWorklogFile{Verb: string(tools.MutationUpdate), Path: change.Path + " -> " + change.TargetPath})
			seen[change.Path], seen[change.TargetPath] = true, true
		}
	}
	verb := tools.NameWrite
	if name == tools.NameEdit {
		verb = tools.NameEdit
	}
	for _, file := range state.Writes {
		if file.Path != "" && !seen[file.Path] {
			files = append(files, checkpointWorklogFile{Verb: verb, Path: file.Path})
			seen[file.Path] = true
		}
	}
	for _, file := range state.Deletes {
		if file.Path != "" && !seen[file.Path] {
			files = append(files, checkpointWorklogFile{Verb: tools.NameDelete, Path: file.Path})
			seen[file.Path] = true
		}
	}
	return files
}

// Deduplicate at the last occurrence so repeated work cannot crowd newer
// distinct facts out of the bounded checkpoint.
func latestCheckpointWorklogEntries[T comparable](items []T) []T {
	seen := make(map[T]bool)
	var latest []T
	for _, item := range slices.Backward(items) {
		if !seen[item] {
			seen[item] = true
			latest = append(latest, item)
		}
	}
	slices.Reverse(latest)
	return latest
}

// renderCheckpointWorklog renders the worklog section, or "" when the segment
// recorded no machine-observable work.
func renderCheckpointWorklog(worklog checkpointWorklog) string {
	commits, commitsOmitted := checkpointWorklogTail(worklog.commits, compactWorklogMaxCommits)
	files, filesOmitted := checkpointWorklogTail(worklog.files, compactWorklogMaxFiles)
	commands, commandsOmitted := checkpointWorklogTail(worklog.commands, compactWorklogMaxCommands)
	if len(commits) == 0 && len(files) == 0 && len(commands) == 0 {
		return ""
	}
	var blocks []string
	if len(commits) > 0 {
		blocks = append(blocks, renderCheckpointWorklogBlock("Commits", commits, commitsOmitted))
	}
	if len(files) > 0 {
		rows := make([]string, 0, len(files))
		for _, file := range files {
			rows = append(rows, checkpointWorklogItem(file.Verb+" "+file.Path))
		}
		blocks = append(blocks, renderCheckpointWorklogBlock("Files touched", rows, filesOmitted))
	}
	if len(commands) > 0 {
		rows := make([]string, 0, len(commands))
		for _, command := range commands {
			rows = append(rows, checkpointWorklogItem(command))
		}
		blocks = append(blocks, renderCheckpointWorklogBlock("Shell tool outcomes", rows, commandsOmitted))
	}
	var sb strings.Builder
	sb.WriteString(checkpointWorklogHeading)
	sb.WriteString("\nObserved file mutations and shell tool outcomes from the archived segment since the previous checkpoint. This is a historical snapshot; newer runtime state and tool results take precedence. Shell tool success does not prove that a background process has finished.\n\n")
	sb.WriteString(strings.Join(blocks, "\n\n"))
	return sb.String()
}

// checkpointWorklogTail keeps the newest maxItems entries (in their original
// order) and reports how many older entries were dropped.
func checkpointWorklogTail[T any](items []T, maxItems int) ([]T, int) {
	if maxItems <= 0 || len(items) <= maxItems {
		return items, 0
	}
	return items[len(items)-maxItems:], len(items) - maxItems
}

func renderCheckpointWorklogBlock(title string, rows []string, omitted int) string {
	var sb strings.Builder
	sb.WriteString("### ")
	sb.WriteString(title)
	sb.WriteString("\n")
	if omitted > 0 {
		fmt.Fprintf(&sb, "- (%d earlier omitted)\n", omitted)
	}
	for _, row := range rows {
		sb.WriteString("- ")
		sb.WriteString(checkpointWorklogItem(row))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// checkpointWorklogItem flattens one transcript-authored value onto one line
// and caps its length. A newline inside a path or command could otherwise
// forge a section heading the checkpoint readers key on.
func checkpointWorklogItem(text string) string {
	return llm.TruncateStringRunes(oneLineCheckpointText(text), compactWorklogItemChars, "…")
}

// applyCheckpointMachineState adds the runtime-owned machine sections to a
// freshly built checkpoint summary: the worklog of the archived head and the
// build-time repository state. Both replace any same-heading section the
// summary already carries, so model prose can never shadow the machine facts.
func applyCheckpointMachineState(summary string, archivedHead []message.Message, repositoryState string, estimateTokens func(string) int) string {
	if estimateTokens(repositoryState) > compactCheckpointMachineMaxTokens {
		repositoryState = ""
	}
	worklog := buildCheckpointWorklog(archivedHead)
	budget := min(compactWorklogMaxTokens, compactCheckpointMachineMaxTokens-estimateTokens(repositoryState))
	section := boundedCheckpointWorklog(worklog, budget, estimateTokens)
	summary = ensureCheckpointWorklogSection(summary, section)
	return ensureCheckpointRepositoryStateSection(summary, repositoryState)
}

// boundedCheckpointWorklog drops complete older rows rather than truncating
// recovery-critical sections or leaving a cut-off command that looks complete.
func boundedCheckpointWorklog(worklog checkpointWorklog, budget int, estimateTokens func(string) int) string {
	omitted := len(worklog.files) > compactWorklogMaxFiles || len(worklog.commands) > compactWorklogMaxCommands || len(worklog.commits) > compactWorklogMaxCommits
	worklog.files, _ = checkpointWorklogTail(worklog.files, compactWorklogMaxFiles)
	worklog.commands, _ = checkpointWorklogTail(worklog.commands, compactWorklogMaxCommands)
	worklog.commits, _ = checkpointWorklogTail(worklog.commits, compactWorklogMaxCommits)
	for {
		section := renderCheckpointWorklog(worklog)
		if omitted && section != "" {
			section += "\n\n- Earlier worklog entries omitted; full details are in archived history."
		}
		if estimateTokens(section) <= budget {
			return section
		}
		omitted = true
		switch {
		case len(worklog.commands) > 0:
			worklog.commands = worklog.commands[1:]
		case len(worklog.files) > 0:
			worklog.files = worklog.files[1:]
		case len(worklog.commits) > 0:
			worklog.commits = worklog.commits[1:]
		default:
			return ""
		}
	}
}

func ensureCheckpointWorklogSection(summary, section string) string {
	return ensureCheckpointMachineSection(summary, checkpointWorklogHeading, section, checkpointProgressHeading)
}

func ensureCheckpointRepositoryStateSection(summary, section string) string {
	// Repository state follows the worklog when there is one; without it the
	// section still belongs next to the other status-like sections, not at the
	// end of the checkpoint.
	anchor := checkpointWorklogHeading
	if findMarkdownHeadingLine(summary, anchor) < 0 {
		anchor = checkpointProgressHeading
	}
	return ensureCheckpointMachineSection(summary, checkpointRepositoryStateHeading, section, anchor)
}

// ensureCheckpointMachineSection places a runtime-owned section: it replaces
// an existing section with the same heading (the machine facts win over
// whatever the summary carried), inserts a new one directly after
// anchorHeading's section, or appends it when the anchor is missing. An empty
// section removes any existing same-heading section and adds nothing.
func ensureCheckpointMachineSection(summary, heading, section, anchorHeading string) string {
	section = strings.TrimSpace(section)
	start, end, found := markdownSectionBounds(summary, heading)
	if section == "" {
		if !found {
			return summary
		}
		return spliceCheckpointSection(summary, start-len(heading), end, "")
	}
	if found {
		return spliceCheckpointSection(summary, start-len(heading), end, section)
	}
	_, anchorEnd, ok := markdownSectionBounds(summary, anchorHeading)
	if !ok {
		anchorEnd = len(summary)
	}
	return spliceCheckpointSection(summary, anchorEnd, anchorEnd, section)
}

// spliceCheckpointSection replaces summary[start:end] with section, keeping
// exactly one blank line between neighboring sections.
func spliceCheckpointSection(summary string, start, end int, section string) string {
	section = strings.TrimSpace(section)
	prefix := strings.TrimRight(summary[:start], "\n")
	rest := strings.TrimLeft(summary[end:], "\n")
	switch {
	case section == "":
		switch {
		case prefix == "":
			return rest
		case rest == "":
			return prefix
		default:
			return prefix + "\n\n" + rest
		}
	case prefix == "" && rest == "":
		return section
	case prefix == "":
		return section + "\n\n" + rest
	case rest == "":
		return prefix + "\n\n" + section
	default:
		return prefix + "\n\n" + section + "\n\n" + rest
	}
}

// buildCheckpointRepositoryState probes the checkout at checkpoint-build time.
// It returns "" when there is nothing to report: no workdir, no git binary, a
// probe failure, or a timeout. The probe runs with its own deadline because it
// blocks the compaction barrier.
func buildCheckpointRepositoryState(ctx context.Context, workDir string) string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" || !worktree.GitAvailable() {
		return ""
	}
	probeCtx, cancel := context.WithTimeout(ctx, checkpointGitProbeTimeout)
	defer cancel()
	branch, err := worktree.CurrentBranch(probeCtx, workDir)
	if err != nil {
		return ""
	}
	head, err := worktree.HeadCommit(probeCtx, workDir)
	if err != nil {
		return ""
	}
	dirty, ok := worktree.IsDirty(probeCtx, workDir)
	if !ok {
		return ""
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		// Detached HEAD has no branch name; naming the state beats an empty
		// field.
		branch = "(detached HEAD)"
	}
	head = strings.TrimSpace(head)
	if len(head) > 12 {
		head = head[:12]
	}
	tree := "clean"
	if dirty {
		tree = "uncommitted changes"
	}
	var sb strings.Builder
	sb.WriteString(checkpointRepositoryStateHeading)
	sb.WriteString("\nCaptured when this checkpoint was built. This is a historical snapshot; newer repository observations and tool results take precedence.\n\n")
	fmt.Fprintf(&sb, "- branch: %s\n", checkpointWorklogItem(branch))
	fmt.Fprintf(&sb, "- HEAD: %s\n", checkpointWorklogItem(head))
	fmt.Fprintf(&sb, "- working tree: %s\n", tree)
	return strings.TrimRight(sb.String(), "\n")
}
