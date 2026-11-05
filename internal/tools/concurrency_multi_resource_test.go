package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func multiResourcePolicy(mode ConcurrencyMode, resources ...string) ConcurrencyPolicy {
	return ConcurrencyPolicy{Resources: resources, Mode: mode}
}

func TestConcurrencyConflictUsesEveryResource(t *testing.T) {
	tests := []struct {
		name string
		a    ConcurrencyPolicy
		b    ConcurrencyPolicy
		want bool
	}{
		{
			name: "disjoint write sets batch together",
			a:    multiResourcePolicy(ConcurrencyModeWrite, "file:/a", "file:/b"),
			b:    multiResourcePolicy(ConcurrencyModeWrite, "file:/c"),
			want: false,
		},
		{
			name: "partial overlap with a write conflicts",
			a:    multiResourcePolicy(ConcurrencyModeWrite, "file:/a", "file:/b"),
			b:    multiResourcePolicy(ConcurrencyModeWrite, "file:/b", "file:/c"),
			want: true,
		},
		{
			name: "read sets share every resource",
			a:    multiResourcePolicy(ConcurrencyModeRead, "file:/a", "file:/b"),
			b:    multiResourcePolicy(ConcurrencyModeRead, "file:/b", "file:/c"),
			want: false,
		},
		{
			name: "a read set conflicts with one written member",
			a:    multiResourcePolicy(ConcurrencyModeRead, "file:/a", "file:/b"),
			b:    multiResourcePolicy(ConcurrencyModeWrite, "file:/b"),
			want: true,
		},
		{
			name: "workspace in either set overlaps everything",
			a:    multiResourcePolicy(ConcurrencyModeWrite, "file:/a"),
			b:    multiResourcePolicy(ConcurrencyModeWrite, "workspace"),
			want: true,
		},
		{
			name: "exclusive conflicts on unrelated resources",
			a:    multiResourcePolicy(ConcurrencyModeExclusive, "tool:question"),
			b:    multiResourcePolicy(ConcurrencyModeWrite, "file:/a", "file:/b"),
			want: true,
		},
		{
			name: "global is not a workspace wildcard",
			a:    multiResourcePolicy(ConcurrencyModeRead, "global"),
			b:    multiResourcePolicy(ConcurrencyModeWrite, "file:/a"),
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Conflicts(tc.b); got != tc.want {
				t.Fatalf("Conflicts() = %v, want %v", got, tc.want)
			}
			if got := tc.b.Conflicts(tc.a); got != tc.want {
				t.Fatalf("Conflicts() reversed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConcurrencyConflictResolvesSymlinkAndHardlinkAliases(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.go")
	if err := os.WriteFile(target, []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink.go")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(dir, "hardlink.go")
	if err := os.Link(target, hardlink); err != nil {
		t.Fatal(err)
	}
	disjoint := filepath.Join(dir, "disjoint.go")
	if err := os.WriteFile(disjoint, []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{symlink, hardlink} {
		same := multiResourcePolicy(ConcurrencyModeWrite, "file:"+target)
		alias := multiResourcePolicy(ConcurrencyModeWrite, "file:"+path)
		if !same.Conflicts(alias) {
			t.Fatalf("policy for %s did not conflict with its physical target", path)
		}
	}
	if multiResourcePolicy(ConcurrencyModeWrite, "file:"+target).Conflicts(
		multiResourcePolicy(ConcurrencyModeWrite, "file:"+disjoint)) {
		t.Fatal("disjoint files conflicted")
	}
}

func TestNormalizeConcurrencyResourcesOwnsSlice(t *testing.T) {
	input := []string{" file:/b ", "", "file:/a", "file:/b"}
	normalized := NormalizeConcurrencyResources(input)
	want := []string{"file:/a", "file:/b"}
	if !slices.Equal(normalized, want) {
		t.Fatalf("normalized = %v, want %v", normalized, want)
	}
	input[0] = "file:/mutated"
	input[2] = "file:/mutated"
	if !slices.Equal(normalized, want) {
		t.Fatalf("normalized = %v after input mutation, want %v", normalized, want)
	}
	if got := NormalizeConcurrencyResources(nil); got != nil {
		t.Fatalf("nil resources = %v, want nil", got)
	}
}

func TestResourceConflictIncludesSymlinkAncestors(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	holder := multiResourcePolicy(ConcurrencyModeWrite, "file:"+link)
	for _, resource := range []string{
		"file:" + filepath.Join(link, "child"),
		"path:" + filepath.Join(link, "subdir"),
		"file:" + filepath.Join(target, "child"),
		"path:" + filepath.Join(target, "subdir"),
	} {
		other := multiResourcePolicy(ConcurrencyModeRead, resource)
		if !holder.Conflicts(other) || !other.Conflicts(holder) ||
			!holder.LeaseConflicts(other) || !other.LeaseConflicts(holder) {
			t.Fatalf("ancestor deletion does not conflict with %s", resource)
		}
	}
	sibling := multiResourcePolicy(ConcurrencyModeWrite, "file:"+link+"-other")
	if holder.Conflicts(sibling) || holder.LeaseConflicts(sibling) {
		t.Fatal("a shared name prefix is not an ancestor")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	child := multiResourcePolicy(ConcurrencyModeRead, "file:"+filepath.Join(link, "child"))
	if !holder.Conflicts(child) || !holder.LeaseConflicts(child) {
		t.Fatal("entry dependencies must remain protected after unlink")
	}
}

func TestApplyPatchPolicyDeclaresEveryTarget(t *testing.T) {
	dir := t.TempDir()
	tool := ApplyPatchTool{BaseDir: dir}
	tests := []struct {
		name  string
		patch string
		want  []string
	}{
		{
			name: "multiple additions",
			patch: "*** Begin Patch\n" +
				"*** Add File: b.go\n+package b\n" +
				"*** Add File: a.go\n+package a\n" +
				"*** End Patch\n",
			want: []string{"file:" + filepath.Join(dir, "a.go"), "file:" + filepath.Join(dir, "b.go")},
		},
		{
			name: "rename declares source and target",
			patch: "*** Begin Patch\n" +
				"*** Update File: old.go\n*** Move to: new.go\n@@\n-old\n+new\n" +
				"*** End Patch\n",
			want: []string{"file:" + filepath.Join(dir, "new.go"), "file:" + filepath.Join(dir, "old.go")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args, err := json.Marshal(ApplyPatchArgs{Patch: tc.patch})
			if err != nil {
				t.Fatal(err)
			}
			policy := tool.ConcurrencyPolicy(args)
			if policy.Mode != ConcurrencyModeWrite {
				t.Fatalf("mode = %q, want write", policy.Mode)
			}
			if !slices.Equal(policy.Resources, tc.want) {
				t.Fatalf("resources = %v, want %v", policy.Resources, tc.want)
			}
			if !tool.ConcurrencyBatchable(args) {
				t.Fatal("a resolvable patch must be admitted as a batching candidate")
			}
		})
	}
}

func TestApplyPatchPolicyFallsBackToWorkspaceExclusive(t *testing.T) {
	tool := ApplyPatchTool{BaseDir: t.TempDir()}
	for _, args := range []json.RawMessage{
		json.RawMessage(`{"patch":"not a patch"}`),
		json.RawMessage(`{"patch":""}`),
		json.RawMessage(`{"patch":`),
	} {
		policy := tool.ConcurrencyPolicy(args)
		if policy.Mode != ConcurrencyModeExclusive || !slices.Equal(policy.Resources, []string{"workspace"}) {
			t.Fatalf("policy = %+v for %s, want workspace exclusive", policy, args)
		}
	}
}

func TestDeletePolicyDeclaresEveryTarget(t *testing.T) {
	dir := t.TempDir()
	args, err := json.Marshal(DeleteRequest{Paths: []string{"b.go", "a.go", "a.go"}, Reason: "cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	tool := DeleteTool{BaseDir: dir}
	policy := tool.ConcurrencyPolicy(args)
	want := []string{"file:" + filepath.Join(dir, "a.go"), "file:" + filepath.Join(dir, "b.go")}
	if policy.Mode != ConcurrencyModeWrite || !slices.Equal(policy.Resources, want) {
		t.Fatalf("policy = %+v, want write of %v", policy, want)
	}
	if !tool.ConcurrencyBatchable(args) {
		t.Fatal("a decoded delete must be admitted as a batching candidate")
	}
}

func TestDeletePolicyDecodeFailureStaysExclusive(t *testing.T) {
	tool := DeleteTool{BaseDir: t.TempDir()}
	policy := tool.ConcurrencyPolicy(json.RawMessage(`{"paths":["a.go"]}`))
	if policy.Mode != ConcurrencyModeExclusive || !slices.Equal(policy.Resources, []string{"tool:" + NameDelete}) {
		t.Fatalf("policy = %+v, want the exclusive default", policy)
	}
}
