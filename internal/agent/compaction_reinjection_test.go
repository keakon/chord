package agent

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
	"github.com/keakon/chord/internal/permission"
)

func TestCompactionReinjectionQuotaTokens(t *testing.T) {
	cases := []struct {
		name      string
		remaining int
		usable    int
		want      int
	}{
		{name: "no remaining budget", remaining: 0, usable: 1000, want: 0},
		{name: "negative remaining budget", remaining: -10, usable: 1000, want: 0},
		{name: "no usable budget", remaining: 100, usable: 0, want: 0},
		{name: "healthy window keeps a quarter of what is free", remaining: 1000, usable: 1000, want: 250},
		{name: "exactly at the working margin", remaining: 500, usable: 1000, want: 0},
		{name: "below the working margin", remaining: 400, usable: 1000, want: 0},
		{name: "just above the working margin", remaining: 502, usable: 1000, want: 2},
		{name: "margin binds before the share", remaining: 600, usable: 1000, want: 100},
		{name: "share and margin coincide", remaining: 200, usable: 300, want: 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactionReinjectionQuotaTokens(tc.remaining, tc.usable); got != tc.want {
				t.Fatalf("compactionReinjectionQuotaTokens(%d, %d) = %d, want %d", tc.remaining, tc.usable, got, tc.want)
			}
		})
	}
}

func TestCompactionReinjectionUsesRemainingInputBudget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	for _, window := range []int{8192, 120000} {
		for _, reserved := range []int{500, 3000} {
			a.ctxMgr.SetTokenBudgets(window, 3000, reserved)
			messages := []message.Message{{Role: message.RoleUser, Content: strings.Repeat("x", 7200)}}
			plan := a.compactionInjectedFileBudgets(messages)
			if plan.maxTotalBytes != 0 {
				t.Fatalf("window=%d reserved=%d: near-full input budget allows %d bytes", window, reserved, plan.maxTotalBytes)
			}
		}
	}
}

func TestCompactionKeyFilesDoNotCreateUserBoundary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "key.go"), []byte("package key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newTestMainAgent(t, root)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 0)
	messages := []message.Message{
		{Role: message.RoleUser, Content: "Continue the task"},
		{Role: message.RoleUser, IsCompactionSummary: true, Content: "## Files and Evidence\n- key.go\n\n## Next Step\n- continue"},
	}
	injected, index := a.injectCompactionFileContext(messages)
	if index != 2 {
		t.Fatalf("injection index = %d, want 2", index)
	}
	if injected[index].Kind != message.KindTurnOverlay || modelcompat.LastUserMessageIndex(injected) != 0 {
		t.Fatal("key-file overlay changed the real user boundary")
	}
}

// A checkpoint that already consumed the working margin must suppress the
// on-demand overlay instead of filling the window the compaction just freed.
// The same request surface re-injects once the window leaves room above the
// margin, which is what makes this a budget decision and not a file gate.
func TestInjectCompactionFileContextSkipsWhenPostCompactionMarginIsGone(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "key.go"), []byte("package key\n"), 0o644); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}

	const usable = 30000
	a.ctxMgr.SetTokenBudgets(usable, usable, 0)

	// Grow the checkpoint until it consumes two thirds of the usable budget:
	// the remaining third sits inside the margin the overlay must keep free.
	header := "## Files and Evidence\n- key.go\n\n## Next Step\n- continue\n\n"
	chunk := strings.Repeat("fixture padding line standing in for a long summary body\n", 100)
	msgs := []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: header}}
	for estimateMessagesTokens(a.ctxMgr, msgs) < 2*usable/3 {
		msgs[0].Content += chunk
	}

	plan := a.compactionInjectedFileBudgets(msgs)
	if plan.usableTokens != usable {
		t.Fatalf("usable_input_budget = %d, want %d", plan.usableTokens, usable)
	}
	if plan.remainingTokens <= 0 {
		t.Fatalf("fixture exhausted the budget instead of leaving a margin: remaining_tokens=%d", plan.remainingTokens)
	}
	if plan.remainingTokens > usable/compactionWorkingMarginDivisor {
		t.Fatalf("fixture must sit inside the working margin: remaining_tokens=%d", plan.remainingTokens)
	}
	if plan.quotaTokens != 0 || plan.maxTotalBytes != 0 {
		t.Fatalf("a checkpoint inside the working margin must suppress the overlay: quota_tokens=%d max_total_bytes=%d", plan.quotaTokens, plan.maxTotalBytes)
	}
	if got, gotIdx := a.injectCompactionFileContext(msgs); len(got) != len(msgs) || gotIdx != -1 {
		t.Fatalf("len(got) = %d idx = %d, want unchanged %d and -1", len(got), gotIdx, len(msgs))
	}

	// The same request surface re-injects against a wider window, where the
	// remaining budget sits above the margin.
	a.ctxMgr.SetTokenBudgets(120000, 120000, 0)
	wider, widerIdx := a.injectCompactionFileContext(msgs)
	if widerIdx != 1 || len(wider) != len(msgs)+1 {
		t.Fatalf("wider window must re-inject, insertedAt=%d len=%d", widerIdx, len(wider))
	}
}

