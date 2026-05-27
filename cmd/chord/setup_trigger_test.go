package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
)

func TestRunRootTriggersWizardOnlyForRootMissingConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	origWizard := runInitialSetupWizardFunc
	origInitApp := initAppRunner
	defer func() {
		runInitialSetupWizardFunc = origWizard
		initAppRunner = origInitApp
	}()

	var wizardCalls int
	runInitialSetupWizardFunc = func(context.Context, SetupWizardOptions) error {
		wizardCalls++
		return nil
	}
	initAppRunner = func(bool, string, sessionStartupOptions) (*AppContext, error) {
		return nil, fmt.Errorf("stop after wizard")
	}

	root := newRootCmd()
	if err := runRoot(root, nil); err == nil || err.Error() != "stop after wizard" {
		t.Fatalf("runRoot(root) err = %v, want stop after wizard", err)
	}
	if wizardCalls != 1 {
		t.Fatalf("wizardCalls = %d, want 1", wizardCalls)
	}

	wizardCalls = 0
	child := &cobra.Command{Use: "doctor"}
	root.AddCommand(child)
	if err := runRoot(child, nil); err == nil || err.Error() != "stop after wizard" {
		t.Fatalf("runRoot(child) err = %v, want stop after wizard", err)
	}
	if wizardCalls != 0 {
		t.Fatalf("wizardCalls on child = %d, want 0", wizardCalls)
	}
}

func TestRootCommandVariantsTriggerWizardBeforeInitApp(t *testing.T) {
	for _, args := range [][]string{{"--continue"}, {"--resume", "sid-123"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", configHome)

			origWizard := runInitialSetupWizardFunc
			origInitApp := initAppRunner
			defer func() {
				runInitialSetupWizardFunc = origWizard
				initAppRunner = origInitApp
				flagContinueSession = false
				flagResumeSession = ""
				flagWorktree = ""
				flagWorktreeStartupInfo = nil
				flagWorktreeStartupMeta = nil
			}()

			wizardCalls := 0
			runInitialSetupWizardFunc = func(context.Context, SetupWizardOptions) error {
				wizardCalls++
				if err := os.MkdirAll(configHome, 0o700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte("providers: {}\nmodel_pools: {}\n"), 0o644)
			}
			initAppRunner = func(bool, string, sessionStartupOptions) (*AppContext, error) {
				return nil, errors.New("stop")
			}

			cmd := newRootCmd()
			cmd.SetArgs(args)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			err := cmd.ExecuteContext(t.Context())
			if err == nil || err.Error() != "stop" {
				t.Fatalf("ExecuteContext(%v) err = %v, want stop", args, err)
			}
			if wizardCalls != 1 {
				t.Fatalf("wizardCalls = %d, want 1", wizardCalls)
			}
		})
	}
}

func TestRunRootWorktreeFlagTriggersWizardBeforeWorktreePreparation(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	origWizard := runInitialSetupWizardFunc
	origInitApp := initAppRunner
	defer func() {
		runInitialSetupWizardFunc = origWizard
		initAppRunner = origInitApp
		flagWorktree = ""
		flagWorktreeStartupInfo = nil
		flagWorktreeStartupMeta = nil
	}()

	wizardCalls := 0
	runInitialSetupWizardFunc = func(context.Context, SetupWizardOptions) error {
		wizardCalls++
		return errors.New("wizard-stop")
	}
	initAppRunner = func(bool, string, sessionStartupOptions) (*AppContext, error) {
		return nil, errors.New("initApp should not run")
	}

	cmd := newRootCmd()
	if err := cmd.Flags().Set("worktree", "feat-auth"); err != nil {
		t.Fatalf("set worktree flag: %v", err)
	}
	flagWorktree = "feat-auth"
	if err := runRoot(cmd, nil); err == nil || err.Error() != "wizard-stop" {
		t.Fatalf("runRoot err = %v, want wizard-stop", err)
	}
	if wizardCalls != 1 {
		t.Fatalf("wizardCalls = %d, want 1", wizardCalls)
	}
}

