package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/jsonx"
)

const (
	doctorAgentCommandFound   = "found"
	doctorAgentCommandMissing = "missing"
)

type doctorAgentsOptions struct {
	Out  io.Writer
	JSON bool
}

type doctorAgentEntry struct {
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	Backend       string `json:"backend,omitempty"`
	Command       string `json:"command,omitempty"`
	CommandStatus string `json:"command_status,omitempty"`
	CommandPath   string `json:"command_path,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

type doctorAgentsReport struct {
	// LoadError is set when the definitions failed to load; Entries is then
	// empty because the loader returns no partial set.
	LoadError string              `json:"load_error,omitempty"`
	Entries   []doctorAgentEntry  `json:"entries"`
	Summary   doctorAgentsSummary `json:"summary"`
}

type doctorAgentsSummary struct {
	Total          int `json:"total"`
	External       int `json:"external"`
	CommandMissing int `json:"command_missing"`
}

func newDoctorAgentsCmd() *cobra.Command {
	opts := doctorAgentsOptions{}
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Diagnose agent definitions and external backend commands",
		Long: `Check that agent definitions load and that external backend commands resolve.

Agent definitions with a backend block run on an external coding-agent CLI, so a
missing command fails every delegation for that role. Launch argument and
environment validation already run when the definitions load; this command adds
the command-availability check the loader cannot do, and lists the definitions
it found.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.Out = cmd.OutOrStdout()
			return runDoctorAgents(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Write a JSON report")
	return cmd
}

func runDoctorAgents(opts doctorAgentsOptions) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	cwd, err := os.Getwd()
	if err != nil {
		return cliExitError{code: 2, err: fmt.Errorf("get working directory: %w", err)}
	}
	// Agent definitions come from the content root, the same root the main
	// program loads them from (the main worktree inside a linked worktree).
	root := resolveContentRoot(context.Background(), cwd)
	configHome, _ := config.ConfigHomeDir()
	agentConfigs, loadErr := config.ResolveAgentConfigs(
		filepath.Join(root, ".chord", "agents"),
		filepath.Join(configHome, "agents"),
	)

	report := doctorAgentsReport{Entries: []doctorAgentEntry{}}
	if loadErr != nil {
		report.LoadError = loadErr.Error()
	}
	for _, def := range agentConfigs {
		if def == nil {
			continue
		}
		entry := doctorAgentEntry{Name: def.Name, Mode: doctorAgentModeLabel(def.Mode)}
		if def.Backend != nil {
			entry.Backend = def.Backend.Kind
			entry.Command = def.Backend.EffectiveCommand()
			path, lookErr := exec.LookPath(entry.Command)
			switch {
			case lookErr == nil:
				entry.CommandStatus = doctorAgentCommandFound
				entry.CommandPath = path
			default:
				entry.CommandStatus = doctorAgentCommandMissing
				entry.Detail = fmt.Sprintf("command %q is not in PATH; delegation for this role will fail until the CLI is installed and signed in", entry.Command)
			}
		}
		report.Entries = append(report.Entries, entry)
	}
	sort.Slice(report.Entries, func(i, j int) bool { return report.Entries[i].Name < report.Entries[j].Name })
	for _, entry := range report.Entries {
		report.Summary.Total++
		if entry.Backend != "" {
			report.Summary.External++
		}
		if entry.CommandStatus == doctorAgentCommandMissing {
			report.Summary.CommandMissing++
		}
	}

	if opts.JSON {
		if err := jsonx.EncodeIndent(out, report, "", "  "); err != nil {
			return cliExitError{code: 2, err: fmt.Errorf("write JSON report: %w", err)}
		}
	} else {
		renderDoctorAgentsText(out, report)
	}

	if loadErr != nil {
		return cliExitError{code: 2, err: fmt.Errorf("load agent definitions: %w", loadErr)}
	}
	if report.Summary.CommandMissing > 0 {
		return cliExitError{code: 1, err: fmt.Errorf("%d external backend command(s) not found", report.Summary.CommandMissing)}
	}
	return nil
}

// doctorAgentModeLabel names the runtime a definition runs on: subagent roles
// can carry an external backend, the main role never can.
func doctorAgentModeLabel(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return "main"
	}
	return mode
}

func renderDoctorAgentsText(out io.Writer, report doctorAgentsReport) {
	if report.LoadError != "" {
		fmt.Fprintf(out, "load: FAILED\n  %s\n", report.LoadError)
		return
	}
	for _, entry := range report.Entries {
		fmt.Fprintf(out, "%s (mode: %s)\n", entry.Name, entry.Mode)
		if entry.Backend == "" {
			fmt.Fprintf(out, "  backend: internal\n")
			continue
		}
		fmt.Fprintf(out, "  backend: %s\n", entry.Backend)
		switch entry.CommandStatus {
		case doctorAgentCommandFound:
			fmt.Fprintf(out, "  command: %s (found: %s)\n", entry.Command, entry.CommandPath)
		case doctorAgentCommandMissing:
			fmt.Fprintf(out, "  command: %s (missing from PATH)\n", entry.Command)
		}
		if entry.Detail != "" {
			fmt.Fprintf(out, "  detail: %s\n", strings.TrimSpace(entry.Detail))
		}
	}
	if report.Summary.Total == 0 {
		fmt.Fprintln(out, "no agent definitions found")
		return
	}
	fmt.Fprintf(out, "\n%d agent definition(s), %d external, %d missing command(s)\n",
		report.Summary.Total, report.Summary.External, report.Summary.CommandMissing)
}
