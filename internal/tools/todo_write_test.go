package tools

import (
	"context"
	"strings"
	"testing"
)

type memTodoStore struct {
	items                   []TodoItem
	allowMultipleInProgress bool
}

func (m *memTodoStore) UpdateTodos(todos []TodoItem) error {
	m.items = append([]TodoItem(nil), todos...)
	return nil
}

func (m *memTodoStore) GetTodos() []TodoItem { return m.items }

func (m *memTodoStore) AllowMultipleInProgressTodos() bool { return m.allowMultipleInProgress }

func TestTodoWriteDescriptionIncludesFinalSyncGuidance(t *testing.T) {
	desc := (TodoWriteTool{}).Description()
	if !strings.Contains(desc, "Before the final response") {
		t.Fatalf("Description() missing final-response guidance: %q", desc)
	}
	if !strings.Contains(desc, "Delegate-enabled parallel work") {
		t.Fatalf("Description() missing Delegate parallel-work guidance: %q", desc)
	}
	if !strings.Contains(desc, "unique active_form") {
		t.Fatalf("Description() missing unique active_form requirement: %q", desc)
	}
}

func TestTodoWriteExecuteRejectsEmptyTodoList(t *testing.T) {
	store := &memTodoStore{}
	tool := NewTodoWriteTool(store)
	for _, raw := range [][]byte{
		[]byte(`{"todos":[]}`),
		[]byte(`{}`),
	} {
		t.Run(string(raw), func(t *testing.T) {
			_, err := tool.Execute(context.Background(), raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if len(store.items) != 0 {
				t.Fatalf("store should not be updated on error, got %+v", store.items)
			}
		})
	}
}

func TestTodoWriteExecuteMinimal(t *testing.T) {
	store := &memTodoStore{}
	tool := NewTodoWriteTool(store)
	raw := []byte(`{"todos":[{"id":"1","content":"A","status":"pending"}]}`)
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1. A") {
		t.Fatalf("unexpected output: %q", out)
	}
	if len(store.items) != 1 {
		t.Fatalf("stored todos: %+v", store.items)
	}
}

func TestTodoWriteExecuteRejectsMultipleInProgressWithoutPolicy(t *testing.T) {
	store := &memTodoStore{}
	tool := NewTodoWriteTool(store)
	raw := []byte(`{"todos":[{"id":"1","content":"A","status":"in_progress","active_form":"doing A"},{"id":"2","content":"B","status":"in_progress","active_form":"doing B"}]}`)
	if _, err := tool.Execute(context.Background(), raw); err == nil {
		t.Fatal("expected error for multiple in_progress items")
	}
	if len(store.items) != 0 {
		t.Fatalf("store should not be updated on error, got %+v", store.items)
	}
}

func TestTodoWriteExecuteAllowsMultipleInProgressWithDistinctActiveForms(t *testing.T) {
	store := &memTodoStore{allowMultipleInProgress: true}
	tool := NewTodoWriteTool(store)
	raw := []byte(`{"todos":[{"id":"main","content":"Coordinate implementation","status":"in_progress","active_form":"coordinating main flow"},{"id":"delegate-1","content":"Investigate parser","status":"in_progress","active_form":"delegate coder investigating parser"},{"id":"delegate-2","content":"Update docs","status":"in_progress","active_form":"delegate writer updating docs"}]}`)
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 3 {
		t.Fatalf("stored todos: %+v", store.items)
	}
	for _, want := range []string{"coordinating main flow", "delegate coder investigating parser", "delegate writer updating docs"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing active_form %q: %q", want, out)
		}
	}
}

func TestTodoWriteExecuteRejectsMultipleInProgressWithoutActiveForm(t *testing.T) {
	store := &memTodoStore{allowMultipleInProgress: true}
	tool := NewTodoWriteTool(store)
	raw := []byte(`{"todos":[{"id":"1","content":"A","status":"in_progress","active_form":"doing A"},{"id":"2","content":"B","status":"in_progress"}]}`)
	if _, err := tool.Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "active_form is required") {
		t.Fatalf("expected active_form error, got %v", err)
	}
	if len(store.items) != 0 {
		t.Fatalf("store should not be updated on error, got %+v", store.items)
	}
}

func TestTodoWriteExecuteRejectsMultipleInProgressWithDuplicateActiveForm(t *testing.T) {
	store := &memTodoStore{allowMultipleInProgress: true}
	tool := NewTodoWriteTool(store)
	raw := []byte(`{"todos":[{"id":"1","content":"A","status":"in_progress","active_form":"same workstream"},{"id":"2","content":"B","status":"in_progress","active_form":"same workstream"}]}`)
	if _, err := tool.Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "duplicates in_progress todo") {
		t.Fatalf("expected duplicate active_form error, got %v", err)
	}
	if len(store.items) != 0 {
		t.Fatalf("store should not be updated on error, got %+v", store.items)
	}
}

func TestTodoWriteExecuteIgnoresUnknownPriorityInJSON(t *testing.T) {
	// Old tool calls may still include "priority"; json.Unmarshal ignores unknown fields.
	store := &memTodoStore{}
	tool := NewTodoWriteTool(store)
	raw := []byte(`{"todos":[{"id":"1","content":"A","status":"pending","priority":"high"}]}`)
	_, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 || store.items[0].ID != "1" {
		t.Fatalf("stored: %+v", store.items)
	}
}

func TestTodoWriteParametersNoPriorityProperty(t *testing.T) {
	params := (TodoWriteTool{}).Parameters()
	props := params["properties"].(map[string]any)
	todos := props["todos"].(map[string]any)
	items := todos["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	if _, ok := itemProps["priority"]; ok {
		t.Fatal("priority should not appear in tool schema")
	}
}

func TestTodoWriteParametersExposeActiveForm(t *testing.T) {
	params := (TodoWriteTool{}).Parameters()
	props := params["properties"].(map[string]any)
	todos := props["todos"].(map[string]any)
	if !strings.Contains(todos["description"].(string), "Multiple in_progress items") {
		t.Fatalf("todos description missing multiple in_progress guidance: %q", todos["description"])
	}
	items := todos["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	if _, ok := itemProps["active_form"]; !ok {
		t.Fatal("active_form should appear in tool schema")
	}
}
