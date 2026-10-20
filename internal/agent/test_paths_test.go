package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/externalsupervisor"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

// TestMain protects fallback callers that do not receive a PathLocator from
// reading or writing the user's Chord directories. Full agent fixtures use
// explicit per-project locators so independent tests can run concurrently.
//
// Before any of that, it lets this test binary serve as the external worker's
// lifecycle supervisor: a supervised worker re-executes the running
// executable, which under `go test` is this binary. Intercepting ahead of the
// testing framework keeps that path identical to production — no test-only
// entrypoint and no override hook in the code under test.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == externalsupervisor.CommandName {
		os.Exit(externalsupervisor.Run(os.Args[2:]))
	}
	root, err := os.MkdirTemp("", "chord-agent-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	paths := newAgentTestPathLocator(root)
	for _, setting := range []struct{ name, value string }{
		{"CHORD_CONFIG_HOME", paths.ConfigHome},
		{"CHORD_STATE_DIR", paths.StateDir},
		{"CHORD_CACHE_DIR", paths.CacheDir},
		{"CHORD_SESSIONS_DIR", paths.SessionsRoot},
		{"CHORD_LOGS_DIR", paths.LogsDir},
	} {
		if err := os.Setenv(setting.name, setting.value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			_ = os.RemoveAll(root)
			os.Exit(1)
		}
	}
	code := m.Run()
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

func newAgentTestPathLocator(projectRoot string) *config.PathLocator {
	root := filepath.Join(projectRoot, ".test-chord")
	stateDir := filepath.Join(root, "state")
	return &config.PathLocator{
		ConfigHome:   filepath.Join(root, "config"),
		StateDir:     stateDir,
		CacheDir:     filepath.Join(root, "cache"),
		SessionsRoot: filepath.Join(root, "sessions"),
		LogsDir:      filepath.Join(root, "logs"),
		ExportsDir:   filepath.Join(stateDir, "exports"),
	}
}

func TestMainAgentFixturesKeepParallelSessionPathsIndependent(t *testing.T) {
	fallback, err := config.DefaultPathLocator()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main", "restore"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			var a *MainAgent
			if name == "restore" {
				a = newTestMainAgentForRestore(t, root, testProjectSessionDir(t, root, "sample-session"))
			} else {
				a = newTestMainAgent(t, root)
			}
			sessionsDir, err := a.projectSessionsDir()
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{sessionsDir, a.memoryMgr.Layout().StateDir, a.pathLocator.CacheDir, a.pathLocator.ConfigHome} {
				rel, err := filepath.Rel(root, path)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					t.Fatalf("fixture path %q escaped project %q: %v", path, root, err)
				}
			}
			content := "request " + name
			manager := a.recoveryManager()
			if err := manager.PersistMessageDurable(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: content}); err != nil {
				t.Fatal(err)
			}
			messages, err := manager.LoadMessages(identity.MainAgentID)
			if err != nil || len(messages) != 1 || messages[0].Content != content {
				t.Fatalf("isolated messages = %#v, error = %v", messages, err)
			}
			current, err := config.DefaultPathLocator()
			if err != nil || *current != *fallback {
				t.Fatalf("fixture changed process fallback paths: %v", err)
			}
		})
	}
}
