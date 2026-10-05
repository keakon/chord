package refresh

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/modelcatalog/gen"
)

// Only this opt-in test uses an external repository. Ordinary refresh tests
// create their own repositories and remain independent of network access.
func TestPinnedCatalogRepository(t *testing.T) {
	if os.Getenv("CHORD_RUN_MODELCATALOG_INTEGRATION_TESTS") != "1" {
		t.Skip("set CHORD_RUN_MODELCATALOG_INTEGRATION_TESTS=1 to compare the pinned upstream catalog")
	}
	embedded, err := gen.Load(filepath.Join("..", "data"))
	if err != nil {
		t.Fatal(err)
	}
	if embedded.Source == nil {
		t.Fatal("embedded catalog has no pinned repository identity")
	}
	localPath, err := catalogTestLocalPath(os.Getenv("CHORD_MODELCATALOG_TEST_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	dir := catalogTestSnapshot(t, localPath, embedded.Source.Repository, embedded.Source.Revision)
	upstream, err := gen.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gen.LoadCandidates(dir, upstream); err != nil {
		t.Fatal(err)
	}
	// Source records publication identity rather than catalog contents; the
	// upstream does not have Chord's locally generated snapshot.yaml.
	upstream.Source = embedded.Source
	if !reflect.DeepEqual(upstream, embedded) {
		t.Fatal("pinned upstream catalog differs from the embedded source snapshot")
	}
}

func catalogTestLocalPath(configPath string) (string, error) {
	if configPath == "" {
		return "", nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	var config struct {
		LocalPath string `yaml:"local_path"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return "", err
	}
	if config.LocalPath == "" {
		return "", nil
	}
	if !filepath.IsAbs(config.LocalPath) {
		config.LocalPath = filepath.Join(filepath.Dir(configPath), config.LocalPath)
	}
	return filepath.Abs(config.LocalPath)
}

// Always clone into a test-owned directory at the pinned tag. A configured
// checkout is read-only, including its branch, index, and uncommitted files.
func catalogTestSnapshot(t *testing.T, localPath, repository, revision string) string {
	t.Helper()
	if localPath != "" {
		info, err := os.Stat(localPath)
		switch {
		case err == nil:
			if !info.IsDir() {
				t.Fatal("configured catalog local_path must be a directory")
			}
			repository = localPath
		case errors.Is(err, os.ErrNotExist):
			// An absent checkout falls back to the recorded upstream.
		default:
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	dir, err := cloneTag(ctx, repository, revision)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestCatalogTestConfigPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(configPath, []byte("local_path: ./catalog checkout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := catalogTestLocalPath(configPath)
	if err != nil || got != filepath.Join(dir, "catalog checkout") {
		t.Fatalf("relative path = %q, %v", got, err)
	}
	if err := os.WriteFile(configPath, []byte("local_path: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := catalogTestLocalPath(configPath); err != nil || got != "" {
		t.Fatalf("empty local path = %q, %v", got, err)
	}
	if err := os.WriteFile(configPath, []byte("unknown_path: catalog\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogTestLocalPath(configPath); err == nil {
		t.Fatal("unknown config key was accepted")
	}
}

func TestCatalogTestSnapshotUsesLocalOrClonesWhenAbsent(t *testing.T) {
	const revision = "v2099-01-01.1"
	upstream := initUpstreamRepo(t, revision, false)
	local := initUpstreamRepo(t, revision, true)
	// A dirty checkout must neither change the pinned snapshot nor be reset.
	const dirty = "uncommitted local catalog edit\n"
	if err := os.WriteFile(filepath.Join(local, "catalog.yaml"), []byte(dirty), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, localPath string
		candidates      int
	}{
		{"configured checkout", local, 1},
		{"absent checkout", filepath.Join(t.TempDir(), "absent"), 0},
		{"no configured path", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The fallback is also a fixture: these tests never access a
			// real remote, even when the configured local path is absent.
			dir := catalogTestSnapshot(t, tc.localPath, upstream, revision)
			catalog, err := gen.Load(dir)
			if err != nil || "v"+catalog.Version != revision {
				t.Fatalf("snapshot: %v, %v", catalog, err)
			}
			candidates, err := gen.LoadCandidates(dir, catalog)
			if err != nil || len(candidates) != tc.candidates {
				t.Fatalf("candidates = %v, %v", candidates, err)
			}
			if dir == upstream || dir == tc.localPath {
				t.Fatal("snapshot must not modify a configured checkout")
			}
		})
	}
	if data, err := os.ReadFile(filepath.Join(local, "catalog.yaml")); err != nil || string(data) != dirty {
		t.Fatalf("configured checkout was modified: %q, %v", data, err)
	}
}
