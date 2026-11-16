package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/keakon/golog"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/externalsupervisor"
	"github.com/keakon/chord/internal/logtest"
	"github.com/keakon/chord/internal/trust"
)

func TestMain(m *testing.M) {
	// Host-level external-agent tests launch a real supervised worker. The
	// supervisor re-executes the running executable, which under `go test` is
	// this binary, so it must serve the hidden supervisor command before the
	// testing framework sees the argument. Same interception as the agent and
	// externalclaude test mains.
	if len(os.Args) > 1 && os.Args[1] == externalsupervisor.CommandName {
		os.Exit(externalsupervisor.Run(os.Args[2:]))
	}
	os.Exit(runTestsWithTemporaryRoot(m))
}

// Keep fallback spill files and subprocess scratch inside this test process's
// own directory. Never sweep the shared system temporary directory.
func runTestsWithTemporaryRoot(m *testing.M) (code int) {
	root, err := os.MkdirTemp("", "ch-cmd-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test temporary root:", err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			fmt.Fprintln(os.Stderr, "remove test temporary root:", err)
			code = 1
		}
	}()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if err := os.Setenv(key, root); err != nil {
			fmt.Fprintln(os.Stderr, "set test temporary root:", err)
			return 1
		}
	}
	// Guard the real user-level chord files: the process-wide CHORD_* roots
	// cannot be redirected here without breaking tests that depend on default
	// path resolution, shared state between parallel tests, or path-length
	// limits (takeover sockets). Instead, snapshot the real files before
	// m.Run and require them byte-identical afterwards, so any test that
	// forgets its own CHORD_CONFIG_HOME/CHORD_STATE_DIR isolation and writes
	// the real auth.yaml / auth.state.json / config.yaml / project-trust.json
	// fails the whole test binary.
	guard, err := snapshotUserLevelChordFiles()
	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshot user-level chord files:", err)
		return 1
	}
	setDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	code = m.Run()
	setDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	if err := guard.verifyUnchanged(); err != nil {
		fmt.Fprintf(os.Stderr, "user-level chord files were modified by tests: %v\n", err)
		return 1
	}
	return code
}

// userLevelChordFiles resolves the real user-level files chord may write.
// It must be called before any CHORD_* environment override: the config
// package resolves from the untouched environment and returns the paths
// under the real home directory.
func userLevelChordFiles() ([]string, error) {
	var paths []string
	for _, resolve := range []func() (string, error){
		config.ConfigPath,
		config.AuthPath,
		config.AuthStatePath,
	} {
		path, err := resolve()
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	locator, err := config.ResolvePathLocator(&config.Config{}, config.PathOptions{})
	if err != nil {
		return nil, err
	}
	return append(paths, trust.Path(locator.StateDir)), nil
}

type stateFileGuard struct {
	snapshots map[string]string // path -> "" when absent, else sha256 hex
}

func snapshotUserLevelChordFiles() (*stateFileGuard, error) {
	paths, err := userLevelChordFiles()
	if err != nil {
		return nil, err
	}
	guard := &stateFileGuard{snapshots: make(map[string]string, len(paths))}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			guard.snapshots[path] = ""
		case err != nil:
			return nil, fmt.Errorf("read %s: %w", path, err)
		default:
			guard.snapshots[path] = fmt.Sprintf("%x", sha256.Sum256(data))
		}
	}
	return guard, nil
}

// verifyUnchanged reports every guarded file that appeared, disappeared, or
// changed content during the test run.
func (g *stateFileGuard) verifyUnchanged() error {
	var changed []string
	for path, before := range g.snapshots {
		data, err := os.ReadFile(path)
		after := ""
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			changed = append(changed, fmt.Sprintf("%s (unreadable: %v)", path, err))
			continue
		default:
			after = fmt.Sprintf("%x", sha256.Sum256(data))
		}
		if after != before {
			changed = append(changed, fmt.Sprintf("%s (%s -> %s)", path,
				stateFileGuardState(before), stateFileGuardState(after)))
		}
	}
	if len(changed) == 0 {
		return nil
	}
	sort.Strings(changed)
	return fmt.Errorf("modified: %s", strings.Join(changed, "; "))
}

func stateFileGuardState(hash string) string {
	if hash == "" {
		return "absent"
	}
	return "sha256:" + hash[:12]
}
