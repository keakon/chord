package main

import (
	"fmt"
	"os"

	"github.com/charmbracelet/x/term"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tui"
)

func commandScope(source string) string {
	switch source {
	case "project-md", "project-yaml":
		return "project"
	case "global-md", "global-yaml":
		return "global"
	default:
		return ""
	}
}

type tuiProgramRunner interface {
	Run() (tea.Model, error)
	Quit()
}

type tuiProgramFactory struct {
	stdin      *os.File
	stdout     *os.File
	isTerminal func(uintptr) bool
	getSize    func(uintptr) (int, int, error)
	openTTY    func() (*os.File, *os.File, error)
	newProgram func(tea.Model, ...tea.ProgramOption) tuiProgramRunner
}

type tuiProgramPlan struct {
	model         tui.Model
	runner        tuiProgramRunner
	initialWidth  int
	initialHeight int
}

func defaultTUIProgramFactory() tuiProgramFactory {
	return tuiProgramFactory{
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		isTerminal: term.IsTerminal,
		getSize:    term.GetSize,
		openTTY:    tea.OpenTTY,
		newProgram: func(model tea.Model, opts ...tea.ProgramOption) tuiProgramRunner {
			return tea.NewProgram(model, opts...)
		},
	}
}

func (f tuiProgramFactory) build(ac *AppContext) (tuiProgramPlan, error) {
	if f.stdin == nil {
		f.stdin = os.Stdin
	}
	if f.stdout == nil {
		f.stdout = os.Stdout
	}
	if f.isTerminal == nil {
		f.isTerminal = term.IsTerminal
	}
	if f.getSize == nil {
		f.getSize = term.GetSize
	}
	if f.openTTY == nil {
		f.openTTY = tea.OpenTTY
	}
	if f.newProgram == nil {
		f.newProgram = defaultTUIProgramFactory().newProgram
	}

	var opts []tea.ProgramOption
	terminalOut := f.stdout
	if !f.isTerminal(f.stdin.Fd()) {
		ttyIn, ttyOut, err := f.openTTY()
		if err != nil {
			return tuiProgramPlan{}, fmt.Errorf("chord requires a terminal (TTY): %w", err)
		}
		terminalOut = ttyOut
		opts = append(opts, tea.WithInput(ttyIn))
	}
	if f.isTerminal(terminalOut.Fd()) {
		opts = append(opts, tea.WithOutput(tui.WrapTerminalImageOutput(terminalOut)))
	}

	initialWidth, initialHeight := 80, 24
	if f.isTerminal(terminalOut.Fd()) {
		if w, h, err := f.getSize(terminalOut.Fd()); err == nil && w > 0 && h > 0 {
			initialWidth, initialHeight = w, h
		}
	}

	model := tui.NewModelWithSize(nil, initialWidth, initialHeight)
	if ac != nil && ac.MainAgent != nil {
		model = tui.NewModelWithSize(ac.MainAgent, initialWidth, initialHeight)
	}
	tuiModel := model
	tuiModel.ApplyCadenceProfileFromEnv()
	if ac != nil {
		tuiModel.SetInstanceID(ac.InstanceID)
	}
	if ac != nil && ac.Cfg != nil {
		if len(ac.Cfg.KeyMap) > 0 {
			tuiModel.SetKeyMap(tui.KeyMapFromConfig(ac.Cfg.KeyMap))
		}
		if ac.Cfg.IMESwitchTarget != "" {
			tuiModel.SetIMESwitchTarget(ac.Cfg.IMESwitchTarget)
		}
		tui.SetSingleLineDiffColumnsLimit(ac.Cfg.Diff.InlineMaxColumns)
		if len(ac.LoadedCommands) > 0 {
			tuiCmds := make([]tui.CustomCommand, len(ac.LoadedCommands))
			for i, d := range ac.LoadedCommands {
				tuiCmds[i] = tui.CustomCommand{Cmd: "/" + d.Name, Desc: d.Description, Scope: commandScope(d.Source)}
			}
			tuiModel.SetCustomCommands(tuiCmds)
		}
	}
	if f.isTerminal(terminalOut.Fd()) {
		osc9 := false
		if ac != nil && ac.Cfg != nil && ac.Cfg.DesktopNotification != nil {
			osc9 = *ac.Cfg.DesktopNotification
		}
		tuiModel.SetDesktopNotification(osc9, terminalOut)
	}

	opts = append(opts, tea.WithWindowSize(initialWidth, initialHeight))
	// Chord renders streaming transcript updates near sticky separators and side
	// panels. Terminal hard-scroll optimizations can leave stale rows in that
	// layout across terminal hosts, so disable them globally and let the renderer
	// redraw changed lines instead.
	opts = append(opts, tea.WithoutScrollOptimization())
	return tuiProgramPlan{model: tuiModel, runner: f.newProgram(&tuiModel, opts...), initialWidth: initialWidth, initialHeight: initialHeight}, nil
}
