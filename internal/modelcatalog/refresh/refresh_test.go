package refresh

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
)

// sourceFiles are the minimal verified catalog sources for the upstream
// fixture repository: one endpoint, one model, one binding. The catalog
// version is filled in from the tag.
var sourceFiles = map[string]string{
	"catalog.yaml": `version: "2026-10-02.1"
`,
	"endpoints.yaml": `endpoints:
  - preset_id: openai
    protocol: responses
    request_url: https://api.openai.com/v1/responses
    auth_method: bearer
    env_var: OPENAI_API_KEY
    docs:
      - url: https://example.invalid/docs
        checked: 2026-10-01
`,
	"models.yaml": `models:
  - id: openai/gpt-6.1-sol
    context: 400000
    output: 128000
    sources:
      - url: https://example.invalid/model
        checked: 2026-10-01
`,
	"bindings.yaml": `bindings:
  - endpoint: openai
    wire_model_id: gpt-6.1-sol
    model_id: openai/gpt-6.1-sol
`,
}

const candidateFile = `wire_model_id: gpt-6.1-sol-messages
scope: gateway-a
model_id: openai/gpt-6.1-sol
context: 400000
sources:
  - url: https://example.invalid/sighting
    checked: 2026-10-02
`

// initUpstreamRepo creates a local git repository with the fixture sources,
// one commit, a candidate entry, and the given version tag. The returned path
// acts as the "remote" for refresh, so the test never touches a network.
func initUpstreamRepo(t *testing.T, tag string, withCandidates bool) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	for name, content := range sourceFiles {
		if name == "catalog.yaml" {
			// The catalog version follows the tag, so every test's fixture is
			// as new as its tag claims.
			content = `version: "` + strings.TrimPrefix(tag, "v") + `"
`
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if withCandidates {
		if err := os.MkdirAll(filepath.Join(dir, "candidates"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "candidates", "gpt-6.1-sol-messages.yaml"), []byte(candidateFile), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-b", "main")
	run("add", ".")
	run("commit", "-m", "fixture catalog")
	run("tag", tag)
	return dir
}

func TestParseLsRemoteTags(t *testing.T) {
	out := []byte(`1111111111111111111111111111111111111111	refs/tags/v2026-10-01.1
2222222222222222222222222222222222222222	refs/tags/v2026-10-01.1^{}
3333333333333333333333333333333333333333	refs/tags/v2026-09-30.3
4444444444444444444444444444444444444444	refs/tags/latest
5555555555555555555555555555555555555555	refs/heads/main
`)
	tags := parseLsRemoteTags(out)
	want := []string{"v2026-10-01.1", "v2026-09-30.3"}
	if len(tags) != len(want) {
		t.Fatalf("parseLsRemoteTags = %v, want %v", tags, want)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("parseLsRemoteTags = %v, want %v", tags, want)
		}
	}
	if got := newestTag(tags); got != "v2026-10-01.1" {
		t.Fatalf("newestTag = %q, want the highest version", got)
	}
}

func TestRunRefreshesInstallsAndRecordsOrigin(t *testing.T) {
	upstream := initUpstreamRepo(t, "v2026-10-02.1", true)
	cachePath := filepath.Join(t.TempDir(), "cache", "modelcatalog-cache.json")

	before := modelcatalog.OriginInfo()
	result, err := Run(context.Background(), upstream, cachePath)
	if err != nil {
		t.Fatalf("refresh run: %v", err)
	}
	if !result.Updated {
		t.Fatal("a newer upstream snapshot must update the cache")
	}
	if result.ToVersion != "2026-10-02.1" || result.Revision != "v2026-10-02.1" || result.CandidateCount != 1 {
		t.Fatalf("result = %+v, want the fixture snapshot identity", result)
	}
	if result.FromVersion != before.Version {
		t.Fatalf("result.FromVersion = %q, want the previously effective version %q", result.FromVersion, before.Version)
	}
	// The refreshed snapshot is in effect in this process right away.
	origin := modelcatalog.OriginInfo()
	if !origin.Cached || origin.Version != "2026-10-02.1" || origin.Source == nil || origin.Source.Revision != "v2026-10-02.1" {
		t.Fatalf("origin after refresh = %+v, want the fetched snapshot", origin)
	}
	if len(modelcatalog.EffectiveCandidates()) != 1 {
		t.Fatal("the refreshed candidate entry must be in effect")
	}
	// The cache file records the source and never contains credentials.
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	for _, want := range []string{`"revision": "v2026-10-02.1"`, `"version": "2026-10-02.1"`, "gpt-6.1-sol-messages"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("cache file missing %q:\n%s", want, data)
		}
	}
}

func TestRunSkipsWriteWhenNothingNewer(t *testing.T) {
	// A version strictly newer than any earlier test installs, so this test
	// passes in any order within the package binary.
	upstream := initUpstreamRepo(t, "v2026-10-03.1", false)
	cachePath := filepath.Join(t.TempDir(), "modelcatalog-cache.json")

	// First run installs the fixture snapshot (newer than the embedded one).
	if _, err := Run(context.Background(), upstream, cachePath); err != nil {
		t.Fatalf("first run: %v", err)
	}
	stamp, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}

	// A second run of the same tag must not rewrite the cache.
	result, err := Run(context.Background(), upstream, cachePath)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if result.Updated {
		t.Fatal("an equally new snapshot must not rewrite the cache")
	}
	again, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(stamp.ModTime()) {
		t.Error("the cache file must stay untouched when the snapshot is not newer")
	}
}

func TestRunFailsOnUntaggedUpstream(t *testing.T) {
	upstream := initUpstreamRepo(t, "v2026-10-02.1", false)
	// Remove the tag so the repository has no version tags to pin.
	cmd := exec.Command("git", "tag", "-d", "v2026-10-02.1")
	cmd.Dir = upstream
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("delete tag: %v: %s", err, out)
	}
	cachePath := filepath.Join(t.TempDir(), "modelcatalog-cache.json")
	if _, err := Run(context.Background(), upstream, cachePath); err == nil {
		t.Fatal("an upstream without version tags must fail; refresh never follows a branch head")
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatal("a failed refresh must not leave a cache file behind")
	}
}

func TestIsVersionTag(t *testing.T) {
	for tag, want := range map[string]bool{
		"v2026-10-01.1": true,
		"v1.2":          true,
		"latest":        false,
		"v":             false,
		"2026-10-01.1":  false,
	} {
		if got := isVersionTag(tag); got != want {
			t.Errorf("isVersionTag(%q) = %v, want %v", tag, got, want)
		}
	}
}
