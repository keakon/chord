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
	Head string `json:"head"`
	// StatusUnknown separates "git could not be asked" from "git said clean":
	// a timed-out status check must never produce a trusted diff stat.
	StatusUnknown bool `json:"status_unknown,omitempty"`
	Dirty         bool `json:"dirty,omitempty"`
}

func captureExternalCheckoutBaseline(dir string) *externalCheckoutBaseline {
	head, err := externalGitOutput(dir, "rev-parse", "HEAD")
	head = strings.TrimSpace(head)
	if err != nil || head == "" {
		return nil
	}
	// git already tracks the working tree state; asking it whether anything is
	// dirty costs one index scan and no file reads.
	status, err := externalGitOutput(dir, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return &externalCheckoutBaseline{Head: head, StatusUnknown: true}
	}
	return &externalCheckoutBaseline{Head: head, Dirty: strings.TrimSpace(status) != ""}
}

func externalCheckoutCompletion(dir string, baseline *externalCheckoutBaseline, binding WorkDirState, summary string) *CompletionEnvelope {
	env := &CompletionEnvelope{Summary: strings.TrimSpace(summary), FileAttributionIncomplete: true,
		RemainingLimitations: []string{"Chord does not track which files the external process changed; the checkout is shared, so diff it against the reported base commit instead."}}
	if baseline == nil {
		env.RemainingLimitations = append(env.RemainingLimitations, "Git startup baseline is unavailable.")
		return env
	}
	branch, _ := externalGitOutput(dir, "rev-parse", "--abbrev-ref", "HEAD")
	env.Worktree = &CompletionWorktree{Path: dir, Name: binding.WorktreeID,
		Generation: binding.Generation, Base: baseline.Head, Branch: strings.TrimSpace(branch)}
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
