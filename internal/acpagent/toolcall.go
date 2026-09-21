package acpagent

import (
	"encoding/json"
	"fmt"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/toolname"
)

// toolArgs is the subset of tool arguments worth showing in a client-side tool
// card. Chord tools share a few naming conventions, so one struct covers the
// common cases and unknown arguments simply produce no detail.
type toolArgs struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
	Command string `json:"command"`
	Query   string `json:"query"`
	URL     string `json:"url"`
	Name    string `json:"name"`
	Task    string `json:"task"`
}

// maxToolTitleDetail bounds the argument fragment embedded in a tool title.
const maxToolTitleDetail = 120

// toolKind maps a Chord tool name onto the category clients use to pick an
// icon and a permission prompt.
func toolKind(name string) acp.ToolKind {
	switch toolname.Normalize(name) {
	case toolname.Read, toolname.ReadArtifact, toolname.ViewImage:
		return acp.ToolKindRead
	case toolname.Write, toolname.Edit, toolname.ApplyPatch:
		return acp.ToolKindEdit
	case toolname.Delete:
		return acp.ToolKindDelete
	case toolname.Grep, toolname.Glob:
		return acp.ToolKindSearch
	case toolname.Shell, toolname.JobOutput, toolname.JobList, toolname.JobKill:
		return acp.ToolKindExecute
	case toolname.WebFetch:
		return acp.ToolKindFetch
	case toolname.TodoWrite:
		return acp.ToolKindThink
	default:
		return acp.ToolKindOther
	}
}

// toolLabel turns a tool name into a human-readable prefix: "job_output" becomes
// "Job Output".
func toolLabel(name string) string {
	name = toolname.Normalize(strings.TrimSpace(name))
	if name == "" {
		return "Tool"
	}
	words := strings.Fields(strings.ReplaceAll(name, "_", " "))
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

// toolTitle is the one-line description clients show for a tool call. It keeps
// the tool label and appends the most identifying argument it can find.
func toolTitle(name, argsJSON string) string {
	label := toolLabel(name)
	detail := toolTitleDetail(argsJSON)
	if detail == "" {
		return label
	}
	return label + " " + detail
}

func toolTitleDetail(argsJSON string) string {
	var args toolArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(argsJSON)), &args); err != nil {
		return ""
	}
	detail := firstNonEmpty(args.Path, args.Pattern, args.Command, args.Query, args.URL, args.Name, args.Task)
	detail = strings.TrimSpace(detail)
	if len(detail) > maxToolTitleDetail {
		detail = detail[:maxToolTitleDetail] + "…"
	}
	return detail
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// rawInput keeps the model-authored JSON as-is when it is valid, and reports
// nothing otherwise: clients render rawInput as structured data, so a partial
// streaming fragment must never be passed off as one.
func rawInput(argsJSON string) any {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return nil
	}
	return json.RawMessage(trimmed)
}

// toolLocations reports the file a tool call targets, for tools whose argument
// names the file they touch.
func toolLocations(name, argsJSON string) []acp.ToolCallLocation {
	switch toolname.Normalize(name) {
	case toolname.Read, toolname.ReadArtifact, toolname.ViewImage, toolname.Write, toolname.Edit, toolname.Delete, toolname.Lsp:
	default:
		return nil
	}
	var args toolArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(argsJSON)), &args); err != nil {
		return nil
	}
	path := strings.TrimSpace(args.Path)
	if path == "" {
		return nil
	}
	return []acp.ToolCallLocation{{Path: path}}
}

func toolCallStart(e agent.ToolCallStartEvent) acp.SessionUpdate {
	opts := []acp.ToolCallStartOpt{
		acp.WithStartKind(toolKind(e.Name)),
		acp.WithStartStatus(acp.ToolCallStatusPending),
	}
	if locations := toolLocations(e.Name, e.ArgsJSON); len(locations) > 0 {
		opts = append(opts, acp.WithStartLocations(locations))
	}
	if input := rawInput(e.ArgsJSON); input != nil {
		opts = append(opts, acp.WithStartRawInput(input))
	}
	return acp.StartToolCall(acp.ToolCallId(e.ID), toolTitle(e.Name, e.ArgsJSON), opts...)
}

