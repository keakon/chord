package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// samplePlan is the example from ARCHITECTURE.md §4.3.
const samplePlan = `# Implement user authentication

## Constraints
- No ORM
- Use bcrypt for password hashing

## Technical approach
MVC pattern with Go + Chi + SQLite

## Tasks

### 1. Create user database model
Create the user table schema and Go model struct in internal/models/user.go.

### 2. Implement password hashing (depends: 1)
Implement bcrypt-based password hashing and verification utilities.

### 3. Build login/register API endpoints (depends: 1, 2)
Implement HTTP handlers for user registration and login with JWT token generation.

### 4. Write integration tests (depends: 3)
Write end-to-end tests for the auth flow using httptest.
`

// ---------- ParseDocument tests ----------

func TestParseDocument_WellFormed(t *testing.T) {
	doc, err := ParseDocument(samplePlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if doc.Title != "Implement user authentication" {
		t.Errorf("title: got %q, want %q", doc.Title, "Implement user authentication")
	}

	wantConstraints := "- No ORM\n- Use bcrypt for password hashing"
	if doc.Constraints != wantConstraints {
		t.Errorf("constraints:\n got: %q\nwant: %q", doc.Constraints, wantConstraints)
	}

	if len(doc.Tasks) != 4 {
		t.Fatalf("tasks: got %d, want 4", len(doc.Tasks))
	}

	// Task 1: no dependencies, pending by default.
	task1 := doc.Tasks[0]
	if task1.ID != "1" {
		t.Errorf("task 1 ID: got %q", task1.ID)
	}
	if task1.Title != "Create user database model" {
		t.Errorf("task 1 title: got %q", task1.Title)
	}
	if task1.Description != "Create the user table schema and Go model struct in internal/models/user.go." {
		t.Errorf("task 1 desc: got %q", task1.Description)
	}
	if task1.Dependencies != nil {
		t.Errorf("task 1 deps: got %v, want nil", task1.Dependencies)
	}
	if task1.Status != StatusPending {
		t.Errorf("task 1 status: got %q, want %q", task1.Status, StatusPending)
	}

	// Task 2: depends on 1.
	if !reflect.DeepEqual(doc.Tasks[1].Dependencies, []string{"1"}) {
		t.Errorf("task 2 deps: got %v, want [1]", doc.Tasks[1].Dependencies)
	}

	// Task 3: depends on 1 and 2.
	if !reflect.DeepEqual(doc.Tasks[2].Dependencies, []string{"1", "2"}) {
		t.Errorf("task 3 deps: got %v, want [1 2]", doc.Tasks[2].Dependencies)
	}

	// Task 4: depends on 3.
	if !reflect.DeepEqual(doc.Tasks[3].Dependencies, []string{"3"}) {
		t.Errorf("task 4 deps: got %v, want [3]", doc.Tasks[3].Dependencies)
	}
}

func TestParseDocument_Dependencies(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantDeps []string
	}{
		{
			name:     "no dependencies",
			input:    "# P\n\n## Tasks\n\n### 1. Task\nDesc.\n",
			wantDeps: nil,
		},
		{
			name:     "single dependency",
			input:    "# P\n\n## Tasks\n\n### 2. Task (depends: 1)\nDesc.\n",
			wantDeps: []string{"1"},
		},
		{
			name:     "multiple dependencies",
			input:    "# P\n\n## Tasks\n\n### 5. Task (depends: 1, 3, 4)\nDesc.\n",
			wantDeps: []string{"1", "3", "4"},
		},
		{
			name:     "dependencies with extra spaces",
			input:    "# P\n\n## Tasks\n\n### 3. Task (depends:  2 , 1 )\nDesc.\n",
			wantDeps: []string{"2", "1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := ParseDocument(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(doc.Tasks) != 1 {
				t.Fatalf("tasks: got %d, want 1", len(doc.Tasks))
			}
			if !reflect.DeepEqual(doc.Tasks[0].Dependencies, tt.wantDeps) {
				t.Errorf("deps: got %v, want %v", doc.Tasks[0].Dependencies, tt.wantDeps)
			}
		})
	}
}

