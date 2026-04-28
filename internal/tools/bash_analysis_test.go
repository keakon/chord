package tools

import (
	"reflect"
	"testing"
)

func TestAnalyzeBashCommandExtractsCompoundSubcommands(t *testing.T) {
	analysis, err := AnalyzeBashCommand("echo foo && rm bar")
	if err != nil {
		t.Fatalf("AnalyzeBashCommand: %v", err)
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

func TestAnalyzeBashCommandExtractsNestedCommandSubstitution(t *testing.T) {
	analysis, err := AnalyzeBashCommand(`echo "sha=$(git rev-parse HEAD)" && pwd`)
	if err != nil {
		t.Fatalf("AnalyzeBashCommand: %v", err)
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

func TestAnalyzeBashCommandSkipsFunctionBodies(t *testing.T) {
	analysis, err := AnalyzeBashCommand("cleanup() { rm bar; }\npwd")
	if err != nil {
		t.Fatalf("AnalyzeBashCommand: %v", err)
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
