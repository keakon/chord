package llm

import (
	"fmt"

	"github.com/keakon/chord/internal/message"
)

// RequestAttempt is the final semantic input surface for one transport attempt.
// Its slices are read-only and must not be retained for mutation by observers.
type RequestAttempt struct {
	Model        FallbackModel
	Messages     []message.Message
	Tools        []message.ToolDefinition
	SystemPrompt string
	InputBudget  int
}

// RequestInputBudgetError skips a target without submitting an oversized request.
type RequestInputBudgetError struct {
	ModelRef        string
	EstimatedTokens int
	InputBudget     int
}

func (e *RequestInputBudgetError) Error() string {
	return fmt.Sprintf("request for %s needs %d input tokens; available input budget is %d", e.ModelRef, e.EstimatedTokens, e.InputBudget)
}
