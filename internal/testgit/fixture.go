// Package testgit builds isolated Git repositories from cached, immutable fixtures.
package testgit

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type file struct {
	path string
	data []byte
	mode fs.FileMode
}

type fixture struct {
	once  sync.Once
	files []file
	err   error
}

var fixtures sync.Map

// NewRepository returns a private working tree with one commit on main. Each
// caller receives independent files, index and refs; only the immutable seed
// bytes are shared. No seed directory survives construction.
func NewRepository(t testing.TB, contents map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	data, err := json.Marshal(contents)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256(data)
	value, _ := fixtures.LoadOrStore(key, &fixture{})
	seed := value.(*fixture)
	seed.once.Do(func() { seed.files, seed.err = build(contents) })
	if seed.err != nil {
		t.Fatalf("build Git fixture: %v", seed.err)
	}
	root := t.TempDir()
	for _, f := range seed.files {
		path := filepath.Join(root, f.path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, f.data, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func build(contents map[string]string) ([]file, error) {
	root, err := os.MkdirTemp("", "chord-git-seed-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	for name, data := range contents {
		head, _, _ := strings.Cut(name, "/")
		if !fs.ValidPath(name) || !filepath.IsLocal(name) || strings.ContainsRune(name, '\\') || strings.EqualFold(head, ".git") {
			return nil, fmt.Errorf("invalid fixture path %q", name)
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			return nil, err
		}
	}
	for _, args := range [][]string{
		{"-c", "init.templateDir=", "init", "-q", "--initial-branch=main"},
		{"add", "."},
		{"-c", "commit.gpgsign=false", "-c", "core.hooksPath=", "commit", "-q", "--allow-empty", "-m", "fixture"},
		// The fixture outlives this build, so the isolation has to be part of
		// its configuration: every later commit — by a test helper or by the
		// code under test — must ignore the developer's global hook and signing
		// policy. A developer-global core.hooksPath would otherwise run inside
		// each of those commits (a hook that needs a review remote fails them
		// all). An empty core.hooksPath disables hook lookup.
		{"config", "core.hooksPath", ""},
		{"config", "commit.gpgsign", "false"},
	} {
		// No detached maintenance may mutate the repository while its files
		// are captured below or outlive the temporary seed directory.
		cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
		cmd.Dir = root
		cmd.Env = environment()
		if output, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %v: %w: %s", args, err, output)
		}
	}
	var files []file
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, file{path: rel, data: data, mode: info.Mode().Perm()})
		return nil
	})
	return files, err
}

// A seed must not inherit an index, object store or repository selected by the
// developer's shell. It also must not depend on user signing/hooks/configuration.
func environment() []string {
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
			env = append(env, entry)
		}
	}
	return append(env, "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0")
}
