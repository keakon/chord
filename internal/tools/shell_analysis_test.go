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

func TestAnalyzeShellCommandKeepsAssignmentsInSubcommandSource(t *testing.T) {
	// An assignment can change which binary the command name runs and how it
	// runs, so a narrow rule that matches only the command text must not cover
	// it. CommandSource keeps the bare command text so a deny rule can still
	// anchor to the command a user named.
	cases := []struct {
		name    string
		command string
		source  string
	}{
		{"prefix PATH", "PATH=/tmp/evil git status", "PATH=/tmp/evil git status"},
		{"prefix exec path", "GIT_EXEC_PATH=/tmp/evil git status", "GIT_EXEC_PATH=/tmp/evil git status"},
		{"prefix plain env", "FOO=bar git status", "FOO=bar git status"},
		{"statement", "PATH=/tmp/evil; git status", "PATH=/tmp/evil; git status"},
		{"and chain", "PATH=/tmp/evil && git status", "PATH=/tmp/evil && git status"},
		{"export", "export PATH=/tmp/evil; git status", "export PATH=/tmp/evil; git status"},
		{"declare", "declare -x PATH=/tmp/evil; git status", "declare -x PATH=/tmp/evil; git status"},
		{"nested block", "if true; then PATH=/tmp/evil; git status; fi", "PATH=/tmp/evil; git status"},
		{"enclosing block", "PATH=/tmp/evil; if true; then git status; fi", "PATH=/tmp/evil; if true; then git status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			analysis, err := AnalyzeShellCommand(tc.command)
			if err != nil {
				t.Fatalf("AnalyzeShellCommand: %v", err)
			}
			last := analysis.Subcommands[len(analysis.Subcommands)-1]
			if last.Source != tc.source {
				t.Fatalf("source = %q, want %q", last.Source, tc.source)
			}
			if last.CommandSource != "git status" {
				t.Fatalf("command source = %q, want %q", last.CommandSource, "git status")
			}
		})
	}
}

func TestAnalyzeShellCommandIgnoresAssignmentsWithoutAValue(t *testing.T) {
	// Exporting or redeclaring a name without a value cannot redirect the
	// command, and an assignment after the command cannot reach back to it, so
	// the bare command text stays the reviewed source.
	for _, command := range []string{
		"export PATH; git status",
		"declare -p PATH; git status",
		"git status; PATH=/tmp/evil",
	} {
		analysis, err := AnalyzeShellCommand(command)
		if err != nil {
			t.Fatalf("AnalyzeShellCommand(%q): %v", command, err)
		}
		last := analysis.Subcommands[len(analysis.Subcommands)-1]
		if last.Source != "git status" {
			t.Fatalf("AnalyzeShellCommand(%q): source = %q, want %q", command, last.Source, "git status")
		}
	}
}
