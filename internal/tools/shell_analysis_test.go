package tools

import (
	"reflect"
	"testing"
)

func TestAnalyzeShellCommandExtractsCompoundSubcommands(t *testing.T) {
	analysis, err := AnalyzeShellCommand("echo foo && rm bar")
	if err != nil {
		t.Fatalf("AnalyzeShellCommand: %v", err)
	}
	got := make([]string, 0, len(analysis.Subcommands))
	for _, sub := range analysis.Subcommands {
		got = append(got, sub.Source)
	}
	want := []string{"echo foo", "rm bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subcommands = %#v, want %#v", got, want)
	}
}

func TestAnalyzeShellCommandExtractsNestedCommandSubstitution(t *testing.T) {
	analysis, err := AnalyzeShellCommand(`echo "sha=$(git rev-parse HEAD)" && pwd`)
	if err != nil {
		t.Fatalf("AnalyzeShellCommand: %v", err)
	}
	got := make([]string, 0, len(analysis.Subcommands))
	for _, sub := range analysis.Subcommands {
		got = append(got, sub.Source)
	}
	want := []string{`echo "sha=$(git rev-parse HEAD)"`, "git rev-parse HEAD", "pwd"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subcommands = %#v, want %#v", got, want)
	}
}

func TestAnalyzeShellCommandExtractsFunctionBodies(t *testing.T) {
	// A function body executes when the same command string calls the function,
	// so its commands must reach permission matching: a narrow allow rule that
	// matches the call site must not silently cover an unreviewed body.
	analysis, err := AnalyzeShellCommand("cleanup() { rm bar; }\npwd")
	if err != nil {
		t.Fatalf("AnalyzeShellCommand: %v", err)
	}
	got := make([]string, 0, len(analysis.Subcommands))
	for _, sub := range analysis.Subcommands {
		got = append(got, sub.Source)
	}
	want := []string{"rm bar", "pwd"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subcommands = %#v, want %#v", got, want)
	}
}

func TestAnalyzeShellCommandKeepsPathAssignmentInSubcommandSource(t *testing.T) {
	// PATH chooses which binary the command name runs, so a narrow rule that
	// matches only the command name must not cover it. Other assignments only
	// set an environment value and stay outside the matched source.
	analysis, err := AnalyzeShellCommand("PATH=/tmp/evil git status")
	if err != nil {
		t.Fatalf("AnalyzeShellCommand: %v", err)
	}
	got := make([]string, 0, len(analysis.Subcommands))
	for _, sub := range analysis.Subcommands {
		got = append(got, sub.Source)
	}
	want := []string{"PATH=/tmp/evil git status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subcommands = %#v, want %#v", got, want)
	}

	stripAnalysis, err := AnalyzeShellCommand("FOO=bar git status")
	if err != nil {
		t.Fatalf("AnalyzeShellCommand: %v", err)
	}
	if got := stripAnalysis.Subcommands[0].Source; got != "git status" {
		t.Fatalf("subcommand source = %q, want environment assignment stripped", got)
	}
}
