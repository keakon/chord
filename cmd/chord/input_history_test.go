package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/inputhistory"
	"github.com/keakon/chord/internal/tui"
)

func TestInputHistoryFactorySharedCleanup(t *testing.T) {
	dir := t.TempDir()
	var running *tui.Model
	factory := tuiProgramFactory{isTerminal: func(uintptr) bool { return false }, openTTY: func() (*os.File, *os.File, error) { return os.Stdin, os.Stdout, nil }, newProgram: func(model tea.Model, _ ...tea.ProgramOption) tuiProgramRunner {
		running = model.(*tui.Model)
		return &fakeTUIRunner{}
	}}
	plan, err := factory.build(&AppContext{Cfg: config.DefaultConfig(), PathLocator: &config.PathLocator{StateDir: dir}, ProjectLocator: &config.ProjectLocator{ProjectKey: "project", CanonicalRoot: "/sample"}})
	if err != nil {
		t.Fatal(err)
	}
	running.Update(tea.PasteMsg{Content: "original input"})
	running.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	cleanup := tuiCleanup(&plan.model, io.Discard)
	cleanup()
	cleanup()
	store := inputhistory.New(dir, inputhistory.DefaultOptions())
	defer store.Close(context.Background())
	snap, err := store.Snapshot(context.Background(), true)
	if err != nil || len(snap.Entries) != 1 || snap.Entries[0].Record.ProjectKey != "project" || snap.Entries[0].Record.TextParts[0].Text != "original input" {
		t.Fatal(snap, err)
	}
}

func TestInputHistoryCleanupAfterStartupAndRunFailure(t *testing.T) {
	if dir := os.Getenv("CHORD_HISTORY_CLEANUP_HELPER"); dir != "" {
		store := inputhistory.New(dir, inputhistory.DefaultOptions())
		defer store.Close(context.Background())
		snapshot, err := store.Snapshot(context.Background(), true)
		if err != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].Record.TextParts[0].Text != "sample input" {
			t.Fatal(snapshot, err)
		}
		return
	}
	for _, failure := range []string{"runtime", "runner"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			var running *tui.Model
			runner := &fakeTUIRunner{runErr: errors.New("sample runner error")}
			factory := tuiProgramFactory{isTerminal: func(uintptr) bool { return false }, openTTY: func() (*os.File, *os.File, error) { return os.Stdin, os.Stdout, nil }, newProgram: func(model tea.Model, _ ...tea.ProgramOption) tuiProgramRunner {
				running = model.(*tui.Model)
				return runner
			}}
			ac := &AppContext{Cfg: config.DefaultConfig(), PathLocator: &config.PathLocator{StateDir: dir}, ProjectLocator: &config.ProjectLocator{ProjectKey: "project", CanonicalRoot: "/sample"}}
			func() {
				plan, err := factory.build(ac)
				if err != nil {
					t.Fatal(err)
				}
				defer tuiCleanup(&plan.model, io.Discard)()
				running.Update(tea.PasteMsg{Content: "sample input"})
				running.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
				if failure == "runtime" {
					if _, err = createRuntime(ac); err == nil {
						t.Fatal("expected startup failure")
					}
					return
				}
				if _, err = plan.runner.Run(); err == nil {
					t.Fatal("expected runner failure")
				}
			}()
			child := exec.Command(os.Args[0], "-test.run=^TestInputHistoryCleanupAfterStartupAndRunFailure$")
			child.Env = append(os.Environ(), "CHORD_HISTORY_CLEANUP_HELPER="+dir)
			if out, err := child.CombinedOutput(); err != nil {
				t.Fatalf("reader process: %v %s", err, out)
			}
		})
	}
}
