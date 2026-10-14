package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// The repository line belongs to the session-context <env> block, not to the
// first user message, so the user's own text reaches the model untouched.
func TestSessionEnvBlockCarriesGitStatus(t *testing.T) {
	env := SessionEnvSnapshot{WorkDir: "/work", Platform: "linux/amd64", Date: "Mon Jan 2 2006", Git: getGitStatus(t.TempDir())}
	if got := env.renderEnvBlock(); !strings.Contains(got, "  Working directory: /work\n  Git repository: no\n  Platform: linux/amd64") {
		t.Fatalf("env block missing git line: %q", got)
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := getGitStatus(repo); got != "Git repository: yes (branch main, captured when the working directory was set up)" {
		t.Fatalf("getGitStatus = %q", got)
	}
}

func TestDetectVenvPath_FindsVenvUnderWorkDir(t *testing.T) {
	tmp := t.TempDir()
	venvDir := filepath.Join(tmp, ".venv")
	if err := os.MkdirAll(venvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvDir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectVenvPath(tmp, tmp)
	if got != venvDir {
		t.Fatalf("detectVenvPath(%q) = %q, want %q", tmp, got, venvDir)
	}
}

func TestDetectVenvPath_WalksUpToParentVenv(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "internal", "agent")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	venvDir := filepath.Join(root, ".venv")
	if err := os.MkdirAll(venvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvDir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectVenvPath(workDir, root)
	if got != venvDir {
		t.Fatalf("detectVenvPath(%q) = %q, want parent venv %q", workDir, got, venvDir)
	}
}

func TestDetectVenvPath_ReturnsSingleNearestVenvWithinProjectRoot(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "internal", "agent")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootVenv := filepath.Join(root, ".venv")
	nearestVenv := filepath.Join(root, "internal", "venv")
	for _, dir := range []string{rootVenv, nearestVenv} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := detectVenvPath(workDir, root)
	if got != nearestVenv {
		t.Fatalf("detectVenvPath(%q) = %q, want nearest single venv %q", workDir, got, nearestVenv)
	}
}

func TestDetectVenvPath_DoesNotWalkAboveProjectRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	workDir := filepath.Join(root, "internal", "agent")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parentVenv := filepath.Join(parent, ".venv")
	if err := os.MkdirAll(parentVenv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentVenv, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectVenvPath(workDir, root)
	if got != "" {
		t.Fatalf("detectVenvPath(%q) = %q, want empty when venv is above project root", workDir, got)
	}
}

func TestDetectVenvPath_PrefersDotVenv(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{".venv", "venv", "env"} {
		dir := filepath.Join(tmp, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := detectVenvPath(tmp, tmp)
	want := filepath.Join(tmp, ".venv")
	if got != want {
		t.Fatalf("detectVenvPath(%q) = %q, want %q (.venv takes precedence)", tmp, got, want)
	}
}

func TestDetectVenvPath_ReturnsEmptyWhenNoVenv(t *testing.T) {
	tmp := t.TempDir()

	got := detectVenvPath(tmp, tmp)
	if got != "" {
		t.Fatalf("detectVenvPath(%q) = %q, want empty string", tmp, got)
	}
}

func TestDetectVenvPath_ReturnsEmptyWhenDirMissingPyvenvCfg(t *testing.T) {
	tmp := t.TempDir()
	// Create .venv directory but without pyvenv.cfg — not a valid venv.
	if err := os.MkdirAll(filepath.Join(tmp, ".venv"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := detectVenvPath(tmp, tmp)
	if got != "" {
		t.Fatalf("detectVenvPath(%q) = %q, want empty string (no pyvenv.cfg)", tmp, got)
	}
}

func TestDetectVenvPath_ReturnsEmptyForEmptyWorkDir(t *testing.T) {
	got := detectVenvPath("", "")
	if got != "" {
		t.Fatalf("detectVenvPath(\"\") = %q, want empty string", got)
	}
}

func TestDetectVenvPath_FindsVenvNotEnv(t *testing.T) {
	tmp := t.TempDir()
	// Only "venv" exists (not ".venv").
	venvDir := filepath.Join(tmp, "venv")
	if err := os.MkdirAll(venvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvDir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectVenvPath(tmp, tmp)
	if got != venvDir {
		t.Fatalf("detectVenvPath(%q) = %q, want %q", tmp, got, venvDir)
	}
}

func TestBuildSystemPrompt_OmitsDynamicEnvFromSystemPrompt(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry(), cachedWorkDir: "/tmp/project", cachedVenvPath: "/tmp/project/.venv"}

	got := a.buildSystemPrompt()
	for _, want := range []string{
		"<env>",
		"Working directory:",
		"Today's date:",
		"Python virtual environment:",
	} {
		if strings.Contains(got, want) {
			t.Fatalf("buildSystemPrompt() should not contain dynamic env %q (now in reminder), got:\n%s", want, got)
		}
	}
}

func TestSessionEnvSnapshot_RendersVenvRelativeToWorkDir(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "internal", "agent")
	venvPath := filepath.Join(root, ".venv")
	a := &MainAgent{tools: tools.NewRegistry(), cachedWorkDir: workDir}
	a.setCachedVenvPath(venvPath)

	env := a.sessionEnvSnapshot()
	block := env.renderEnvBlock()
	if !strings.Contains(block, "Python virtual environment: ../../.venv") {
		t.Fatalf("env block missing relative venv line, got:\n%s", block)
	}
	if strings.Contains(block, venvPath) {
		t.Fatalf("env block should use a path relative to the working directory, got:\n%s", block)
	}
	if !strings.Contains(block, "Platform: ") {
		t.Fatalf("env block missing platform, got:\n%s", block)
	}
	if !strings.Contains(block, "Today's date: ") {
		t.Fatalf("env block missing date, got:\n%s", block)
	}
}

func TestSessionEnvSnapshot_OmitsVenvWhenAbsent(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry(), cachedWorkDir: "/tmp/project"}

	env := a.sessionEnvSnapshot()
	block := env.renderEnvBlock()
	if strings.Contains(block, "Python virtual environment") {
		t.Fatalf("env block should omit venv when venvPath is empty, got:\n%s", block)
	}
}

func TestSubAgentBuildSystemPrompt_OmitsDynamicEnv(t *testing.T) {
	reg := tools.NewRegistry()
	root := t.TempDir()
	workDir := filepath.Join(root, "internal", "agent")
	venvPath := filepath.Join(root, ".venv")
	s := &SubAgent{
		tools:    reg,
		workDir:  workDir,
		venvPath: venvPath,
		taskDesc: "Run tests",
	}

	got := s.buildSystemPrompt()
	for _, want := range []string{
		"<env>",
		"Working directory:",
		"Today's date:",
		"Python virtual environment:",
	} {
		if strings.Contains(got, want) {
			t.Fatalf("SubAgent buildSystemPrompt() should not contain dynamic env %q (now in reminder), got:\n%s", want, got)
		}
	}
}

func TestSubAgentBuildSystemPrompt_OmitsVenvWhenAbsent(t *testing.T) {
	reg := tools.NewRegistry()
	s := &SubAgent{
		tools:    reg,
		workDir:  "/tmp/project",
		taskDesc: "Run tests",
	}

	got := s.buildSystemPrompt()
	if strings.Contains(got, "Python virtual environment") {
		t.Fatalf("SubAgent buildSystemPrompt() unexpectedly included venv line when venvPath is empty, got:\n%s", got)
	}
}