// toolCallArgsUpdate forwards streamed tool arguments. The arguments are only
// surfaced once they are complete enough to be valid JSON.
func toolCallArgsUpdate(e agent.ToolCallUpdateEvent) (acp.SessionUpdate, bool) {
	var opts []acp.ToolCallUpdateOpt
	if input := rawInput(e.ArgsJSON); input != nil {
		opts = append(opts, acp.WithUpdateRawInput(input))
	}
	if e.ArgsStreamingDone {
		opts = append(opts, acp.WithUpdateTitle(toolTitle(e.Name, e.ArgsJSON)))
	}
	if len(opts) == 0 {
		return acp.SessionUpdate{}, false
	}
	return acp.UpdateToolCall(acp.ToolCallId(e.ID), opts...), true
}

func toolCallDiscard(e agent.ToolCallDiscardEvent) acp.SessionUpdate {
	// A discarded speculative card never ran, but ACP has no cancelled tool
	// status, so it is closed as failed instead of being left pending forever.
	return acp.UpdateToolCall(acp.ToolCallId(e.ID), acp.WithUpdateStatus(acp.ToolCallStatusFailed))
}

func toolCallExecutionUpdate(e agent.ToolCallExecutionEvent) (acp.SessionUpdate, bool) {
	switch e.State {
	case agent.ToolCallExecutionStateQueued, agent.ToolCallExecutionStateRunning:
		return acp.UpdateToolCall(acp.ToolCallId(e.ID), acp.WithUpdateStatus(acp.ToolCallStatusInProgress)), true
	default:
		// Receiving means the arguments are still streaming: the card stays
		// pending until execution starts.
		return acp.SessionUpdate{}, false
	}
}

func toolCallProgressUpdate(e agent.ToolProgressEvent) (acp.SessionUpdate, bool) {
	detail := progressText(e.Progress)
	if detail == "" {
		return acp.SessionUpdate{}, false
	}
	title := fmt.Sprintf("%s (%s)", toolLabel(e.Name), detail)
	return acp.UpdateToolCall(acp.ToolCallId(e.CallID), acp.WithUpdateTitle(title)), true
}

func progressText(progress agent.ToolProgressSnapshot) string {
	if text := strings.TrimSpace(progress.Text); text != "" {
		return text
	}
	if progress.Total > 0 {
		return fmt.Sprintf("%d/%d", progress.Current, progress.Total)
	}
	return strings.TrimSpace(progress.Label)
}

func toolCallResult(e agent.ToolResultEvent) acp.SessionUpdate {
	// Chord keeps cancellation as a distinct terminal state; ACP only knows
	// completed and failed, and a cancelled tool produced no valid result.
	status := acp.ToolCallStatusCompleted
	if e.Status != agent.ToolResultStatusSuccess {
		status = acp.ToolCallStatusFailed
	}
	opts := []acp.ToolCallUpdateOpt{acp.WithUpdateStatus(status)}
	if content := toolResultContent(e); len(content) > 0 {
		opts = append(opts, acp.WithUpdateContent(content))
	}
	output := strings.TrimSpace(e.Payload)
	if output == "" {
		output = strings.TrimSpace(e.Result)
	}
	if output != "" {
		opts = append(opts, acp.WithUpdateRawOutput(output))
	}
	return acp.UpdateToolCall(acp.ToolCallId(e.CallID), opts...)
}

// toolResultContent renders a finished tool call for the client: the tool's own
// output, plus the unified diff when the tool changed a file. Diffs travel as
// text because Chord's diff is already rendered and its file states no longer
// carry the pre-edit text ACP's diff content requires.
func toolResultContent(e agent.ToolResultEvent) []acp.ToolCallContent {
	var content []acp.ToolCallContent
	output := strings.TrimSpace(e.Payload)
	if output == "" {
		output = strings.TrimSpace(e.Result)
	}
	if output != "" {
		content = append(content, acp.ToolContent(acp.TextBlock(output)))
	}
	if diff := strings.TrimSpace(e.Diff); diff != "" {
		content = append(content, acp.ToolContent(acp.TextBlock(diff)))
	}
	return content
}
