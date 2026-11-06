package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCheckpointRequestProjectionSummarizesSuccessfulCalls(t *testing.T) {
	commands := []string{
		"[success] git status --short --branch && git log --reverse --format='%H %s'",
		"[success] make test-fast PKGS='./internal/agent' && make race-fast PKGS='./internal/agent'",
		"[error] make test-fast",
		"[cancelled] make race-fast",
		"[outcome_unknown] git commit",
		"[running] make build",
	}
	msg := message.Message{Role: message.RoleUser, IsCompactionSummary: true,
		Content: renderCheckpointWorklog(checkpointWorklog{commands: commands})}
	projected := projectCheckpointRequests([]message.Message{msg})
	got := projected[0].Content
	for _, success := range commands[:2] {
		if strings.Contains(got, success) {
			t.Fatalf("kept successful command: %s", success)
		}
	}
	for _, other := range commands[2:] {
		if !strings.Contains(got, other) {
			t.Fatalf("lost call outcome: %s", other)
		}
	}
	if !strings.Contains(got, "2 successful shell call(s)") || !strings.Contains(got, "archived history") {
		t.Fatalf("missing history reference:\n%s", got)
	}
	if len(got) >= len(msg.Content) {
		t.Fatal("projection grew the checkpoint")
	}
	if !reflect.DeepEqual(projectCheckpointRequests(projected), projected) {
		t.Fatal("projection not idempotent")
	}
	if !strings.Contains(msg.Content, commands[0]) {
		t.Fatal("durable command removed")
	}
}

func TestCheckpointRequestProjectionKeepsShortSuccessAndOmission(t *testing.T) {
	worklog := checkpointWorklog{commands: []string{"[success] pwd"}}
	msg := message.Message{Role: message.RoleUser, IsCompactionSummary: true,
		Content: renderCheckpointWorklog(worklog) + "\n\n" + checkpointWorklogOmittedNote}
	got := projectCheckpointRequests([]message.Message{msg})[0].Content
	if !strings.Contains(got, "[success] pwd") || !strings.Contains(got, checkpointWorklogOmittedNote) {
		t.Fatalf("lost short call or omission notice:\n%s", got)
	}
}

func TestCheckpointRequestProjectionTodoClassification(t *testing.T) {
	todos := []tools.TodoItem{
		{ID: "review", Status: tools.TodoStatusCompleted, Content: "Review the parser."},
		{ID: "test", Status: tools.TodoStatusInProgress, Content: "Test empty input.", ActiveForm: "testing input"},
	}
	for _, kind := range []string{"user_request", "", "done_rejected"} {
		t.Run(kind, func(t *testing.T) {
			anchor := fallbackAnchor{Kind: kind}
			if kind != "" {
				anchor.Text = "Check the parser."
			}
			body := "## Todo State\n" + formatTodosAsRelevanceBullets(todos, anchor) + "\n\n## Next Step\nRun the parser test."
			body = ensureCompactionTodoSnapshot(body, todos)
			msg := message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: body}
			got := projectCheckpointRequests([]message.Message{msg})[0].Content
			if kind == "done_rejected" {
				if got != body {
					t.Fatal("rejection classification changed")
				}
				return
			}
			if strings.Contains(got, "classified by fallback") {
				t.Fatal("empty classification retained")
			}
			for _, required := range []string{"[completed] review: Review the parser.", "[in_progress] test: Test empty input. | active: testing input", "## Next Step\nRun the parser test."} {
				if !strings.Contains(got, required) {
					t.Fatalf("lost %q", required)
				}
			}
		})
	}
}

func TestCheckpointRequestProjectionKeepsAuthoredTodos(t *testing.T) {
	body := "## Todo State\n- Active/relevant to latest request:\n  - [pending] inspect: Inspect the parser.\n### Runtime TODO snapshot\n  - [pending] inspect: Inspect the parser."
	msg := message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: body}
	if got := projectCheckpointRequests([]message.Message{msg})[0].Content; got != body {
		t.Fatal("authored todo classification changed")
	}
}

func TestCheckpointRequestProjectionEmptyTodo(t *testing.T) {
	body := "## Todo State\n" + formatTodosAsRelevanceBullets(nil, fallbackAnchor{}) + "\n\n## Next Step\nRead the parser."
	msg := message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: body}
	got := projectCheckpointRequests([]message.Message{msg})[0].Content
	if got != "## Next Step\nRead the parser." {
		t.Fatalf("unexpected projection: %s", got)
	}
}

func TestCheckpointRequestProjectionModeExplanation(t *testing.T) {
	body := buildCompactionCheckpointMessage("## Next Step\nRead the parser.", []string{"history-1.md"}, compactionSummaryModeModelDriven, nil)
	retained := "\n\n" + retainedRecentMessagesHeading + "\nUser:\n> Keep the parser behavior."
	evidence := "\n" + message.CompactionEvidenceTag + "\n" + checkpointModelDrivenModeNote
	msg := message.Message{Role: message.RoleUser, IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven, Content: body + retained + evidence}
	got := projectCheckpointRequests([]message.Message{msg})[0].Content
	if strings.Contains(strings.Split(got, retainedRecentMessagesHeading)[0], "no summarization model was called") {
		t.Fatal("diagnostic mode explanation retained")
	}
	for _, want := range []string{"Archived history is authoritative.", "history-1.md", retained, evidence} {
		if !strings.Contains(got, want) {
			t.Fatalf("lost %q", want)
		}
	}
}

func TestCheckpointRequestProjectionKeepsQuotedModeExplanation(t *testing.T) {
	for _, boundary := range []string{retainedRecentMessagesHeading, message.CompactionEvidenceTag} {
		body := "## Next Step\nRead the parser.\n\n" + boundary + "\nQuoted text:" + message.CompactionCompressedTag + "\n" + checkpointModelDrivenModeNote
		msg := message.Message{Role: message.RoleUser, IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven, Content: body}
		if got := projectCheckpointRequests([]message.Message{msg})[0].Content; got != body {
			t.Fatalf("quoted mode explanation changed after %s", boundary)
		}
	}
}
