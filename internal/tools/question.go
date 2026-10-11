package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	QuestionPolicyUserRequired    = "user_required"
	QuestionPolicyDefaultAllowed  = "default_allowed"
	QuestionPolicyOptional        = "optional"
	QuestionOutcomeAnswered       = "answered"
	QuestionOutcomeDefaulted      = "defaulted"
	QuestionOutcomeDeclined       = "declined"
	QuestionOutcomeNoResponse     = "no_response"
	QuestionOutcomeSuperseded     = "superseded"
	QuestionOutcomeCancelled      = "cancelled"
	QuestionOutcomeError          = "error"
	QuestionStatusAccepted        = "accepted"
	QuestionStatusResolved        = "resolved"
	QuestionStatusWaitInterrupted = "wait_interrupted"
	MaxQuestionBatch              = 3
	MaxPendingQuestions           = 32
	MaxQuestionTextBytes          = 8192
	MaxQuestionOptions            = 16
)

type QuestionItem struct {
	Question        string           `json:"question"`
	Header          string           `json:"header"`
	Options         []QuestionOption `json:"options,omitempty"`
	Multiple        bool             `json:"multiple,omitempty"`
	ResponsePolicy  string           `json:"response_policy,omitempty"`
	DefaultOptionID string           `json:"default_option_id,omitempty"`
}

type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type QuestionAnswer struct {
	QuestionID  string   `json:"question_id"`
	ResultID    string   `json:"result_id,omitempty"`
	Header      string   `json:"header"`
	Selected    []string `json:"selected,omitempty"`
	SelectedIDs []string `json:"selected_ids,omitempty"`
	Outcome     string   `json:"outcome"`
}

type QuestionArgs struct {
	Questions []QuestionItem `json:"questions,omitempty"`
	Wait      *bool          `json:"wait,omitempty"`
	WaitFor   []string       `json:"wait_for,omitempty"`
}

func (a QuestionArgs) Waits() bool { return a.Wait == nil || *a.Wait }

type QuestionResult struct {
	Status      string           `json:"status"`
	BatchID     string           `json:"batch_id,omitempty"`
	QuestionIDs []string         `json:"question_ids,omitempty"`
	Answers     []QuestionAnswer `json:"answers,omitempty"`
	PendingIDs  []string         `json:"pending_ids,omitempty"`
}

type QuestionFunc func(context.Context, QuestionArgs) (QuestionResult, error)

type QuestionTool struct {
	questionFn  QuestionFunc
	synchronous bool
}

func NewQuestionTool(fn QuestionFunc) *QuestionTool { return &QuestionTool{questionFn: fn} }
func (t *QuestionTool) Synchronous() *QuestionTool {
	return &QuestionTool{questionFn: t.questionFn, synchronous: true}
}
func (QuestionTool) Name() string         { return NameQuestion }
func (QuestionTool) IsReadOnly() bool     { return true }
func (t *QuestionTool) IsAvailable() bool { return t.questionFn != nil }
func (t *QuestionTool) Description() string {
	s := "Ask for user decisions in the user's language. Users can select options or enter text. user_required (default) never times out; default_allowed requires an explicit single-choice default; optional may close unanswered. A system default or missing answer is not user consent or authorization."
	if !t.synchronous {
		// The wait contract has to be spelled out here: nothing else tells the
		// model what a wait returns. A later wait reports only the outcome,
		// because the chosen values were delivered when each decision was made,
		// and a wait ends early when another decision in the task arrives.
		s += " Supply questions to create and wait, set wait=false to create and continue independent work, or supply wait_for IDs to block until those decisions arrive. wait_for is an argument of this tool, never a field of a question object; questions and wait_for are exclusive; wait applies only to creation. A wait for questions this call did not ask reports only their outcome, because the chosen values were delivered when each decision was made; a wait also returns early with pending_ids when another decision in the task arrives. Never poll or recreate pending questions."
	}
	return s
}