func TestRunRootSkipsWizardWhenConfigExists(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.MkdirAll(configHome, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte("providers: {}\nmodel_pools: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	origWizard := runInitialSetupWizardFunc
	origInitApp := initAppRunner
	defer func() {
		runInitialSetupWizardFunc = origWizard
		initAppRunner = origInitApp
	}()

	called := false
	runInitialSetupWizardFunc = func(context.Context, SetupWizardOptions) error {
		called = true
		return nil
	}
	initAppRunner = func(bool, string, sessionStartupOptions) (*AppContext, error) {
		return nil, errors.New("stop")
	}

	if err := runRoot(newRootCmd(), nil); err == nil || err.Error() != "stop" {
		t.Fatalf("runRoot err = %v, want stop", err)
	}
	if called {
		t.Fatal("expected wizard to be skipped when config exists")
	}
}

func TestRootCommandHelpAndVersionDoNotTriggerWizard(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", configHome)

			origWizard := runInitialSetupWizardFunc
			defer func() { runInitialSetupWizardFunc = origWizard }()

			wizardCalls := 0
			runInitialSetupWizardFunc = func(context.Context, SetupWizardOptions) error {
				wizardCalls++
				return nil
			}

			cmd := newRootCmd()
			cmd.SetArgs(args)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			if err := cmd.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("ExecuteContext(%v): %v", args, err)
			}
			if wizardCalls != 0 {
				t.Fatalf("wizardCalls = %d, want 0", wizardCalls)
			}
		})
	}
}

func TestSubcommandsDoNotTriggerWizard(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	origWizard := runInitialSetupWizardFunc
	defer func() { runInitialSetupWizardFunc = origWizard }()

	wizardCalls := 0
	runInitialSetupWizardFunc = func(context.Context, SetupWizardOptions) error {
		wizardCalls++
		return nil
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "auth", args: []string{"auth", "codex"}, want: initialSetupRequiredMessage},
		{name: "doctor", args: []string{"doctor", "models"}, want: initialSetupRequiredMessage},
		{name: "headless", args: []string{"headless"}, want: initialSetupRequiredMessage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newRootCmd()
			cmd.SetArgs(tt.args)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			err := cmd.ExecuteContext(t.Context())
			if err == nil || err.Error() != tt.want {
				t.Fatalf("ExecuteContext(%v) err = %v, want %q", tt.args, err, tt.want)
			}
		})
	}
	if wizardCalls != 0 {
		t.Fatalf("wizardCalls = %d, want 0", wizardCalls)
	}
}

func TestWorktreeSubcommandDoesNotTriggerWizard(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	origWizard := runInitialSetupWizardFunc
	defer func() { runInitialSetupWizardFunc = origWizard }()

	wizardCalls := 0
	runInitialSetupWizardFunc = func(context.Context, SetupWizardOptions) error {
		wizardCalls++
		return nil
	}

	repo := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(cwd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir(repo): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: /nonexistent\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.git): %v", err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"worktree", "feat-auth"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.ExecuteContext(t.Context())
	if err == nil {
		t.Fatal("expected worktree subcommand to fail without triggering wizard")
	}
	if wizardCalls != 0 {
		t.Fatalf("wizardCalls = %d, want 0", wizardCalls)
	}
}

func TestRootPersistentPreRunSetsConfigHome(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", "")

	cmd := newRootCmd()
	if err := cmd.PersistentFlags().Set("config-home", configHome); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if cmd.PersistentPreRunE == nil {
		t.Fatal("expected PersistentPreRunE")
	}
	if err := cmd.PersistentPreRunE(cmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if path != filepath.Join(configHome, "config.yaml") {
		t.Fatalf("ConfigPath = %q, want %q", path, filepath.Join(configHome, "config.yaml"))
	}
	auth, err := config.AuthPath()
	if err != nil {
		t.Fatalf("AuthPath: %v", err)
	}
	if auth != filepath.Join(configHome, "auth.yaml") {
		t.Fatalf("AuthPath = %q, want %q", auth, filepath.Join(configHome, "auth.yaml"))
	}
}

func TestRunRootDoesNotTriggerWizardWhenConfigIsMalformed(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.MkdirAll(configHome, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte("providers: [\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	origWizard := runInitialSetupWizardFunc
	defer func() { runInitialSetupWizardFunc = origWizard }()

	called := false
	runInitialSetupWizardFunc = func(context.Context, SetupWizardOptions) error {
		called = true
		return nil
	}

	err := runRoot(newRootCmd(), nil)
	if err == nil {
		t.Fatal("expected malformed config to fail")
	}
	if called {
		t.Fatal("expected wizard to be skipped for malformed config")
	}
	if err.Error() == initialSetupRequiredMessage {
		t.Fatalf("expected malformed config error, got initial setup error: %v", err)
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Fatalf("expected yaml error, got %v", err)
	}
}
