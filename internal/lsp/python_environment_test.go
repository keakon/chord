package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/testgit"
)

func TestPrepareWorkspaceSettingsFindsLinkedWorktreeVirtualenv(t *testing.T) {
	main, linked := pythonEnvironmentWorktrees(t)
	project := filepath.Join(linked, "api")
	mainProject := filepath.Join(main, "api")
	python := writeTestVirtualenv(t, mainProject, ".venv")
	writePythonEnvironmentFixture(t, filepath.Join(mainProject, "pyrightconfig.json"), `{"typeCheckingMode":"basic"}`, 0600)

	for _, boundary := range []string{linked, project} {
		manager := NewManager(&config.Config{}, boundary, nil)
		server := config.LSPServerConfig{Command: "pyright-langserver"}
		root, matched, ok := manager.discoverWorkspaceRoot("pyright", server, filepath.Join(project, "app.py"))
		if !ok || !matched || root != project {
			t.Fatalf("Pyright workspace = (%q, %v, %v), want %q", root, matched, ok, project)
		}
		settings := prepareWorkspaceSettings(t.Context(), "pyright", server, root, manager.settingsBoundary(root))
		if got := settingsForSection(settings, "python.pythonPath"); got != python {
			t.Fatalf("interpreter from %q = %q, want %q", boundary, got, python)
		}
	}

	// Local discovery still walks upward and does not depend on uv or project
	// metadata at the directory containing the environment.
	local := writeTestVirtualenv(t, linked, "venv")
	server := config.LSPServerConfig{Command: "pyright-langserver"}
	settings := prepareWorkspaceSettings(t.Context(), "pyright", server, project, linked)
	if got := settingsForSection(settings, "python.pythonPath"); got != local {
		t.Fatalf("local interpreter = %q, want %q", got, local)
	}
}

func TestLinkedWorktreeVirtualenvMatchesIncludedRequirements(t *testing.T) {
	main, linked := pythonEnvironmentWorktrees(t)
	mainProject := filepath.Join(main, "api")
	linkedProject := filepath.Join(linked, "api")
	python := writeTestVirtualenv(t, mainProject, ".venv")
	for _, project := range []string{mainProject, linkedProject} {
		writePythonEnvironmentFixture(t, filepath.Join(project, "requirements.txt"), "-r requirements/base.txt\n-c constraints.txt\n", 0600)
		writePythonEnvironmentFixture(t, filepath.Join(project, "requirements", "base.txt"), "sample==1.0\n", 0600)
		writePythonEnvironmentFixture(t, filepath.Join(project, "constraints.txt"), "-c constraints/common.txt\n", 0600)
		writePythonEnvironmentFixture(t, filepath.Join(project, "constraints", "common.txt"), "sample<2.0\n", 0600)
	}
	settings := prepareWorkspaceSettings(t.Context(), "pyright", config.LSPServerConfig{}, linkedProject, linked)
	if got := settingsForSection(settings, "python.pythonPath"); got != python {
		t.Fatalf("matching included requirements interpreter = %q, want %q", got, python)
	}

	writePythonEnvironmentFixture(t, filepath.Join(linkedProject, "requirements", "base.txt"), "sample==2.0\n", 0600)
	settings = prepareWorkspaceSettings(t.Context(), "pyright", config.LSPServerConfig{}, linkedProject, linked)
	if len(settings) != 0 {
		t.Fatalf("unexpected Pyright settings with different included requirements: %#v", settings)
	}
}