// questionItemSchema describes one entry of "questions". A create needs
// question and header, but the requirement is declared as an alternative rather
// than as the item's own required pair: a model waiting for a question it has
// already asked tends to hang wait_for off the question object it is looking at,
// and that item then carries wait_for and nothing else. DecodeQuestionArgs
// accepts that spelling and hoists the IDs, so the item schema has to admit it —
// the validator strips an undeclared field silently, and the call that follows
// then reads as "create", which is how one pending question becomes two.
func questionItemSchema(allowWaitFor bool) map[string]any {
	props := map[string]any{
		"question": map[string]any{"type": "string"}, "header": map[string]any{"type": "string", "description": "Short display label"},
		"options":           map[string]any{"type": "array", "maxItems": MaxQuestionOptions, "items": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "description": "Unique stable option ID"}, "label": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}}, "required": []string{"id", "label"}, "additionalProperties": false}},
		"multiple":          map[string]any{"type": "boolean", "description": "Allow multiple choices. Defaults to false."},
		"response_policy":   map[string]any{"type": "string", "enum": []string{QuestionPolicyUserRequired, QuestionPolicyDefaultAllowed, QuestionPolicyOptional}},
		"default_option_id": map[string]any{"type": "string", "description": "Required only for default_allowed; references options.id"},
	}
	alternatives := []map[string]any{{"required": []string{"question", "header"}}}
	if allowWaitFor {
		props["wait_for"] = map[string]any{"type": "array", "minItems": 1, "maxItems": MaxPendingQuestions, "items": map[string]any{"type": "string"}, "description": "Existing question IDs to wait for. Prefer the top-level wait_for; this spelling is accepted only on an item that carries nothing else, and is then treated exactly like the top-level argument."}
		alternatives = append(alternatives, map[string]any{"required": []string{"wait_for"}})
	}
	return map[string]any{"type": "object", "properties": props, "anyOf": alternatives, "additionalProperties": false}
}

func (t *QuestionTool) Parameters() map[string]any {
	properties := map[string]any{
		"questions": map[string]any{"type": "array", "minItems": 1, "maxItems": MaxQuestionBatch, "coerceFromObject": true, "items": questionItemSchema(!t.synchronous)},
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if t.synchronous {
		schema["required"] = []string{"questions"}
	} else {
		properties["wait"] = map[string]any{"type": "boolean", "description": "Creation only; default true"}
		properties["wait_for"] = map[string]any{"type": "array", "minItems": 1, "maxItems": MaxPendingQuestions, "items": map[string]any{"type": "string"}, "description": "Existing question IDs owned by the current task"}
	}
	return schema
}

func ValidateQuestionItems(items []QuestionItem) error {
	if len(items) == 0 || len(items) > MaxQuestionBatch {
		return fmt.Errorf("questions must contain 1 to %d items", MaxQuestionBatch)
	}
	for i, q := range items {
		if strings.TrimSpace(q.Question) == "" || strings.TrimSpace(q.Header) == "" {
			return fmt.Errorf("question[%d]: question and header are required", i)
		}
		if len(q.Question) > MaxQuestionTextBytes || len(q.Header) > 256 || len(q.Options) > MaxQuestionOptions {
			return fmt.Errorf("question[%d]: size limit exceeded", i)
		}
		ids := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.Label) == "" || ids[o.ID] || len(o.ID) > 128 || len(o.Label) > 1024 || len(o.Description) > MaxQuestionTextBytes {
				return fmt.Errorf("question[%d]: options require unique nonempty IDs and bounded labels/descriptions", i)
			}
			ids[o.ID] = true
		}
		switch q.ResponsePolicy {
		case "", QuestionPolicyUserRequired, QuestionPolicyOptional:
			if q.DefaultOptionID != "" {
				return fmt.Errorf("question[%d]: default requires default_allowed", i)
			}
		case QuestionPolicyDefaultAllowed:
			if q.Multiple || !ids[q.DefaultOptionID] {
				return fmt.Errorf("question[%d]: default_allowed requires a valid single-choice default", i)
			}
		default:
			return fmt.Errorf("question[%d]: unknown response_policy", i)
		}
	}
	return nil
}
func (t *QuestionTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	args, err := DecodeQuestionArgs(raw)
	if err != nil {
		return "", err
	}
	if t.synchronous && (len(args.WaitFor) > 0 || !args.Waits()) {
		return "", fmt.Errorf("asynchronous questions are available only to the main agent")
	}
	if t.questionFn == nil {
		return "", fmt.Errorf("question is unavailable")
	}
	result, err := t.questionFn(ctx, args)
	if err != nil {
		return "", fmt.Errorf("question failed: %w", err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal question result: %w", err)
	}
	return string(data), nil
}
