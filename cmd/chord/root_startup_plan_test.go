package main

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/spf13/cobra"
)

func TestCommandScope(t *testing.T) {
	cases := map[string]string{
		"project-md":   "project",
		"project-yaml": "project",
		"global-md":    "global",
		"global-yaml":  "global",
		"":             "",
		"unknown":      "",
	}
	for source, want := range cases {
		if got := commandScope(source); got != want {
			t.Fatalf("commandScope(%q) = %q, want %q", source, got, want)
		}
	}
}

func TestPlanRootStartupMutualExclusion(t *testing.T) {
	_, err := planRootStartup(newRootCmd(), true, "sid-123", "")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("planRootStartup err = %v, want mutual exclusion", err)
	}
}

func TestPlanRootStartupSessionOptionsAndPprof(t *testing.T) {
	t.Setenv("CHORD_PPROF_PORT", "6060")
	cmd := newRootCmd()
	plan, err := planRootStartup(cmd, true, "", "")
	if err != nil {
		t.Fatalf("planRootStartup: %v", err)
	}
	if !plan.RunSetupWizard {
		t.Fatal("RunSetupWizard = false, want true for root command")
	}
	if plan.PprofListenAddr != "127.0.0.1:6060" {
		t.Fatalf("PprofListenAddr = %q", plan.PprofListenAddr)
	}
	if !plan.SessionOptions.ContinueLatest || plan.SessionOptions.ResumeID != "" {
		t.Fatalf("SessionOptions = %+v", plan.SessionOptions)
	}
}

func TestPlanRootStartupWorktreeChanged(t *testing.T) {
	cmd := &cobra.Command{Use: "chord"}
	cmd.Flags().String("worktree", "", "")
	if err := cmd.Flags().Set("worktree", "feat-auth"); err != nil {
		t.Fatalf("set worktree: %v", err)
	}
	prevInfo := flagWorktreeStartupInfo
	flagWorktreeStartupInfo = nil
	defer func() { flagWorktreeStartupInfo = prevInfo }()

	plan, err := planRootStartup(cmd, false, " sid-123 ", "feat-auth")
	if err != nil {
		t.Fatalf("planRootStartup: %v", err)
	}
	if !plan.PrepareWorktree || plan.WorktreeName != "feat-auth" {
		t.Fatalf("worktree plan = %+v", plan)
	}
	if plan.SessionOptions.ResumeID != "sid-123" {
		t.Fatalf("resume plan = %+v", plan)
	}
}

func TestPlanRootStartupInvalidPprofKeepsSessionOptions(t *testing.T) {
	t.Setenv("CHORD_PPROF_PORT", "not-a-port")
	cmd := newRootCmd()
	plan, err := planRootStartup(cmd, false, " sid-abc ", "")
	if err == nil || !errors.Is(err, ErrInvalidPprofPort) {
		t.Fatalf("planRootStartup err = %v, want wrapped ErrInvalidPprofPort", err)
	}
	if plan.PprofListenAddr != "" {
		t.Fatalf("PprofListenAddr = %q, want empty when port invalid", plan.PprofListenAddr)
	}
	if plan.SessionOptions.ResumeID != "sid-abc" {
		t.Fatalf("SessionOptions.ResumeID = %q, want sid-abc preserved even on pprof error", plan.SessionOptions.ResumeID)
	}
	if !plan.RunSetupWizard {
		t.Fatal("RunSetupWizard should remain true when pprof port is invalid")
	}
}

type fakeTUIRunner struct {
	runCalled  bool
	quitCalled bool
	runErr     error
}

func (r *fakeTUIRunner) Run() (tea.Model, error) {
	r.runCalled = true
	return nil, r.runErr
}

func (r *fakeTUIRunner) Quit() { r.quitCalled = true }

func TestTUIProgramFactoryFallbackSizeAndRunError(t *testing.T) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer stdin.Close()
	stdout, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer stdout.Close()
	runner := &fakeTUIRunner{runErr: errors.New("boom")}
	factory := tuiProgramFactory{
		stdin:      stdin,
		stdout:     stdout,
		isTerminal: func(uintptr) bool { return false },
		openTTY: func() (*os.File, *os.File, error) {
			return stdin, stdout, nil
		},
		newProgram: func(tea.Model, ...tea.ProgramOption) tuiProgramRunner { return runner },
	}
	plan, err := factory.build(&AppContext{})
	if err != nil {
		t.Fatalf("factory.build: %v", err)
	}
	if plan.initialWidth != 80 || plan.initialHeight != 24 {
		t.Fatalf("size = %dx%d, want 80x24", plan.initialWidth, plan.initialHeight)
	}
	if _, err := plan.runner.Run(); err == nil || err.Error() != "boom" {
		t.Fatalf("Run err = %v, want boom", err)
	}
	if !runner.runCalled {
		t.Fatal("runner Run not called")
	}
}