func TestLinkedWorktreeVirtualenvRejectsUnsafeRequirementIncludes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{name: "remote include", content: "-r https://example.invalid/requirements.txt\n"},
		{name: "cycle", content: "-r requirements/base.txt\n"},
		{name: "checkout escape", content: "-r ../../outside.txt\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main, linked := pythonEnvironmentWorktrees(t)
			writeTestVirtualenv(t, filepath.Join(main, "api"), ".venv")
			writePythonEnvironmentFixture(t, filepath.Join(main, "api", "requirements.txt"), tc.content, 0600)
			writePythonEnvironmentFixture(t, filepath.Join(linked, "api", "requirements.txt"), tc.content, 0600)
			if tc.name == "cycle" {
				for _, project := range []string{filepath.Join(main, "api"), filepath.Join(linked, "api")} {
					writePythonEnvironmentFixture(t, filepath.Join(project, "requirements", "base.txt"), "-r ../requirements.txt\n", 0600)
				}
			}
			settings := prepareWorkspaceSettings(t.Context(), "pyright", config.LSPServerConfig{}, filepath.Join(linked, "api"), linked)
			if len(settings) != 0 {
				t.Fatalf("unexpected Pyright settings for unsafe requirements include: %#v", settings)
			}
		})
	}
}

func TestLinkedWorktreeVirtualenvRejectsMismatches(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, main, linked string)
	}{
		{name: "different lock", change: func(t *testing.T, main, linked string) {
			writeTestVirtualenv(t, filepath.Join(main, "api"), ".venv")
			writePythonEnvironmentFixture(t, filepath.Join(linked, "api", "uv.lock"), "version = 2\n", 0600)
		}},
		{name: "different manifest", change: func(t *testing.T, main, linked string) {
			writeTestVirtualenv(t, filepath.Join(main, "api"), ".venv")
			writePythonEnvironmentFixture(t, filepath.Join(linked, "api", "pyproject.toml"), "[project]\nname = 'other'\n", 0600)
		}},
		{name: "different included requirements", change: func(t *testing.T, main, linked string) {
			writeTestVirtualenv(t, filepath.Join(main, "api"), ".venv")
			for _, dependency := range []struct {
				project string
				version string
			}{
				{project: filepath.Join(main, "api"), version: "1.0"},
				{project: filepath.Join(linked, "api"), version: "2.0"},
			} {
				writePythonEnvironmentFixture(t, filepath.Join(dependency.project, "requirements.txt"), "-r requirements/base.txt\n", 0600)
				writePythonEnvironmentFixture(t, filepath.Join(dependency.project, "requirements", "base.txt"), "sample=="+dependency.version+"\n", 0600)
			}
		}},
		{name: "no project metadata", change: func(t *testing.T, main, linked string) {
			writeTestVirtualenv(t, filepath.Join(main, "api"), ".venv")
			for _, root := range []string{main, linked} {
				if err := os.Remove(filepath.Join(root, "api", "pyproject.toml")); err != nil {
					t.Fatal(err)
				}
				writePythonEnvironmentFixture(t, filepath.Join(root, "api", "pyrightconfig.json"), "{}", 0600)
			}
		}},
		{name: "environment belongs to another directory", change: func(t *testing.T, main, _ string) {
			writeTestVirtualenv(t, main, ".venv")
		}},
		{name: "no virtualenv metadata", change: func(t *testing.T, main, _ string) {
			project := filepath.Join(main, "api")
			writeTestVirtualenv(t, project, ".venv")
			if err := os.Remove(filepath.Join(project, ".venv", "pyvenv.cfg")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "tracked virtualenv", change: func(t *testing.T, main, _ string) {
			writeTestVirtualenv(t, filepath.Join(main, "api"), ".venv")
			runPythonEnvironmentGit(t, main, "add", "--", "api/.venv/pyvenv.cfg")
		}},
		{name: "nested unrelated checkout", change: func(t *testing.T, main, _ string) {
			foreign := testgit.NewRepository(t, nil)
			project := filepath.Join(main, "api")
			if err := os.Rename(filepath.Join(foreign, ".git"), filepath.Join(project, ".git")); err != nil {
				t.Fatal(err)
			}
			writeTestVirtualenv(t, project, ".venv")
		}},
		{name: "nested linked checkout", change: func(t *testing.T, main, linked string) {
			project := filepath.Join(main, "api")
			names := []string{"pyproject.toml", "uv.lock"}
			for _, name := range names {
				if err := os.Remove(filepath.Join(project, name)); err != nil {
					t.Fatal(err)
				}
			}
			runPythonEnvironmentGit(t, main, "worktree", "add", "--detach", project, "HEAD")
			for _, name := range names {
				content, err := os.ReadFile(filepath.Join(linked, "api", name))
				if err != nil {
					t.Fatal(err)
				}
				writePythonEnvironmentFixture(t, filepath.Join(project, name), string(content), 0600)
			}
			writeTestVirtualenv(t, project, ".venv")
		}},
		{name: "no environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main, linked := pythonEnvironmentWorktrees(t)
			if tc.change != nil {
				tc.change(t, main, linked)
			}
			settings := prepareWorkspaceSettings(t.Context(), "pyright", config.LSPServerConfig{}, filepath.Join(linked, "api"), linked)
			if len(settings) != 0 {
				t.Fatalf("unexpected Pyright settings: %#v", settings)
			}
		})
	}
}

