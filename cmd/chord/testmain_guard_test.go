package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

// guardProbeEnv switches TestUserFileGuardProbe into its violating mode. It
// is only set on the subprocess spawned by TestUserFileGuardFailsBinary, so
// normal runs skip the probe entirely.
const guardProbeEnv = "CHORD_TEST_GUARD_PROBE"

func guardFileHash(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// TestStateFileGuardVerifyUnchanged covers the diff logic directly: a guard
// must stay silent when nothing moved and name every file that appeared,
// disappeared, or changed content.
func TestStateFileGuardVerifyUnchanged(t *testing.T) {
	dir := t.TempDir()
	paths := map[string]string{
		filepath.Join(dir, "config.yaml"):     "one",
		filepath.Join(dir, "auth.yaml"):       "two",
		filepath.Join(dir, "auth.state.json"): "", // absent at snapshot time
	}
	g := &stateFileGuard{snapshots: make(map[string]string, len(paths))}
	for path, content := range paths {
		if content == "" {
			g.snapshots[path] = ""
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		g.snapshots[path] = guardFileHash([]byte(content))
	}

	if err := g.verifyUnchanged(); err != nil {
		t.Fatalf("unchanged files reported as modified: %v", err)
	}

	// Mutate every guarded file: modify one, delete one, create one.
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("violated"), 0o600); err != nil {
		t.Fatalf("rewrite config.yaml: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "auth.yaml")); err != nil {
		t.Fatalf("remove auth.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.state.json"), []byte("new"), 0o600); err != nil {
		t.Fatalf("create auth.state.json: %v", err)
	}

	err := g.verifyUnchanged()
	if err == nil {
		t.Fatal("expected guard to report modifications")
	}
	msg := err.Error()
	for _, want := range []string{
		"modified:",
		"config.yaml (sha256:",
		"auth.yaml (sha256:",
		" -> absent)",
		"auth.state.json (absent -> sha256:",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("guard report %q missing %q", msg, want)
		}
	}
}

// TestUserFileGuardProbe is the violating test the guard exists to catch. It
// only runs in a subprocess whose CHORD roots are redirected, and refuses to
// run otherwise, so it can never touch the real user-level files.
func TestUserFileGuardProbe(t *testing.T) {
	if os.Getenv(guardProbeEnv) == "" {
		t.Skip("probe mode: only spawned by TestUserFileGuardFailsBinary")
	}
	if os.Getenv("CHORD_CONFIG_HOME") == "" || os.Getenv("CHORD_STATE_DIR") == "" {
		t.Skip("refusing to run: CHORD roots are not redirected away from the real home")
	}
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatalf("resolve config path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("guard probe violation"), 0o600); err != nil {
		t.Fatalf("write probe violation: %v", err)
	}
}

// TestUserFileGuardFailsBinary verifies the TestMain guard end to end: it
// spawns this same test binary with CHORD_CONFIG_HOME/CHORD_STATE_DIR pointed
// at temporary directories and the probe enabled, so the guard snapshots the
// redirected paths and the probe write must fail the whole subprocess with
// the guard report. No real user-level file is involved.
func TestUserFileGuardFailsBinary(t *testing.T) {
	if os.Getenv(guardProbeEnv) != "" {
		t.Skip("probe mode: this is the spawning side")
	}
	execPath, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}

	root := t.TempDir()
	configHome := filepath.Join(root, "config-home")
	stateDir := filepath.Join(root, "state-dir")

	cmd := exec.Command(execPath, "-test.run=^TestUserFileGuardProbe$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		guardProbeEnv+"=1",
		"CHORD_CONFIG_HOME="+configHome,
		"CHORD_STATE_DIR="+stateDir,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err = cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("subprocess did not exit with a failure: %v\nstderr:\n%s", err, stderr.String())
	}
	if code := exitErr.ExitCode(); code != 1 {
		t.Fatalf("subprocess exit code = %d, want 1\nstderr:\n%s", code, stderr.String())
	}

	out := stderr.String()
	if !strings.Contains(out, "user-level chord files were modified by tests") {
		t.Fatalf("subprocess failed without the guard report:\n%s", out)
	}
	if !strings.Contains(out, "(absent -> sha256:") {
		t.Fatalf("guard report does not show a previously absent file appearing:\n%s", out)
	}
	// Hermeticity proof: the reported violation must be under the redirected
	// roots, never a real home path.
	if !strings.Contains(out, configHome) {
		t.Fatalf("guard reported a path outside the redirected config home %s:\n%s", configHome, out)
	}
}
