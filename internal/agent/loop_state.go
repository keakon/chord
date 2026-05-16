package agent

import (
	"encoding/json"
	"fmt"
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

var loopBlockedTagRE = regexp.MustCompile(`(?is)<blocked>\s*(.*?)\s*</blocked>`)
var loopVerifyNotRunTagRE = regexp.MustCompile(`(?is)<verify-not-run>\s*(.*?)\s*</verify-not-run>`)
var verificationWordTokenRE = regexp.MustCompile(`(^|[^a-z0-9_])(tox|nox|ava)([^a-z0-9_]|$)`)

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
	Action            LoopAssessmentAction
	Message           string
	Reasons           []string
	TriggerStopReason string
}

type LoopContinuationNote struct {
	Title    string
	Text     string
	DedupKey string
}

type loopRuntimeState struct {
	Enabled bool
	State   LoopState
	Target  string
	// DeferContinuationPromptUntilDone is enabled when /loop on is issued while
	// a turn is already running. In that mode, LOOP CONTINUE/VERIFY prompt
	// injection is suppressed until a terminal assistant stop_reason=done is
	// observed, or a Done tool exit attempt is rejected.
	DeferContinuationPromptUntilDone bool
	ConsecutiveNoProgress            int
	LastProgressSignature            string
	LastAssessmentMessage            string
	ProgressVersion                  uint64
	VerificationVersion              uint64
	LastAssessmentVersion            uint64
	Iteration                        int
	MaxIterations                    int
	MaxIterationsSet                 bool
}

func (s *loopRuntimeState) enable() {
	const defaultMaxAutoExitIntercepts = 10

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
		s.MaxIterations = defaultMaxAutoExitIntercepts
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
	s.DeferContinuationPromptUntilDone = false
	s.Enabled = false
}

func (s *loopRuntimeState) markProgress() {
	s.ProgressVersion++
}

func (s *loopRuntimeState) markVerificationProgress() {
	s.VerificationVersion++
	s.markProgress()
}

func (s *loopRuntimeState) recordAutoExitIntercept() int {
	s.Iteration++
	return s.Iteration
}

func (a *MainAgent) markLoopExitDecisionRequired() {
	a.emitToTUI(InfoEvent{Message: fmt.Sprintf("Loop requires user decision: automatic Done interception limit reached (%d). Approve exit or deny to continue.", a.loopState.MaxIterations)})
	a.clearCurrentTurnKeepLoopState()
}

func (a *MainAgent) clearCurrentTurnKeepLoopState() {
	a.turnMu.Lock()
	a.turn = nil
	a.turnMu.Unlock()
	a.setBugTriagePromptActive(false)
	a.emitActivity("main", ActivityIdle, "")
	if a.loopState.Enabled {
		a.loopState.State = LoopStateExecuting
	}
	a.emitLoopStateChanged()
	a.emitInteractiveToTUI(a.parentCtx, IdleEvent{})
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
	if name != "shell" {
		return false
	}

	command := ""
	if strings.TrimSpace(payload.ArgsJSON) != "" {
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(payload.ArgsJSON), &args); err == nil {
			command = strings.ToLower(strings.TrimSpace(args.Command))
		}
	}

	commandPatterns := []string{
		"go test", "gotestsum", "go vet", "staticcheck", "golangci-lint", "gofmt -w", "gofmt -d",
		"pytest", "python -m pytest", "uv run pytest", "tox", "nox",
		"npm test", "pnpm test", "yarn test", "bun test", "vitest", "jest", "mocha", "ava",
		"cargo test", "cargo clippy", "cargo fmt", "cargo check",
		"mvn test", "gradle test", "./gradlew test",
		"dotnet test", "rspec", "bundle exec rspec", "mix test", "rebar3 eunit",
		"validation", "replay",
	}
	resultPatterns := []string{
		"go test", "gotestsum", "go vet", "staticcheck", "golangci-lint", "gofmt -w", "gofmt -d",
		"pytest", "python -m pytest", "uv run pytest",
		"npm test", "pnpm test", "yarn test", "bun test", "vitest", "jest", "mocha",
		"cargo test", "cargo clippy", "cargo fmt", "cargo check",
		"mvn test", "gradle test", "./gradlew test",
		"dotnet test", "rspec", "bundle exec rspec", "mix test", "rebar3 eunit",
		"validation", "replay",
	}
	for _, pat := range commandPatterns {
		if isVerificationCommandMatch(command, pat) {
			return true
		}
	}
	for _, pat := range resultPatterns {
		if strings.Contains(result, pat) {
			return true
		}
	}
	return false
}

func isVerificationCommandMatch(command, pattern string) bool {
	if command == "" || pattern == "" {
		return false
	}
	if pattern == "tox" || pattern == "nox" || pattern == "ava" {
		return verificationWordTokenRE.MatchString(command)
	}
	return strings.Contains(command, pattern)
}