func TestParseDocument_StatusMarkers(t *testing.T) {
	input := `# Plan

## Tasks

### 1. Pending task
No marker means pending.

### 2. Active task [in_progress]
Currently being worked on.

### 3. Done task [completed]
Already finished.

### 4. Broken task [failed]
Something went wrong.

### 5. Explicit pending [pending]
Explicitly marked as pending.
`

	doc, err := ParseDocument(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Tasks) != 5 {
		t.Fatalf("tasks: got %d, want 5", len(doc.Tasks))
	}

	wantStatuses := []TaskStatus{
		StatusPending,
		StatusInProgress,
		StatusCompleted,
		StatusFailed,
		StatusPending,
	}
	wantTitles := []string{
		"Pending task",
		"Active task",
		"Done task",
		"Broken task",
		"Explicit pending",
	}
	for i := range doc.Tasks {
		if doc.Tasks[i].Status != wantStatuses[i] {
			t.Errorf("task %d status: got %q, want %q", i+1, doc.Tasks[i].Status, wantStatuses[i])
		}
		if doc.Tasks[i].Title != wantTitles[i] {
			t.Errorf("task %d title: got %q, want %q", i+1, doc.Tasks[i].Title, wantTitles[i])
		}
	}
}

func TestParseDocument_StatusWithDependencies(t *testing.T) {
	// Both dependency and status markers on the same task header.
	input := "# P\n\n## Tasks\n\n### 3. Build API (depends: 1, 2) [in_progress]\nWorking.\n"

	doc, err := ParseDocument(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Tasks) != 1 {
		t.Fatalf("tasks: got %d, want 1", len(doc.Tasks))
	}

	task := doc.Tasks[0]
	if task.Title != "Build API" {
		t.Errorf("title: got %q, want %q", task.Title, "Build API")
	}
	if !reflect.DeepEqual(task.Dependencies, []string{"1", "2"}) {
		t.Errorf("deps: got %v, want [1 2]", task.Dependencies)
	}
	if task.Status != StatusInProgress {
		t.Errorf("status: got %q, want %q", task.Status, StatusInProgress)
	}
}

// ---------- Missing/malformed section tests ----------

func TestParseDocument_Empty(t *testing.T) {
	doc, err := ParseDocument("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Title != "" {
		t.Errorf("title: got %q, want empty", doc.Title)
	}
	if doc.Constraints != "" {
		t.Errorf("constraints: got %q, want empty", doc.Constraints)
	}
	if len(doc.Tasks) != 0 {
		t.Errorf("tasks: got %d, want 0", len(doc.Tasks))
	}
}

func TestParseDocument_TitleOnly(t *testing.T) {
	doc, err := ParseDocument("# Just a title\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Title != "Just a title" {
		t.Errorf("title: got %q", doc.Title)
	}
	if doc.Constraints != "" {
		t.Errorf("constraints: got %q, want empty", doc.Constraints)
	}
	if len(doc.Tasks) != 0 {
		t.Errorf("tasks: got %d, want 0", len(doc.Tasks))
	}
}

