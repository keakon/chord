package agent

import (
	"regexp"
	"strings"
)

type LoopState string

const (
	LoopStateIdle            LoopState = "idle"
	LoopStateExecuting       LoopState = "executing"
	LoopStateVerifying       LoopState = "verifying"
	LoopStateAssessing       LoopState = "assessing"
	LoopStateCompleted       LoopState = "completed"
	LoopStateBlocked         LoopState = "blocked"
	LoopStateBudgetExhausted LoopState = "budget_exhausted"
)

var loopDoneTagRE = regexp.MustCompile(`(?is)<done>\s*(.*?)\s*</done>`)
var loopBlockedTagRE = regexp.MustCompile(`(?is)<blocked>\s*(.*?)\s*</blocked>`)
var loopVerifyNotRunTagRE = regexp.MustCompile(`(?is)<verify-not-run>\s*(.*?)\s*</verify-not-run>`)

type LoopAssessmentAction string

const (
	LoopAssessmentActionNone            LoopAssessmentAction = "none"
	LoopAssessmentActionContinue        LoopAssessmentAction = "continue_from_context"
	LoopAssessmentActionVerify          LoopAssessmentAction = "verify"
	LoopAssessmentActionCompleted       LoopAssessmentAction = "completed"
	LoopAssessmentActionBlocked         LoopAssessmentAction = "blocked"
	LoopAssessmentActionBudgetExhausted LoopAssessmentAction = "budget_exhausted"
)

type LoopAssessment struct {
	Action  LoopAssessmentAction
	Message string
	Reasons []string
}

type LoopContinuationNote struct {
	Title    string
	Text     string
	DedupKey string
}

type loopRuntimeState struct {
	Enabled               bool
	State                 LoopState
	Target                string
	ConsecutiveNoProgress int
	LastProgressSignature string
	LastAssessmentMessage string
	ProgressVersion       uint64
	VerificationVersion   uint64
	LastAssessmentVersion uint64
	Iteration             int
	MaxIterations         int
	MaxIterationsSet      bool
}

func (s *loopRuntimeState) enable() {
	s.Enabled = true
	if s.State == "" {
		s.State = LoopStateIdle
	}
	if s.MaxIterations == 0 {
		if s.MaxIterationsSet {
			return
		}
		return
	}
	if s.MaxIterations < 0 {
		s.MaxIterations = 10
	}
}

func (s *loopRuntimeState) enableWithTarget(target string) {
	s.enable()
	if strings.TrimSpace(target) != "" {
		s.Target = strings.TrimSpace(target)
	}
}

func (s *loopRuntimeState) disable() {
	// Once loop mode is disabled, the UI should return to the same state as
	// regular idle mode instead of retaining a stale terminal loop badge.
	s.State = LoopStateIdle
	s.Target = ""
	s.Iteration = 0
	s.MaxIterations = 0
	s.MaxIterationsSet = false
	s.Enabled = false
}

func (s *loopRuntimeState) markProgress() {
	s.ProgressVersion++
}

func (s *loopRuntimeState) markVerificationProgress() {
	s.VerificationVersion++
	s.markProgress()
}

func (s *loopRuntimeState) advanceIteration() bool {
	s.Iteration++
	return s.MaxIterations > 0 && s.Iteration >= s.MaxIterations
}

func normalizeLoopProgressSignature(parts ...string) string {
	trimmed := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		trimmed = append(trimmed, part)
	}
	return strings.Join(trimmed, "|")
}

func extractLoopDoneReason(content string) string {
	matches := loopDoneTagRE.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return ""
	}
	// Allow multiple <done> tags and use the last non-empty reason as the
	// completion signal.
	for i := len(matches) - 1; i >= 0; i-- {
		if len(matches[i]) < 2 {
			continue
		}
		reason := strings.Join(strings.Fields(matches[i][1]), " ")
		if reason == "" {
			continue
		}
		return reason
	}
	return ""
}

func extractLoopBlockedReason(content string) string {
	matches := loopBlockedTagRE.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return ""
	}
	for i := len(matches) - 1; i >= 0; i-- {
		if len(matches[i]) < 2 {
			continue
		}
		reason := strings.Join(strings.Fields(matches[i][1]), " ")
		if reason == "" {
			continue
		}
		return reason
	}
	return ""
}

func extractLoopVerifyNotRunReason(content string) string {
	matches := loopVerifyNotRunTagRE.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return ""
	}
	for i := len(matches) - 1; i >= 0; i-- {
		if len(matches[i]) < 2 {
			continue
		}
		reason := strings.Join(strings.Fields(matches[i][1]), " ")
		if reason == "" {
			continue
		}
		return reason
	}
	return ""
}

func isVerificationLikeToolResult(payload *ToolResultPayload, result string) bool {
	if payload == nil {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(payload.Name))
	result = strings.ToLower(strings.TrimSpace(result))
	if name == "bash" {
		return strings.Contains(result, "go test") ||
			strings.Contains(result, "staticcheck") ||
			strings.Contains(result, "go vet") ||
			strings.Contains(result, "validation") ||
			strings.Contains(result, "replay")
	}
	return false
}