func TestLinkedWorktreeVirtualenvPrefersMainThenMatchingCheckout(t *testing.T) {
	main, linked := pythonEnvironmentWorktrees(t)
	first := filepath.Join(filepath.Dir(linked), "a")
	last := filepath.Join(filepath.Dir(linked), "z")
	runPythonEnvironmentGit(t, main, "worktree", "add", "--detach", last, "HEAD")
	runPythonEnvironmentGit(t, main, "worktree", "add", "--detach", first, "HEAD")
	// A manually created venv is sufficient; uv's lock need not exist.
	for _, root := range []string{main, linked, first, last} {
		if err := os.Remove(filepath.Join(root, "api", "uv.lock")); err != nil {
			t.Fatal(err)
		}
	}
	mainPython := writeTestVirtualenv(t, filepath.Join(main, "api"), "venv")
	firstPython := writeTestVirtualenv(t, filepath.Join(first, "api"), ".venv")
	lastPython := writeTestVirtualenv(t, filepath.Join(last, "api"), ".venv")
	project := filepath.Join(linked, "api")
	check := func(want string) {
		t.Helper()
		if got := discoverPythonInterpreterInLinkedWorktrees(t.Context(), project); got != want {
			t.Fatalf("interpreter = %q, want %q", got, want)
		}
	}
	writeTestVirtualenv(t, filepath.Join(main, "api"), ".venv")
	runPythonEnvironmentGit(t, main, "add", "--", "api/.venv/pyvenv.cfg")
	check(mainPython)
	writePythonEnvironmentFixture(t, filepath.Join(main, "api", "requirements.txt"), "sample==1.0\n", 0600)
	check(firstPython)
	writePythonEnvironmentFixture(t, filepath.Join(first, "api", ".python-version"), "3.12\n", 0600)
	check(lastPython)
}

func pythonEnvironmentWorktrees(t *testing.T) (main, linked string) {
	t.Helper()
	main = normalizeWaiterPath(testgit.NewRepository(t, map[string]string{
		"api/pyproject.toml": "[project]\nname = 'sample'\nversion = '0.1.0'\n",
		"api/uv.lock":        "version = 1\n",
	}))
	linked = filepath.Join(resolvedTempDir(t), "linked")
	runPythonEnvironmentGit(t, main, "worktree", "add", "--detach", linked, "HEAD")
	return main, linked
}

func runPythonEnvironmentGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.excludesFile="}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeTestVirtualenv(t *testing.T, project, name string) string {
	t.Helper()
	venv := filepath.Join(project, name)
	python := filepath.Join(venv, "bin", "python")
	if runtime.GOOS == "windows" {
		python = filepath.Join(venv, "Scripts", "python.exe")
	}
	writePythonEnvironmentFixture(t, python, "", 0700)
	writePythonEnvironmentFixture(t, filepath.Join(venv, "pyvenv.cfg"), "include-system-site-packages = false\n", 0600)
	return python
}

func writePythonEnvironmentFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
