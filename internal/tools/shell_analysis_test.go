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

func TestAnalyzeShellCommandSkipsFunctionBodies(t *testing.T) {
	analysis, err := AnalyzeShellCommand("cleanup() { rm bar; }\npwd")
	if err != nil {
		t.Fatalf("AnalyzeShellCommand: %v", err)
	}
	got := make([]string, 0, len(analysis.Subcommands))
	for _, sub := range analysis.Subcommands {
		got = append(got, sub.Source)
	}
	want := []string{"pwd"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subcommands = %#v, want %#v", got, want)
	}
}
