package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// CompactContextArgs is the structured continuation state the model submits
// when requesting a model-driven context checkpoint. Every field is
// model-authored; runtime facts (current user request, todos, subagents,
// anchors, history map) are captured separately at checkpoint build time.
type CompactContextArgs struct {
	ActiveObjective string   `json:"active_objective"`
	Completed       []string `json:"completed"`
	Decisions       []string `json:"decisions"`
	OpenIssues      []string `json:"open_issues"`
	NextStep        string   `json:"next_step"`
	StateFiles      []string `json:"state_files"`
}

// TokenEstimator estimates the input-token cost of a string. Defaults to a
// conservative bytes/3 heuristic; the MainAgent runtime overrides it with the
// usage-calibrated estimator so the continuation-state budget uses the same
// accounting convention as other context-pressure decisions.
type TokenEstimator func(text string) int

// CompactContextValidator validates CompactContext tool arguments without
// touching the filesystem or the permission system: state_files stays a pure
// lexical workspace-relative path reference, so this tool can never act as
// a read-permission bypass or an existence probe.
type CompactContextValidator struct {
	// ContinuationStateMaxTokens caps the estimated token cost of all text
	// fields combined (matching the evidence-budget tier); zero means no cap.

	ContinuationStateMaxTokens int
	// EstimateTokens converts text to an estimated token count; nil falls
	// back to len(text)/3.
	EstimateTokens TokenEstimator
}

func (v CompactContextValidator) estimateTokens(text string) int {
	if v.EstimateTokens != nil {
		return v.EstimateTokens(text)
	}
	return len(text) / 3
}

// ParseCompactContextArgs trims all strings, rejects empty required fields,
// empty list items, arrays over their declared limits, fields over their
// declared length limits, and validates state-file constraints lexically. It
// never stats, reads, or resolves symlinks. The generic schema validator only
// enforces types and minItems, so the model-authored limits live here; both
// Execute and the MainAgent runtime barrier run this same parser.
func (v CompactContextValidator) ParseCompactContextArgs(raw json.RawMessage) (CompactContextArgs, error) {
	var args CompactContextArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return CompactContextArgs{}, fmt.Errorf("invalid arguments: %w", err)
	}
	args.ActiveObjective = strings.TrimSpace(args.ActiveObjective)
	args.NextStep = strings.TrimSpace(args.NextStep)
	if args.ActiveObjective == "" {
		return CompactContextArgs{}, fmt.Errorf("missing required argument: active_objective")
	}
	if args.NextStep == "" {
		return CompactContextArgs{}, fmt.Errorf("missing required argument: next_step")
	}
	var err error
	if args.Completed, err = validateCompactContextList(args.Completed, 12, 500, "completed"); err != nil {
		return CompactContextArgs{}, err
	}
	if args.Decisions, err = validateCompactContextList(args.Decisions, 8, 500, "decisions"); err != nil {
		return CompactContextArgs{}, err
	}
	if args.OpenIssues, err = validateCompactContextList(args.OpenIssues, 8, 500, "open_issues"); err != nil {
		return CompactContextArgs{}, err
	}
	if err := validateCompactContextField(args.ActiveObjective, 1000, "active_objective"); err != nil {
		return CompactContextArgs{}, err
	}
	if err := validateCompactContextField(args.NextStep, 1000, "next_step"); err != nil {
		return CompactContextArgs{}, err
	}
	stateFiles, err := validateStateFiles(args.StateFiles, 16, 512)
	if err != nil {
		return CompactContextArgs{}, err
	}
	args.StateFiles = stateFiles

	// The continuation-state budget uses the same usage-calibrated token
	// accounting as other context-pressure decisions. state_files paths are
	// model-authored text too and count against the budget, so a checkpoint's
	// self-description cannot crowd out an entire evidence tier.
	aggregated := strings.Join([]string{
		args.ActiveObjective,
		args.NextStep,
		strings.Join(args.Completed, "\n"),
		strings.Join(args.Decisions, "\n"),
		strings.Join(args.OpenIssues, "\n"),
		strings.Join(args.StateFiles, "\n"),
	}, "\n")
	cost := v.estimateTokens(aggregated)
	if limit := v.ContinuationStateMaxTokens; limit > 0 && cost > limit {
		return CompactContextArgs{}, fmt.Errorf("continuation state exceeds the token budget (estimated_cost=%d, budget=%d); shorten active_objective/next_step/completed/decisions/open_issues/state_files and retry", cost, limit)
	}
	return args, nil
}

// validateCompactContextField enforces the per-field rune cap declared in the
// JSON schema. Rune counts are only a cheap prefilter; the aggregated token
// estimate is the authoritative budget.
func validateCompactContextField(text string, maxRunes int, name string) error {
	if len([]rune(text)) > maxRunes {
		return fmt.Errorf("argument %s exceeds the %d-character limit", name, maxRunes)
	}
	return nil
}

// validateCompactContextList trims every item, rejects empty items and arrays
// over the declared maxItems/items.maxLength, and returns the trimmed items.
func validateCompactContextList(items []string, maxItems int, itemMaxRunes int, name string) ([]string, error) {
	if len(items) > maxItems {
		return nil, fmt.Errorf("argument %s contains %d items, exceeding the maximum of %d", name, len(items), maxItems)
	}
	out := make([]string, 0, len(items))
	for i, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("argument %s contains an empty item at index %d", name, i)
		}
		if len([]rune(item)) > itemMaxRunes {
			return nil, fmt.Errorf("argument %s item at index %d exceeds the %d-character limit", name, i, itemMaxRunes)
		}
		out = append(out, item)
	}
	return out, nil
}

