package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// questionInputItem keeps input normalization fields out of stored questions.
type questionInputItem struct {
	QuestionItem
	WaitFor []string `json:"wait_for,omitempty"`
}

func DecodeQuestionArgs(raw json.RawMessage) (QuestionArgs, error) {
	var input struct {
		Questions []questionInputItem `json:"questions"`
		Wait      *bool               `json:"wait"`
		WaitFor   []string            `json:"wait_for"`
	}
	var args QuestionArgs
	if err := json.Unmarshal(raw, &input); err != nil {
		var single struct {
			Questions questionInputItem `json:"questions"`
			Wait      *bool             `json:"wait"`
			WaitFor   []string          `json:"wait_for"`
		}
		if e := json.Unmarshal(raw, &single); e != nil {
			return args, fmt.Errorf("invalid arguments: %w", err)
		}
		input.Questions = []questionInputItem{single.Questions}
		input.Wait, input.WaitFor = single.Wait, single.WaitFor
	}
	args.Wait, args.WaitFor = input.Wait, input.WaitFor
	if err := hoistNestedWaitFor(input.Questions, &args); err != nil {
		return args, err
	}
	// A blank ID cannot identify an existing question; optional placeholders
	// must not turn an otherwise valid creation into a combined create/wait.
	args.WaitFor = nonemptyWaitForIDs(args.WaitFor)
	switch {
	case len(args.Questions) == 0 && len(args.WaitFor) == 0:
		return args, fmt.Errorf("provide either nonempty questions or wait_for")
	case len(args.Questions) > 0 && len(args.WaitFor) > 0:
		return args, fmt.Errorf("questions and wait_for are mutually exclusive; provide only one")
	}
	if len(args.WaitFor) > 0 {
		if args.Wait != nil {
			return args, fmt.Errorf("wait is only valid when creating questions")
		}
		if len(args.WaitFor) > MaxPendingQuestions {
			return args, fmt.Errorf("too many wait_for IDs")
		}
		seen := map[string]bool{}
		for _, id := range args.WaitFor {
			if seen[id] {
				return args, fmt.Errorf("wait_for requires distinct nonempty IDs")
			}
			seen[id] = true
		}
	} else if err := ValidateQuestionItems(args.Questions); err != nil {
		return args, err
	}
	return args, nil
}

// hoistNestedWaitFor accepts a wait-only item without silently discarding any
// meaningful creation parameters. Mixed creation and waiting remain errors.
func hoistNestedWaitFor(items []questionInputItem, args *QuestionArgs) error {
	var nested []string
	for i, item := range items {
		if len(item.WaitFor) == 0 {
			args.Questions = append(args.Questions, item.QuestionItem)
			continue
		}
		var conflicts []string
		if strings.TrimSpace(item.Question) != "" {
			conflicts = append(conflicts, "question")
		}
		if strings.TrimSpace(item.Header) != "" {
			conflicts = append(conflicts, "header")
		}
		if len(item.Options) > 0 {
			conflicts = append(conflicts, "options")
		}
		if item.Multiple {
			conflicts = append(conflicts, "multiple")
		}
		if item.ResponsePolicy != "" {
			conflicts = append(conflicts, "response_policy")
		}
		if item.DefaultOptionID != "" {
			conflicts = append(conflicts, "default_option_id")
		}
		if len(conflicts) > 0 {
			return fmt.Errorf("question[%d]: wait_for cannot be combined with %s; use a wait-only item or questions alone to create new ones", i, strings.Join(conflicts, ", "))
		}
		nested = append(nested, item.WaitFor...)
	}
	if len(nested) == 0 {
		return nil
	}
	if len(args.WaitFor) > 0 {
		return fmt.Errorf("wait_for was given both at the top level and inside questions; provide it once")
	}
	args.WaitFor = nested
	return nil
}

func nonemptyWaitForIDs(ids []string) []string {
	out := ids[:0]
	for _, id := range ids {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
