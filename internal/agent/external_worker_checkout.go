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
	Head  string `json:"head"`
	Dirty bool   `json:"dirty,omitempty"`
}

func captureExternalCheckoutBaseline(dir string) *externalCheckoutBaseline {
	head := strings.TrimSpace(externalGitOutput(dir, "rev-parse", "HEAD"))
	if head == "" {
		return nil
	}
	// git already tracks the working tree state; asking it whether anything is
	// dirty costs one index scan and no file reads.
	dirty := strings.TrimSpace(externalGitOutput(dir, "status", "--porcelain=v1", "--untracked-files=normal")) != ""
	return &externalCheckoutBaseline{Head: head, Dirty: dirty}
}

func externalCheckoutCompletion(dir string, baseline *externalCheckoutBaseline, binding WorkDirState, summary string) *CompletionEnvelope {
	env := &CompletionEnvelope{Summary: strings.TrimSpace(summary), FileAttributionIncomplete: true,
		RemainingLimitations: []string{"Chord does not track which files the external process changed; the checkout is shared, so diff it against the reported base commit instead."}}
	if baseline == nil {
		env.RemainingLimitations = append(env.RemainingLimitations, "Git startup baseline is unavailable.")
		return env
	}
	env.Worktree = &CompletionWorktree{Path: dir, Name: binding.WorktreeID,
		Generation: binding.Generation, Base: baseline.Head,
		Branch: strings.TrimSpace(externalGitOutput(dir, "rev-parse", "--abbrev-ref", "HEAD"))}
	if !baseline.Dirty {
		env.Worktree.DiffStat = strings.TrimSpace(externalGitOutput(dir, "diff", "--stat", baseline.Head))
	} else {
		env.Worktree.DiffError = "Startup checkout was dirty; HEAD diff does not isolate this attempt."
	}
	return normalizeCompletionEnvelope(env)
}

func externalGitOutput(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), externalGitTimeout)
	defer cancel()
	commandArgs := append([]string{"-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", commandArgs...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
