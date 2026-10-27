package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func newResolverFixture(t *testing.T) (*directoryInstructionRuntime, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binding := directoryInstructionBinding{CheckoutRoot: root, ContentRoot: root, WorkDir: root, FallbackAllowed: true}
	r := newDirectoryInstructionRuntime(context.Background(), binding)
	t.Cleanup(r.cancel)
	return r, root
}
func writeResolverRule(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
func discoverResolverDirectory(t *testing.T, r *directoryInstructionRuntime, dir string) directoryInstructionDiscovery {
	t.Helper()
	d, err := r.Discover(context.Background(), r.binding, []tools.InstructionTarget{{Path: dir, Mode: tools.InstructionTargetDirectory}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDirectoryInstructionResolverFreezesPositiveAndNegative(t *testing.T) {
	r, root := newResolverFixture(t)
	writeResolverRule(t, root, " root ")
	dir := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	d := discoverResolverDirectory(t, r, dir)
	if !d.Complete || len(d.Observations) != 3 || d.Observations["."].Body != "root" {
		t.Fatalf("unexpected discovery: %+v", d)
	}
	writeResolverRule(t, root, "changed")
	writeResolverRule(t, filepath.Join(root, "a"), "new")
	d = discoverResolverDirectory(t, r, dir)
	if d.Observations["."].Body != "root" || d.Observations["a"].Status != "absent" {
		t.Fatalf("snapshot changed: %+v", d)
	}
}
func TestDirectoryInstructionResolverPermissionAndHardLimits(t *testing.T) {
	for _, test := range []struct{ name, body, reason string }{{"size", strings.Repeat("x", maxDirectoryInstructionBytes+1), "size"}, {"encoding", string([]byte{255}), "encoding"}, {"limit", strings.Repeat("x", maxDirectoryInstructionBytes), ""}} {
		t.Run(test.name, func(t *testing.T) {
			r, root := newResolverFixture(t)
			writeResolverRule(t, root, test.body)
			d := discoverResolverDirectory(t, r, root)
			if d.Observations["."].Reason != test.reason {
				t.Fatalf("unexpected: %+v", d)
			}
		})
	}
	r, root := newResolverFixture(t)
	writeResolverRule(t, root, "valid")
	r.permissionCheck = func(context.Context, string) error { return fmt.Errorf("read denied") }
	d := discoverResolverDirectory(t, r, root)
	if d.Complete || d.Observations["."].Reason != "permission" || len(r.observed) != 0 {
		t.Fatalf("permission failure frozen: %+v", d)
	}
	r.permissionCheck = nil
	d = discoverResolverDirectory(t, r, root)
	if !d.Complete || d.Observations["."].Body != "valid" {
		t.Fatal(d)
	}
	r.permissionCheck = func(context.Context, string) error { return fmt.Errorf("read denied") }
	d = discoverResolverDirectory(t, r, root)
	if d.Complete {
		t.Fatal("cached body bypassed deny")
	}
}
func TestDirectoryInstructionResolverEntryAndContentSymlinks(t *testing.T) {
	r, root := newResolverFixture(t)
	writeResolverRule(t, root, "root")
	external := t.TempDir()
	target := filepath.Join(external, "file")
	if err := os.WriteFile(target, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []tools.InstructionTargetMode{tools.InstructionTargetEntry, tools.InstructionTargetContent} {
		d, err := r.Discover(context.Background(), r.binding, []tools.InstructionTarget{{Path: link, Mode: mode}})
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if mode == tools.InstructionTargetEntry {
			want = 1
		}
		if len(d.Observations) != want {
			t.Fatalf("mode %v: %+v", mode, d)
		}
	}
}
func TestDirectoryInstructionResolverNestedCheckoutAndCancellation(t *testing.T) {
	r, root := newResolverFixture(t)
	nested := filepath.Join(root, "nested")
	writeResolverRule(t, nested, "nested")
	if err := os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: elsewhere"), 0644); err != nil {
		t.Fatal(err)
	}
	d := discoverResolverDirectory(t, r, nested)
	if len(d.Observations) != 0 {
		t.Fatal("nested checkout absorbed")
	}
	<-r.discoveryToken
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Discover(ctx, r.binding, nil)
	r.discoveryToken <- struct{}{}
	if err != context.Canceled {
		t.Fatal(err)
	}
}
func TestDirectoryInstructionResolverNegativeObservationsNotEvicted(t *testing.T) {
	r, root := newResolverFixture(t)
	for i := range 1030 {
		discoverResolverDirectory(t, r, filepath.Join(root, fmt.Sprintf("dir-%d", i)))
	}
	writeResolverRule(t, filepath.Join(root, "dir-0"), "new")
	d := discoverResolverDirectory(t, r, filepath.Join(root, "dir-0"))
	if d.Observations["dir-0"].Status != "absent" {
		t.Fatal("negative observation evicted")
	}
}

func TestDirectoryInstructionResolverFallbackAndSourcePermissions(t *testing.T) {
	r, root := newResolverFixture(t)
	content, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.binding.ContentRoot = content
	r.binding.FallbackAllowed = true
	writeResolverRule(t, content, "fallback")
	d := discoverResolverDirectory(t, r, root)
	if d.Observations["."].Body != "fallback" || d.Observations["."].SourcePath != filepath.Join(content, "AGENTS.md") {
		t.Fatal(d)
	}
	r2 := newDirectoryInstructionRuntime(context.Background(), r.binding)
	t.Cleanup(r2.cancel)
	r2.permissionCheck = func(_ context.Context, path string) error {
		if path == filepath.Join(root, "AGENTS.md") {
			return fmt.Errorf("denied")
		}
		return nil
	}
	d = discoverResolverDirectory(t, r2, root)
	if d.Complete || d.Observations["."].Body != "" {
		t.Fatal("fallback bypassed deny")
	}
	r3 := newDirectoryInstructionRuntime(context.Background(), r.binding)
	t.Cleanup(r3.cancel)
	writeResolverRule(t, root, string([]byte{255}))
	d = discoverResolverDirectory(t, r3, root)
	if d.Observations["."].Reason != "encoding" {
		t.Fatal("fallback bypassed encoding block")
	}
}

// A rule file symlinked to a shared source outside the checkout is the author's
// own choice, so the rules load and are attributed to the scope holding the
// link. The ruleset's read permission still governs the target.
func TestDirectoryInstructionResolverFollowsExternalRuleSymlink(t *testing.T) {
	r, root := newResolverFixture(t)
	external, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeResolverRule(t, external, "external")
	target := filepath.Join(external, "AGENTS.md")
	if err := os.Symlink(target, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	d := discoverResolverDirectory(t, r, root)
	got := d.Observations["."]
	if !d.Complete || got.Status != "present" || got.Body != "external" || got.SourcePath != target {
		t.Fatalf("external rule symlink not followed: %+v complete=%v", got, d.Complete)
	}
	r2 := newDirectoryInstructionRuntime(context.Background(), r.binding)
	t.Cleanup(r2.cancel)
	r2.permissionCheck = func(_ context.Context, path string) error {
		if path == target {
			return fmt.Errorf("denied")
		}
		return nil
	}
	d = discoverResolverDirectory(t, r2, root)
	if d.Complete || d.Observations["."].Reason != "permission" {
		t.Fatalf("external target bypassed read permission: %+v", d.Observations["."])
	}
}
func TestDirectoryInstructionResolverCapacityPreservesEarlierSnapshots(t *testing.T) {
	r, root := newResolverFixture(t)
	writeResolverRule(t, root, strings.Repeat("a", maxDirectoryInstructionBytes))
	for i := range 4 {
		dir := filepath.Join(root, fmt.Sprintf("dir-%d", i))
		writeResolverRule(t, dir, strings.Repeat("b", maxDirectoryInstructionBytes))
		d := discoverResolverDirectory(t, r, dir)
		if i == 3 && d.Observations["dir-3"].Reason != "capacity" {
			t.Fatal("snapshot capacity not enforced")
		}
	}
	d := discoverResolverDirectory(t, r, root)
	if !d.Complete || len(d.Observations["."].Body) != maxDirectoryInstructionBytes {
		t.Fatal("existing snapshot lost")
	}
}
func TestDirectoryInstructionBindingUnrelatedWorkspaceDisablesFallback(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	binding, err := prepareDirectoryInstructionBinding(context.Background(), directoryInstructionBinding{ContentRoot: other, CheckoutRoot: root, WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if binding.FallbackAllowed {
		t.Fatal("unrelated roots accepted")
	}
}

func TestDirectoryInstructionResolverRetriesFailuresOnlyOnNextRequest(t *testing.T) {
	r, root := newResolverFixture(t)
	calls := 0
	r.permissionCheck = func(context.Context, string) error { calls++; return fmt.Errorf("denied") }
	targets := []tools.InstructionTarget{{Path: root, Mode: tools.InstructionTargetDirectory}}
	for range 2 {
		_, err := r.Discover(withDirectoryInstructionDiscoveryRequest(context.Background(), 1), r.binding, targets)
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("same request retried %d times", calls)
	}
	_, err := r.Discover(withDirectoryInstructionDiscoveryRequest(context.Background(), 2), r.binding, targets)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("new request did not retry: %d", calls)
	}
}

func TestDirectoryInstructionResolverHotDiscoveryDoesNotRepublish(t *testing.T) {
	r, root := newResolverFixture(t)
	discoverResolverDirectory(t, r, root)
	frozen := r.frozen.Load()
	for range 1000 {
		d := discoverResolverDirectory(t, r, root)
		if !d.Complete {
			t.Fatal(d)
		}
	}
	if r.frozen.Load() != frozen {
		t.Fatal("hot discovery unnecessarily recopied frozen observations")
	}
}
