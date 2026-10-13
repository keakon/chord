package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestCanonicalEndpoint(t *testing.T) {
	cases := map[string]string{"HTTPS://Example.COM:443": "https://example.com/", "https://example.com/a/?b=2&a=1": "https://example.com/a/?b=2&a=1", "http://[::1]:80": "http://[::1]/"}
	for input, want := range cases {
		got, err := CanonicalEndpoint(input)
		if err != nil || got != want {
			t.Errorf("canonical = %q, %v; want %q", got, err, want)
		}
	}
	for _, input := range []string{"https://user:password@example.com/", "/relative", "https://example.com/#fragment", "https://example.com/#", "file:///tmp/file", "http://[fe80::1%25lo0]/"} {
		if _, err := CanonicalEndpoint(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestResourceBinding(t *testing.T) {
	for _, tc := range []struct {
		endpoint, resource string
		ok                 bool
	}{
		{"https://example.com/a/b?x=1", "https://example.com/a", true},
		{"https://example.com/a/b?x=1", "https://example.com/a?x=1", true},
		{"https://example.com/ab", "https://example.com/a", false},
		{"https://example.com/a?x=1", "https://example.com/a?x=2", false},
		{"https://example.com/a", "https://other.example/a", false},
		{"https://example.com/a/%2f/b", "https://example.com/a", false},
		{"https://example.com/a/%2e%2e/b", "https://example.com/a", false},
	} {
		if err := ValidateResourceBinding(tc.endpoint, tc.resource); (err == nil) != tc.ok {
			t.Errorf("binding %q %q = %v", tc.endpoint, tc.resource, err)
		}
	}
}

func TestWorkspaceIdentityLinkedAndBare(t *testing.T) {
	ctx := context.Background()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	run(root, "init", repo)
	run(repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial")
	linked := filepath.Join(root, "linked")
	run(repo, "worktree", "add", "--detach", linked)
	a, err := ResolveWorkspaceID(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResolveWorkspaceID(ctx, linked)
	if err != nil || a != b {
		t.Fatalf("linked identity differs: %v", err)
	}
	bareA := filepath.Join(root, "a.git")
	bareB := filepath.Join(root, "b.git")
	run(root, "init", "--bare", bareA)
	run(root, "init", "--bare", bareB)
	a, err = ResolveWorkspaceID(ctx, bareA)
	if err != nil {
		t.Fatal(err)
	}
	b, err = ResolveWorkspaceID(ctx, bareB)
	if err != nil || a == b {
		t.Fatalf("bare identities collide: %v", err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(bareA, link); err == nil {
		b, err = ResolveWorkspaceID(ctx, link)
		if err != nil || a != b {
			t.Fatalf("symlink identity differs: %v", err)
		}
	}
}

func TestTargetIdentitySlotsAndCopies(t *testing.T) {
	opts := ResolveTargetOptions{ContentRoot: t.TempDir(), Main: config.MCPConfig{
		"one":   {URL: "https://example.com/mcp", Headers: map[string]string{"X-Test": "value"}},
		"alias": {URL: "https://example.com:443/mcp", OAuth: &config.MCPOAuthConfig{Scopes: []string{"read"}}},
		"other": {URL: "https://example.com/mcp", OAuth: &config.MCPOAuthConfig{Issuer: "https://auth.example.com", ClientID: "sample"}},
		"query": {URL: "https://example.com/mcp?tenant=2"},
	}}
	targets, err := ResolveTargets(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Target{}
	for _, target := range targets {
		byName[target.Ref.ServerName] = target
	}
	if byName["one"].TargetID != byName["alias"].TargetID || byName["one"].ProfileID != byName["alias"].ProfileID {
		t.Fatal("aliases and scope-only changes must share a slot")
	}
	if byName["one"].ProfileID == byName["other"].ProfileID || byName["one"].TargetID == byName["query"].TargetID {
		t.Fatal("distinct credential identities collide")
	}
	opts.Main["one"].Headers["X-Test"] = "changed"
	if byName["one"].Config.Headers["X-Test"] != "value" {
		t.Fatal("target retained mutable configuration")
	}
}