func TestTUIProgramFactoryUsesTTYSize(t *testing.T) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer stdin.Close()
	stdout, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer stdout.Close()
	factory := tuiProgramFactory{
		stdin:      stdin,
		stdout:     stdout,
		isTerminal: func(uintptr) bool { return true },
		getSize:    func(uintptr) (int, int, error) { return 120, 40, nil },
		newProgram: func(tea.Model, ...tea.ProgramOption) tuiProgramRunner { return &fakeTUIRunner{} },
	}
	plan, err := factory.build(&AppContext{})
	if err != nil {
		t.Fatalf("factory.build: %v", err)
	}
	if plan.initialWidth != 120 || plan.initialHeight != 40 {
		t.Fatalf("size = %dx%d, want 120x40", plan.initialWidth, plan.initialHeight)
	}
}

type optionProbeModel struct{}

func (optionProbeModel) Init() tea.Cmd { return nil }

func (optionProbeModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return optionProbeModel{}, nil }

func (optionProbeModel) View() tea.View { return tea.NewView("") }

func programDisableScrollRegionOptim(t *testing.T, p *tea.Program) bool {
	t.Helper()
	field := reflect.ValueOf(p).Elem().FieldByName("disableScrollRegionOptim")
	if !field.IsValid() || field.Kind() != reflect.Bool {
		t.Fatal("tea.Program no longer exposes a disableScrollRegionOptim bool field")
	}
	return field.Bool()
}

func TestTUIProgramFactoryDisablesScrollRegionOptimization(t *testing.T) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer stdin.Close()
	stdout, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer stdout.Close()
	var captured []tea.ProgramOption
	factory := tuiProgramFactory{
		stdin:      stdin,
		stdout:     stdout,
		isTerminal: func(uintptr) bool { return false },
		openTTY: func() (*os.File, *os.File, error) {
			return stdin, stdout, nil
		},
		newProgram: func(_ tea.Model, opts ...tea.ProgramOption) tuiProgramRunner {
			captured = append([]tea.ProgramOption(nil), opts...)
			return &fakeTUIRunner{}
		},
	}
	if _, err := factory.build(&AppContext{}); err != nil {
		t.Fatalf("factory.build: %v", err)
	}

	if programDisableScrollRegionOptim(t, tea.NewProgram(optionProbeModel{})) {
		t.Fatal("default Bubble Tea program unexpectedly disables scroll-region optimization")
	}
	if !programDisableScrollRegionOptim(t, tea.NewProgram(optionProbeModel{}, captured...)) {
		t.Fatal("factory options did not include WithoutScrollRegionOptimization")
	}
}

func TestTUIProgramFactoryOpenTTYError(t *testing.T) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer stdin.Close()
	stdout, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer stdout.Close()
	factory := tuiProgramFactory{
		stdin:      stdin,
		stdout:     stdout,
		isTerminal: func(fd uintptr) bool { return fd == stdout.Fd() },
		openTTY:    func() (*os.File, *os.File, error) { return nil, nil, errors.New("no tty") },
	}
	_, err = factory.build(&AppContext{})
	if err == nil || !strings.Contains(err.Error(), "requires a terminal") {
		t.Fatalf("factory.build err = %v, want terminal error", err)
	}
}

func TestAPIBaseEnvFallback(t *testing.T) {
	defer func(prev string) { flagAPIBase = prev }(flagAPIBase)

	t.Setenv("CHORD_API_BASE", "https://example.invalid/v1")
	flagAPIBase = ""
	cmd := newRootCmd()
	if err := cmd.PersistentPreRunE(cmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE err = %v", err)
	}
	if flagAPIBase != "https://example.invalid/v1" {
		t.Fatalf("flagAPIBase = %q, want env fallback value", flagAPIBase)
	}

	// The CLI flag wins over the environment variable.
	cmd = newRootCmd()
	if err := cmd.PersistentFlags().Set("api-base", "https://flag.example.invalid/v1"); err != nil {
		t.Fatalf("set api-base flag: %v", err)
	}
	if err := cmd.PersistentPreRunE(cmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE err = %v", err)
	}
	if flagAPIBase != "https://flag.example.invalid/v1" {
		t.Fatalf("flagAPIBase = %q, want flag value to win", flagAPIBase)
	}
}
