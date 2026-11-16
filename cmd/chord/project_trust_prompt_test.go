package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectTrustPromptPreviewAndDetails(t *testing.T) {
	discovery := &projectTrustDiscovery{Sources: []projectTrustSource{{
		File:  ".chord/commands/sample.md",
		Items: []string{"command one", "command two", "command three", "command four", "command five"},
	}}}
	var out bytes.Buffer
	renderProjectTrustAuthorization(&out, discovery, "/sample")
	preview := out.String()
	for _, want := range []string{"command four", "1 more entries", "Start with global settings", "Changes to these declaration files"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview missing %q:\n%s", want, preview)
		}
	}
	for _, unwanted := range []string{".chord/commands/sample.md", "command five", "MCP", "Hooks", "endpoints"} {
		if strings.Contains(preview, unwanted) {
			t.Fatalf("preview contains %q:\n%s", unwanted, preview)
		}
	}
	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"accept", "1\n", true},
		{"skip", "2\n", false},
		{"empty then EOF", "\n", false},
		{"details then EOF", "3\n", false},
		{"details then skip", "3\n2\n", false},
		{"details then accept", "3\n1\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			terminal := &setupTerminal{out: &output, reader: bufio.NewReader(strings.NewReader(tc.input))}
			got, err := promptProjectTrustOnTerminal(terminal, discovery, "/sample")
			if err != nil || got != tc.want {
				t.Fatalf("authorized=%v err=%v, want %v", got, err, tc.want)
			}
			if strings.HasPrefix(tc.input, "3") {
				for _, want := range []string{".chord/commands/sample.md", "command five"} {
					if !strings.Contains(output.String(), want) {
						t.Fatalf("details missing %q:\n%s", want, output.String())
					}
				}
			}
		})
	}
}

func TestProjectTrustPromptShowsDeclaredEffects(t *testing.T) {
	root := t.TempDir()
	writeProjectConfigForTrustTest(t, root, `mcp:
  sample:
    command: sample-server
hooks:
  on_session_start:
    - command: sample-start
  on_session_end:
    - command: sample-end
providers:
  sample:
    api_url: https://example.invalid/v1
`)
	discovery, err := discoverProjectTrust(root, root)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	renderProjectTrustAuthorization(&out, discovery, root)
	text := out.String()
	for _, want := range []string{"sample-server", "Hooks can run commands", "configured provider or proxy endpoints"} {
		if !strings.Contains(text, want) {
			t.Fatalf("disclosure missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "Hooks can run commands") != 1 || strings.Contains(text, "LSP servers") {
		t.Fatalf("duplicated or unrelated effects:\n%s", text)
	}
}

func TestProjectTrustAgentMCPDetails(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "reviewer.yaml")
	if err := os.WriteFile(path, []byte("name: reviewer\nmcp:\n  sample:\n    command: sample-server\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sources := agentDisclosureSources(root, []string{path})
	var out bytes.Buffer
	renderProjectTrustSources(&out, sources)
	if !strings.Contains(out.String(), "sample-server") || len(projectTrustEffects(sources)) != 1 {
		t.Fatalf("agent MCP disclosure missing target or effect: %s", out.String())
	}
}