// validateStateFiles applies the lexical workspace-relative path contract to
// state_files: "/"-separated, relative, no empty/./.. segments, no
// backslashes, no control characters or newlines. Duplicates are dropped,
// order preserved. The filesystem is never touched.
func validateStateFiles(paths []string, maxItems int, maxRunes int) ([]string, error) {
	if len(paths) > maxItems {
		return nil, fmt.Errorf("state_files contains %d paths, exceeding the maximum of %d", len(paths), maxItems)
	}
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for i, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("state_files contains an empty path at index %d", i)
		}
		if len([]rune(p)) > maxRunes {
			return nil, fmt.Errorf("state_files path at index %d exceeds the %d-character limit", i, maxRunes)
		}
		if hasControlChars(p) {
			return nil, fmt.Errorf("state_files path %q must not contain control characters or newlines", p)
		}
		if strings.Contains(p, `\`) {
			return nil, fmt.Errorf("state_files path %q must use '/' separators", p)
		}
		if strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("state_files path %q must be workspace-relative, not absolute", p)
		}
		if strings.HasPrefix(p, "~") {
			return nil, fmt.Errorf("state_files path %q must be workspace-relative, not home-relative", p)
		}
		rawSegments := strings.Split(p, "/")
		for _, seg := range rawSegments {
			if seg == "." || seg == ".." {
				return nil, fmt.Errorf("state_files path %q must not contain '.' or '..' path segments", p)
			}
		}
		cleaned := path.Clean(p)
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return nil, fmt.Errorf("state_files path %q escapes the project root", p)
		}
		// Normalize spellings like "a//b" to the clean form so deduplication
		// compares the same reference. "." / ".." segments are rejected above.
		p = cleaned
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// hasControlChars reports whether s contains C0 control characters or
// newlines. Model-authored state paths must never smuggle formatting that
// could break out of the checkpoint's Externalized State section.
func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// CompactContextTool requests a model-driven context checkpoint from the
// MainAgent runtime. Its Execute only validates the request and returns an
// "accepted" signal; the actual checkpoint barrier runs on the main event
// loop, so this tool never mutates session state by itself.
type CompactContextTool struct {
	validator CompactContextValidator
}

func NewCompactContextTool(validator CompactContextValidator) CompactContextTool {
	return CompactContextTool{validator: validator}
}

func (CompactContextTool) Name() string { return NameCompactContext }

func (CompactContextTool) Description() string {
	return "Request a durable context checkpoint once your current working state is fully externalized (written into state_files or fully expressible in structured arguments).\n" +
		"Runtime pauses the next main-model request, applies the checkpoint atomically, and continues the same turn on the compacted context. This involves a session history rewrite; it is NOT read-only.\n" +
		"Call it alone (no sibling tool calls in the same response) and only when:\n" +
		"- the current phase is wrapped up (" +
		"all investigation, sibling tools, user decisions, and pending verification are done" +
		");\n" +
		"- every fact needed later is captured in state_files or in the structured arguments;\n" +
		"- no key fact exists only in the current context that cannot be re-read or re-derived.\n" +
		"Do not call it when still investigating, waiting on siblings, or wanting a smaller context for its own sake;\n" +
		"do not call it when the context is already small (the runtime rejects low-gain resets).\n" +
		"The runtime may also skip the checkpoint when the minimum apply interval has not elapsed or projected savings are too small; that is a normal policy result, not an error, and retrying the same request repeatedly will not change the outcome.\n" +
		"Prefer Delegate (SubAgent) for separable sub-tasks whose results the main thread can consume; use compact_context only when the main thread itself must keep reasoning across the phase boundary.\n" +
		"A success result only means the request was accepted; a later model-driven [Context Summary] checkpoint confirms the reset was applied.\n" +
		"state_files entries are workspace-relative path references only: neither read nor injected automatically.\n" +
		"If the arguments are rejected, shorten them and retry; never work around the limits by splitting the checkpoint."
}

func (CompactContextTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"active_objective": map[string]any{
				"type":        "string",
				"minLength":   1,
				"maxLength":   1000,
				"description": "The single current goal to keep advancing after the reset. Do not restate the user request.",
			},
			"completed": map[string]any{
				"type":        "array",
				"maxItems":    12,
				"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
				"description": "Concrete progress completed and safe to rely on later.",
			},
			"decisions": map[string]any{
				"type":        "array",
				"maxItems":    8,
				"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
				"description": "Important decisions that must stay in effect, with a one-line reason each.",
			},
			"open_issues": map[string]any{
				"type":        "array",
				"maxItems":    8,
				"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
				"description": "Unresolved blockers, risks, or facts awaiting confirmation.",
			},
			"next_step": map[string]any{
				"type":        "string",
				"minLength":   1,
				"maxLength":   1000,
				"description": "One concrete action executable immediately after the checkpoint applies.",
			},
			"state_files": map[string]any{
				"type":        "array",
				"maxItems":    16,
				"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 512},
				"description": "Workspace-relative paths of files carrying externalized state. References only, never read nor injected automatically.",
			},
		},
		"required":             []string{"active_objective", "next_step"},
		"additionalProperties": false,
	}
}

func (CompactContextTool) IsReadOnly() bool { return false }

// ConcurrencyPolicy declares exclusive scheduling: compact_context rewrites
// session history and must never run alongside sibling tool calls.
func (CompactContextTool) ConcurrencyPolicy(_ json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{Resource: "tool:" + NameCompactContext, Mode: ConcurrencyModeExclusive}
}

func (t CompactContextTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	if _, err := t.validator.ParseCompactContextArgs(raw); err != nil {
		return "", err
	}
	return "Context checkpoint request accepted. No reset has occurred yet; only a later model-driven context checkpoint confirms successful application.", nil
}