func TestParseDocument_NoConstraints(t *testing.T) {
	input := "# Plan\n\n## Tasks\n\n### 1. Task\nDesc.\n"
	doc, err := ParseDocument(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Constraints != "" {
		t.Errorf("constraints: got %q, want empty", doc.Constraints)
	}
	if len(doc.Tasks) != 1 {
		t.Fatalf("tasks: got %d, want 1", len(doc.Tasks))
	}
}

func TestParseDocument_MalformedTaskHeader(t *testing.T) {
	// Non-numeric headers under ## Tasks are ignored.
	input := "# Plan\n\n## Tasks\n\n### Not a numbered task\nIgnored.\n\n### 1. Real task\nReal desc.\n"
	doc, err := ParseDocument(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Tasks) != 1 {
		t.Fatalf("tasks: got %d, want 1", len(doc.Tasks))
	}
	if doc.Tasks[0].ID != "1" || doc.Tasks[0].Title != "Real task" {
		t.Errorf("task: got ID=%q Title=%q", doc.Tasks[0].ID, doc.Tasks[0].Title)
	}
}

func TestParseDocument_UnknownSectionsIgnored(t *testing.T) {
	// Forward-compatible: unknown sections are silently skipped.
	input := `# Plan

## Constraints
- C1

## Technical approach
Some approach text.

## Tasks

### 1. Task
Desc.

## Notes
Some notes.
`
	doc, err := ParseDocument(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Title != "Plan" {
		t.Errorf("title: got %q", doc.Title)
	}
	if doc.Constraints != "- C1" {
		t.Errorf("constraints: got %q", doc.Constraints)
	}
	if len(doc.Tasks) != 1 {
		t.Fatalf("tasks: got %d, want 1", len(doc.Tasks))
	}
}

func TestParseDocument_MultiLineDescription(t *testing.T) {
	input := `# Plan

## Tasks

### 1. Complex task
First line of description.
Second line of description.

Third line after blank line.
`
	doc, err := ParseDocument(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Tasks) != 1 {
		t.Fatalf("tasks: got %d, want 1", len(doc.Tasks))
	}
	want := "First line of description.\nSecond line of description.\n\nThird line after blank line."
	if doc.Tasks[0].Description != want {
		t.Errorf("desc:\n got: %q\nwant: %q", doc.Tasks[0].Description, want)
	}
}

// ---------- Round-trip tests ----------

func TestRoundTrip(t *testing.T) {
	doc1, err := ParseDocument(samplePlan)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")
	if err := SaveDocument(doc1, path); err != nil {
		t.Fatalf("save: %v", err)
	}

	doc2, err := LoadDocument(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	assertDocumentsEqual(t, doc1, doc2)
}

func TestRoundTrip_WithStatuses(t *testing.T) {
	input := `# Plan with statuses

## Tasks

### 1. Done task [completed]
This is done.

### 2. Active task (depends: 1) [in_progress]
Working on it.

### 3. Pending task (depends: 1, 2)
Not started.
`
	doc1, err := ParseDocument(input)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")
	if err := SaveDocument(doc1, path); err != nil {
		t.Fatalf("save: %v", err)
	}

	doc2, err := LoadDocument(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	assertDocumentsEqual(t, doc1, doc2)
}

func TestRoundTrip_EmptyDescription(t *testing.T) {
	doc1 := &Document{
		Title: "Minimal plan",
		Tasks: []Task{
			{ID: "1", Title: "Task with no description", Status: StatusPending},
			{ID: "2", Title: "Another task", Dependencies: []string{"1"}, Status: StatusCompleted},
		},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")
	if err := SaveDocument(doc1, path); err != nil {
		t.Fatalf("save: %v", err)
	}

	doc2, err := LoadDocument(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	assertDocumentsEqual(t, doc1, doc2)
}

// ---------- LoadDocument / SaveDocument tests ----------

func TestLoadDocument_NotFound(t *testing.T) {
	_, err := LoadDocument("/nonexistent/path/plan.md")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestSaveDocument_CreatesDirectories(t *testing.T) {
	doc := &Document{
		Title: "Test",
		Tasks: []Task{
			{ID: "1", Title: "Task", Status: StatusPending},
		},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "plan.md")
	if err := SaveDocument(doc, path); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if doc.FilePath != path {
		t.Errorf("FilePath: got %q, want %q", doc.FilePath, path)
	}
}

func TestSaveDocument_UpdatesFilePath(t *testing.T) {
	doc := &Document{Title: "T"}
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")

	if err := SaveDocument(doc, path); err != nil {
		t.Fatalf("save: %v", err)
	}
	if doc.FilePath != path {
		t.Errorf("FilePath not updated: got %q, want %q", doc.FilePath, path)
	}
}

// ---------- Helpers ----------

// assertDocumentsEqual compares two Documents by value (ignoring FilePath).
func assertDocumentsEqual(t *testing.T, a, b *Document) {
	t.Helper()
	if a.Title != b.Title {
		t.Errorf("title: %q vs %q", a.Title, b.Title)
	}
	if a.Constraints != b.Constraints {
		t.Errorf("constraints:\n a: %q\n b: %q", a.Constraints, b.Constraints)
	}
	if len(a.Tasks) != len(b.Tasks) {
		t.Fatalf("task count: %d vs %d", len(a.Tasks), len(b.Tasks))
	}
	for i := range a.Tasks {
		ta, tb := a.Tasks[i], b.Tasks[i]
		if ta.ID != tb.ID {
			t.Errorf("task %d ID: %q vs %q", i, ta.ID, tb.ID)
		}
		if ta.Title != tb.Title {
			t.Errorf("task %d Title: %q vs %q", i, ta.Title, tb.Title)
		}
		if ta.Description != tb.Description {
			t.Errorf("task %d Description: %q vs %q", i, ta.Description, tb.Description)
		}
		if ta.Status != tb.Status {
			t.Errorf("task %d Status: %q vs %q", i, ta.Status, tb.Status)
		}
		if !reflect.DeepEqual(ta.Dependencies, tb.Dependencies) {
			t.Errorf("task %d Dependencies: %v vs %v", i, ta.Dependencies, tb.Dependencies)
		}
	}
}
