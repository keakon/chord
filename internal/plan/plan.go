// Package plan implements parsing and management of plan documents.
//
// Plan documents are Markdown files stored in .chord/plans/*.md that describe
// tasks for the plan-execute workflow. Each document has a title, optional
// constraints, and a list of tasks with numeric IDs and optional dependencies.
//
// Task format:
//
//	### 1. Task title
//	Description text.
//
//	### 2. Another task (depends: 1) [in_progress]
//	Description with dependency and status marker.
//
// The parser accepts the Markdown task-list format used by the plan-execute workflow.
package plan

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TaskStatus represents the current status of a plan task.
type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"
	StatusInProgress TaskStatus = "in_progress"
	StatusCompleted  TaskStatus = "completed"
	StatusFailed     TaskStatus = "failed"
)

// Task represents a single task within a plan document.
type Task struct {
	ID           string     // Numeric ID parsed from "### N." headers; immutable, not execution order
	Title        string     // Task title text (without dependency/status markers)
	Description  string     // Task description body (may be multi-line)
	Dependencies []string   // IDs of tasks this task depends on (nil if none)
	Status       TaskStatus // Current task status (defaults to pending)
}

// Document represents a parsed plan document.
type Document struct {
	Title       string // Plan title from the H1 heading
	Constraints string // Raw text from the "## Constraints" section
	Tasks       []Task // Ordered list of tasks as they appear in the document
	FilePath    string // File path (set by LoadDocument/SaveDocument)
}

var (
	// taskHeaderRe matches "### <digits>. <rest>" task headings.
	taskHeaderRe = regexp.MustCompile(`^###\s+(\d+)\.\s+(.+)$`)
	// dependsRe extracts "(depends: 1, 2, 3)" from a task title.
	dependsRe = regexp.MustCompile(`\(depends:\s*([^)]+)\)`)
	// statusRe extracts "[status]" markers from a task title.
	statusRe = regexp.MustCompile(`\[(pending|in_progress|completed|failed)\]`)
)

// ParseDocument parses a Markdown plan file into a Document.
// Unknown sections (e.g. "## Technical approach") are silently ignored
// for forward compatibility.
func ParseDocument(content string) (*Document, error) {
	doc := &Document{}
	scanner := bufio.NewScanner(strings.NewReader(content))

	type sectionKind int
	const (
		sNone sectionKind = iota
		sConstraints
		sTasks
		sOther
	)

	cur := sNone
	var currentTask *Task
	var constraintLines []string
	var descLines []string

	finishTask := func() {
		if currentTask == nil {
			return
		}
		currentTask.Description = strings.TrimSpace(strings.Join(descLines, "\n"))
		doc.Tasks = append(doc.Tasks, *currentTask)
		currentTask = nil
		descLines = nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// H1: plan title (first occurrence wins).
		if strings.HasPrefix(trimmed, "# ") && !strings.HasPrefix(trimmed, "## ") {
			if doc.Title == "" {
				doc.Title = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
			}
			finishTask()
			cur = sNone
			continue
		}

		// H2: section boundary.
		if strings.HasPrefix(trimmed, "## ") && !strings.HasPrefix(trimmed, "### ") {
			finishTask()
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "##"))
			switch name {
			case "Constraints":
				cur = sConstraints
			case "Tasks":
				cur = sTasks
			default:
				cur = sOther
			}
			continue
		}

		// H3: task header (only within the Tasks section).
		if cur == sTasks {
			if m := taskHeaderRe.FindStringSubmatch(trimmed); m != nil {
				finishTask()

				id := m[1]
				rest := m[2]

				// Extract dependencies.
				var deps []string
				if dm := dependsRe.FindStringSubmatch(rest); dm != nil {
					for _, d := range strings.Split(dm[1], ",") {
						d = strings.TrimSpace(d)
						if d != "" {
							deps = append(deps, d)
						}
					}
					rest = dependsRe.ReplaceAllString(rest, "")
				}

				// Extract status marker.
				status := StatusPending
				if sm := statusRe.FindStringSubmatch(rest); sm != nil {
					status = TaskStatus(sm[1])
					rest = statusRe.ReplaceAllString(rest, "")
				}

				currentTask = &Task{
					ID:           id,
					Title:        strings.TrimSpace(rest),
					Dependencies: deps,
					Status:       status,
				}
				descLines = nil
				continue
			}
		}

		// Accumulate content for the current section.
		switch cur {
		case sConstraints:
			constraintLines = append(constraintLines, line)
		case sTasks:
			if currentTask != nil {
				descLines = append(descLines, line)
			}
		}
	}

	// Finalize last task.
	finishTask()

	doc.Constraints = strings.TrimSpace(strings.Join(constraintLines, "\n"))

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning plan document: %w", err)
	}

	return doc, nil
}

// LoadDocument reads a plan file from disk and parses it.
func LoadDocument(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading plan file: %w", err)
	}
	doc, err := ParseDocument(string(data))
	if err != nil {
		return nil, err
	}
	doc.FilePath = path
	return doc, nil
}

// SaveDocument writes a Document back to disk in Markdown format.
// It creates parent directories as needed and updates doc.FilePath.
func SaveDocument(doc *Document, path string) error {
	var b strings.Builder

	// Title.
	fmt.Fprintf(&b, "# %s\n", doc.Title)

	// Constraints.
	if doc.Constraints != "" {
		b.WriteString("\n## Constraints\n")
		b.WriteString(doc.Constraints)
		b.WriteString("\n")
	}

	// Tasks.
	if len(doc.Tasks) > 0 {
		b.WriteString("\n## Tasks\n")
		for _, task := range doc.Tasks {
			b.WriteString("\n### ")
			b.WriteString(task.ID)
			b.WriteString(". ")
			b.WriteString(task.Title)
			if len(task.Dependencies) > 0 {
				fmt.Fprintf(&b, " (depends: %s)", strings.Join(task.Dependencies, ", "))
			}
			if task.Status != "" && task.Status != StatusPending {
				fmt.Fprintf(&b, " [%s]", string(task.Status))
			}
			b.WriteString("\n")
			if task.Description != "" {
				b.WriteString(task.Description)
				b.WriteString("\n")
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating plan directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("writing plan file: %w", err)
	}
	doc.FilePath = path
	return nil
}