// Every re-injected path records where the checkpoint declared it (source) on
// top of the read-time revision and the invalidation flag, and the overlay
// states that the body is a fresh read rather than the checkpoint snapshot.
func TestInjectCompactionFileContextRecordsFileSource(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".chord", "notes"), 0o755); err != nil {
		t.Fatalf("mkdir notes dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".chord", "notes", "task.md"), []byte("# objective\nship it\n"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "key.go"), []byte("package key\n"), 0o644); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 0)
	summary := "## Files and Evidence\n- key.go\n\n## Externalized State\n- .chord/notes/task.md\n\n## Next Step\n- continue"

	got, gotIdx := a.injectCompactionFileContext([]message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: summary}})
	if gotIdx != 1 || len(got) != 2 {
		t.Fatalf("insertedAt=%d len=%d, want 1 and 2", gotIdx, len(got))
	}
	overlay := got[1].Parts
	if !strings.Contains(overlay[0].Text, compactionFileCtxPrefix) || !strings.Contains(overlay[0].Text, "re-read from disk") {
		t.Fatalf("overlay intro must mark the body as a fresh read: %q", overlay[0].Text)
	}
	sources := map[string]string{}
	for _, part := range overlay[1:] {
		path, ok := message.FirstFileRefPath(part.Text)
		if !ok {
			continue
		}
		switch {
		case strings.Contains(part.Text, `source="`+compactionFileSourceStateFile+`"`):
			sources[path] = compactionFileSourceStateFile
		case strings.Contains(part.Text, `source="`+compactionFileSourceKeyFile+`"`):
			sources[path] = compactionFileSourceKeyFile
		default:
			sources[path] = ""
		}
	}
	want := map[string]string{
		".chord/notes/task.md": compactionFileSourceStateFile,
		"key.go":               compactionFileSourceKeyFile,
	}
	if !maps.Equal(sources, want) {
		t.Fatalf("re-injected file sources = %v, want %v", sources, want)
	}
}

// A window too small to hold the absolute default overlay budget must not
// receive it: the request-side admission gate measures the previous request's
// observed input, so an overlay-swollen request would only be caught by the
// provider rejection that follows. The overlay is bounded by a quarter of the
// window and skipped when even that is below the smallest useful fragment.
func TestInjectCompactionFileContextBoundsOverlayOnSmallWindow(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "key.go"), []byte("package key\n"), 0o644); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(3000, 3000, 0)
	msgs := []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: "## Files and Evidence\n- key.go\n\n## Next Step\n- continue"}}

	window := a.ctxMgr.GetMaxTokens()
	plan := a.compactionInjectedFileBudgets(msgs)
	if bound := estimateBytesForTokens(a.ctxMgr, window/compactionReinjectionShareDivisor); plan.maxTotalBytes > bound {
		t.Fatalf("overlay budget %d exceeds the small-window bound %d", plan.maxTotalBytes, bound)
	}
	if plan.maxTotalBytes >= compactionInjectedFilesMaxBytes {
		t.Fatalf("small window kept the absolute overlay budget: max_total_bytes=%d", plan.maxTotalBytes)
	}
	// A quarter of a 3K-token window is far below the smallest useful fragment,
	// so the overlay is skipped instead of injecting one that would push the
	// next request past the window.
	if plan.maxTotalBytes != 0 {
		t.Fatalf("quarter of a 3K window must not qualify as an overlay budget: max_total_bytes=%d", plan.maxTotalBytes)
	}
	if got, gotIdx := a.injectCompactionFileContext(msgs); len(got) != len(msgs) || gotIdx != -1 {
		t.Fatalf("small window injected an overlay it cannot hold: len=%d idx=%d", len(got), gotIdx)
	}

	// The same request surface re-injects against a wider window, which takes
	// the remaining-budget path.
	a.ctxMgr.SetTokenBudgets(120000, 120000, 0)
	if _, idx := a.injectCompactionFileContext(msgs); idx != 1 {
		t.Fatalf("wider window must inject, got insertedAt=%d", idx)
	}
}
