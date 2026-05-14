package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// ---------------------------------------------------------------------------
// Public types (shared between tool ↔ TUI)
// ---------------------------------------------------------------------------

// QuestionItem represents a single question to ask the user.
type QuestionItem struct {
	Question string           `json:"question"`
	Header   string           `json:"header"`
	Options  []QuestionOption `json:"options,omitempty"`
	Multiple bool             `json:"multiple,omitempty"`
}

// QuestionOption represents one selectable choice.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// QuestionAnswer holds the user's response to one question.
type QuestionAnswer struct {
	Header   string   `json:"header"`
	Selected []string `json:"selected"` // selected labels or free-text entries
}

// ---------------------------------------------------------------------------
// QuestionFunc — callback used to delegate user interaction to the TUI
// ---------------------------------------------------------------------------

// QuestionFunc is the callback invoked when the Question tool needs user input.
// It presents questions to the user one at a time and returns their answers.
// Each answer is a slice of selected option labels (or a single free-text entry).
type QuestionFunc func(ctx context.Context, questions []QuestionItem) ([]QuestionAnswer, error)

// ---------------------------------------------------------------------------
// QuestionTool
// ---------------------------------------------------------------------------

// QuestionTool asks the user one or more questions and returns their answers.
// It is read-only (does not mutate files or processes) and integrates with
// the TUI via a QuestionFunc callback injected at creation time.
type QuestionTool struct {
	questionFn QuestionFunc
}

// NewQuestionTool creates a QuestionTool with the given callback.
// The callback is responsible for presenting questions to the user (typically
// via TUI channels) and collecting answers.
func NewQuestionTool(fn QuestionFunc) *QuestionTool {
	return &QuestionTool{questionFn: fn}
}

func (QuestionTool) Name() string { return "Question" }

func (QuestionTool) Description() string {
	return "Ask the user one or more questions and wait for their answers. " +
		"Use this when you need user input for decisions. " +
		"Each question can have predefined options (single or multi-select). " +
		"Users can always type a free-text answer even when options are available. " +
		"If no options are provided, the question is free-text only. " +
		"Always write the question text, header, option labels, and option descriptions " +
		"in the user's current language."
}

func (QuestionTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type":        "array",
				"description": "List of questions to ask the user. Write all user-facing text in the user's current language.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{
							"type":        "string",
							"description": "The complete question to present to the user, written in the user's current language",
						},
						"header": map[string]any{
							"type":        "string",
							"description": "Very short label for the question (max 30 chars), written in the user's current language",
						},
						"options": map[string]any{
							"type":        "array",
							"description": "Available choices. Omit for pure free-text input. Write labels and descriptions in the user's current language.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"label": map[string]any{
										"type":        "string",
										"description": "Display text (1-5 words, concise), written in the user's current language",
									},
									"description": map[string]any{
										"type":        "string",
										"description": "Explanation of this choice, written in the user's current language",
									},
								},
								"required": []string{"label", "description"},
							},
						},
						"multiple": map[string]any{
							"type":        "boolean",
							"description": "Whether multiple selections are allowed (default false)",
						},
					},
					"required": []string{"question", "header"},
				},
			},
		},
		"required":             []string{"questions"},
		"additionalProperties": false,
	}
}

func (QuestionTool) IsReadOnly() bool { return true }

func (t *QuestionTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Questions []QuestionItem `json:"questions"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if len(args.Questions) == 0 {
		return "", fmt.Errorf("at least one question is required")
	}

	// Validate each question.
	for i, q := range args.Questions {
		if q.Question == "" {
			return "", fmt.Errorf("question[%d]: question text is required", i)
		}
		if q.Header == "" {
			return "", fmt.Errorf("question[%d]: header is required", i)
		}
	}

	if t.questionFn == nil {
		return "", fmt.Errorf("question callback not configured (running headless?)")
	}

	answers, err := t.questionFn(ctx, args.Questions)
	if err != nil {
		return "", fmt.Errorf("question failed: %w", err)
	}

	result, err := json.Marshal(answers)
	if err != nil {
		return "", fmt.Errorf("marshal answers: %w", err)
	}
	return string(result), nil
}
