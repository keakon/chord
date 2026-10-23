package agent

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const externalGitTimeout = 3 * time.Second

// externalCheckoutBaseline is the only checkout evidence Chord keeps for an
// external worker: which commit the checkout was on and whether it was already
// dirty. It deliberately records no file content and no path list.
//
// Chord cannot attribute an external worker's writes — the CLI edits the
// checkout inside its own process, and other processes (including the user) may
// write the same checkout — so per-file tracking would cost a full-tree scan
// and still be wrong. The completion envelope therefore reports the checkout
// location and base commit and leaves the file list empty, rather than paying
// for a list that cannot be trusted.
type externalCheckoutBaseline struct {
	Head   string `json:"head"`
	Branch string `json:"branch,omitempty"`
	// BranchUnknown records that the branch could not be read at startup; it is
	// reported with the completion instead of silently leaving the field empty.
	BranchUnknown bool `json:"branch_unknown,omitempty"`
	// StatusUnknown separates "git could not be asked" from "git said clean":
	// a timed-out status check must never produce a trusted diff stat.
	StatusUnknown bool `json:"status_unknown,omitempty"`
	Dirty         bool `json:"dirty,omitempty"`
}

func captureExternalCheckoutBaseline(dir string) *externalCheckoutBaseline {
	// One rev-parse returns both identities; asking for the branch here also
	// pins it to the attempt's startup state.
	out, err := externalGitOutput(dir, "rev-parse", "HEAD", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	head := strings.TrimSpace(lines[0])
	if head == "" {
		return nil
	}
	baseline := &externalCheckoutBaseline{Head: head}
	if len(lines) >= 2 {
		baseline.Branch = strings.TrimSpace(lines[1])
	}
	baseline.BranchUnknown = baseline.Branch == ""
	// git already tracks the working tree state; asking it whether anything is
	// dirty costs one index scan and no file reads.
	status, err := externalGitOutput(dir, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		baseline.StatusUnknown = true
		return baseline
	}
	baseline.Dirty = strings.TrimSpace(status) != ""
	return baseline
}

func externalCheckoutCompletion(dir string, baseline *externalCheckoutBaseline, binding WorkDirState, summary string) *CompletionEnvelope {
	env := &CompletionEnvelope{Summary: strings.TrimSpace(summary), FileAttributionIncomplete: true,
		RemainingLimitations: []string{"Chord does not track which files the external process changed; the checkout is shared, so diff it against the reported base commit instead."}}
	if baseline == nil {
		env.RemainingLimitations = append(env.RemainingLimitations, "Git startup baseline is unavailable.")
		return env
	}
	env.Worktree = &CompletionWorktree{Path: dir, Name: binding.WorktreeID,
		Generation: binding.Generation, Base: baseline.Head, Branch: baseline.Branch}
	if baseline.BranchUnknown {
		env.RemainingLimitations = append(env.RemainingLimitations, "Git branch could not be determined at startup.")
	}
	switch {
	case baseline.StatusUnknown:
		env.Worktree.DiffError = "Git status could not be checked at startup; HEAD diff does not isolate this attempt."
	case baseline.Dirty:
		env.Worktree.DiffError = "Startup checkout was dirty; HEAD diff does not isolate this attempt."
	default:
		diff, err := externalGitOutput(dir, "diff", "--stat", baseline.Head)
		if err != nil {
			env.Worktree.DiffError = "Git diff against the base commit could not be computed."
		} else {
			env.Worktree.DiffStat = strings.TrimSpace(diff)
		}
	}
	return normalizeCompletionEnvelope(env)
}

func externalGitOutput(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), externalGitTimeout)
	defer cancel()
	commandArgs := append([]string{"-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", commandArgs...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
