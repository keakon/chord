package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TodoItem represents a single item in the todo list.
type TodoItem struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form,omitempty"` // current action being performed (e.g. "editing main.go")
}

// TodoStore is the interface for persisting todo state. Defined here to avoid
// circular imports — MainAgent implements this interface and injects it at
// construction time.
type TodoStore interface {
	UpdateTodos(todos []TodoItem) error
	GetTodos() []TodoItem
}

// TodoWriteTool updates the todo list by providing the complete replacement
// list. Only available to the MainAgent.
type TodoWriteTool struct {
	store TodoStore
}

// NewTodoWriteTool creates a TodoWriteTool backed by the given TodoStore.
func NewTodoWriteTool(store TodoStore) *TodoWriteTool {
	return &TodoWriteTool{store: store}
}

type todoWriteArgs struct {
	Todos []TodoItem `json:"todos"`
}

func (TodoWriteTool) Name() string { return "TodoWrite" }

func (TodoWriteTool) Description() string {
	return `Update the todo list by providing the complete list of items. This replaces the entire existing list.

## When to Use
1. Complex multi-step tasks (3+ steps)
2. Multi-step bug triage or investigation where explicit checkpoints help
3. After new instructions — capture as todos (order reflects execution order)
4. When starting a task — mark one item in_progress
5. After meaningful progress — update statuses / active_form
6. Before the final response — if you used TodoWrite, sync once more (all completed or cancelled)

## When NOT to Use
1. Single straightforward task
2. Fewer than ~3 trivial steps
3. Purely conversational or informational tasks`
}

func (TodoWriteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"todos": map[string]any{
				"type":        "array",
				"description": "Complete todo list (replaces existing list). Provide ALL items, not just changes. Array order is the intended execution order.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{
							"type":        "string",
							"description": "Unique task ID (e.g. '1', '2', 'adhoc-1')",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "Brief description of the task",
						},
						"status": map[string]any{
							"type":        "string",
							"description": "Task status",
							"enum":        []string{"pending", "in_progress", "completed", "cancelled"},
						},
						"active_form": map[string]any{
							"type":        "string",
							"description": "Optional current action phrase for an in-progress item (e.g. 'inspecting restore flow').",
						},
					},
					"required": []string{"id", "content", "status"},
				},
			},
		},
		"required":             []string{"todos"},
		"additionalProperties": false,
	}
}

func (TodoWriteTool) IsReadOnly() bool { return false }

var validStatuses = map[string]bool{"pending": true, "in_progress": true, "completed": true, "cancelled": true}

func (t *TodoWriteTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a todoWriteArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if len(a.Todos) == 0 {
		return "", fmt.Errorf(
			"TodoWrite: todos must contain at least one item; an empty list is invalid. " +
				"Provide id, content, and status for each task, or omit TodoWrite when no task list is needed",
		)
	}

	// Validate each todo item.
	seen := make(map[string]bool, len(a.Todos))
	inProgress := 0
	for i := range a.Todos {
		item := &a.Todos[i]
		if item.ID == "" {
			return "", fmt.Errorf("todos[%d]: id is required", i)
		}
		if seen[item.ID] {
			return "", fmt.Errorf("todos[%d]: duplicate id %q", i, item.ID)
		}
		seen[item.ID] = true

		if item.Content == "" {
			return "", fmt.Errorf("todos[%d]: content is required", i)
		}
		if !validStatuses[item.Status] {
			return "", fmt.Errorf("todos[%d]: invalid status %q (must be pending, in_progress, completed, or cancelled)", i, item.Status)
		}
		if item.Status == "in_progress" {
			inProgress++
		}
	}
	if inProgress > 1 {
		return "", fmt.Errorf("todo list may contain at most one in_progress item, got %d", inProgress)
	}

	if t.store == nil {
		return "", fmt.Errorf("todo storage not available (no TodoStore configured)")
	}

	if err := t.store.UpdateTodos(a.Todos); err != nil {
		return "", fmt.Errorf("failed to update todos: %w", err)
	}

	// Return the full todo list in markdown checklist format so the model
	// always has an accurate view of the current state.
	var sb strings.Builder
	for _, item := range a.Todos {
		var check string
		switch item.Status {
		case "completed":
			check = "x"
		case "cancelled":
			check = "-"
		default: // pending, in_progress
			check = " "
		}
		if item.Status == "in_progress" {
			fmt.Fprintf(&sb, "- [%s] **%s. %s**\n", check, item.ID, item.Content)
			if af := strings.TrimSpace(item.ActiveForm); af != "" {
				fmt.Fprintf(&sb, "  - active: %s\n", af)
			}
		} else {
			fmt.Fprintf(&sb, "- [%s] %s. %s\n", check, item.ID, item.Content)
		}
	}
	return sb.String(), nil
}
