package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func runGuidedConfigAdd(t *testing.T, ref, input string, opts configAddOptions) (string, error) {
	t.Helper()
	var out strings.Builder
	opts.terminal = &setupTerminal{reader: bufio.NewReader(strings.NewReader(input)), out: &out}
	err := runConfigAdd(context.Background(), &out, ref, opts)
	return out.String(), err
}

func TestGuidedConfigAddNewGateway(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	input := "91not-a-url\nhttps://example.invalid/v1/responses\n$INVALID\nSAMPLE_API_KEY\nny"
	output, err := runGuidedConfigAdd(t, "sample/gpt-6-sol", input, configAddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := config.LoadResolvedConfig(filepath.Join(dir, "config.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	provider := rc.Config.Providers["sample"]
	model := provider.Models["gpt-6-sol"]
	if provider.Type != config.ProviderTypeResponses || provider.APIURL != "https://example.invalid/v1/responses" || provider.Compress != "" || model.Catalog.ID != "openai/gpt-6-sol" {
		t.Fatalf("unexpected gateway: %+v", provider)
	}
	if model.Compat == nil || model.Compat.Responses == nil || len(model.Variants) == 0 || model.Limit.Context != 1050000 {
		t.Fatalf("missing inherited recipe: %+v", model)
	}
	auth, err := os.ReadFile(filepath.Join(dir, "auth.yaml"))
	if err != nil || !strings.Contains(string(auth), "$SAMPLE_API_KEY") {
		t.Fatalf("credential reference: %s, %v", auth, err)
	}
	for _, want := range []string{"No model is selected automatically", "Use an http(s) endpoint", "without $", "Configuration to save", "request compression: off"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in %s", want, output)
		}
	}
}

func TestGuidedConfigAddExistingGatewayCustomize(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	path := filepath.Join(dir, "config.yaml")
	initial := "providers:\n  sample:\n    type: responses\n    api_url: https://example.invalid/v1/responses\n    compress: zstd\nmodel_pools:\n  coding: []\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAMPLE_UNSET_KEY", "")
	auth := "sample: [$SAMPLE_UNSET_KEY]\n"
	authPath := filepath.Join(dir, "auth.yaml")
	if err := os.WriteFile(authPath, []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runGuidedConfigAdd(t, "sample/gpt-6-sol", "1y1\n92y", configAddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := config.LoadResolvedConfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if rc.Config.Providers["sample"].Compress != config.RequestCompressionZstd || rc.Config.ModelPools["coding"][0] != "sample/gpt-6-sol@high" {
		t.Fatalf("customization not saved: %+v", rc.Config)
	}
	rawAuth, err := os.ReadFile(authPath)
	if err != nil || string(rawAuth) != auth {
		t.Fatalf("existing auth changed: %s, %v", rawAuth, err)
	}
	if strings.Contains(output, "API key environment variable") || !strings.Contains(output, "Available reasoning variants") {
		t.Fatalf("unexpected prompts: %s", output)
	}
}

func TestGuidedConfigAddCancellationDoesNotCreateFiles(t *testing.T) {
	for _, input := range []string{"", "\n0", "q", "\x1b", "\x03", "1q\n", "1https://example.invalid/v1/responses\nq\n", "1https://example.invalid/v1/responses\nSAMPLE_KEY\nn", "1https://example.invalid/v1/responses\nSAMPLE_KEY\nnn"} {
		t.Run(input, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", dir)
			output, err := runGuidedConfigAdd(t, "sample/gpt-6-sol", input, configAddOptions{})
			if err != nil || !strings.Contains(output, "Cancelled") {
				t.Fatalf("cancellation: %v, %s", err, output)
			}
			for _, file := range []string{"config.yaml", "auth.yaml"} {
				if _, err := os.Stat(filepath.Join(dir, file)); !os.IsNotExist(err) {
					t.Fatalf("cancelled wizard created %s: %v", file, err)
				}
			}
		})
	}
}

func TestGuidedConfigAddCancellationPreservesFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	files := map[string]string{
		"config.yaml": "providers:\n  sample:\n    type: responses\n    api_url: https://example.invalid/v1/responses\n    compress: gzip\n",
		"auth.yaml":   "sample: [sample-key]\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if output, err := runGuidedConfigAdd(t, "sample/gpt-6-sol", "1ymcoding\n32n", configAddOptions{}); err != nil || !strings.Contains(output, "Cancelled") {
		t.Fatalf("cancellation: %v, %s", err, output)
	}
	for name, expected := range files {
		actual, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(actual) != expected {
			t.Fatalf("cancel changed %s: %s, %v", name, actual, err)
		}
	}
}

func TestConfigAddCommandNonInteractive(t *testing.T) {
	for _, noInteractive := range []bool{false, true} {
		t.Run(strconv.FormatBool(noInteractive), func(t *testing.T) {
			t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
			cmd := newConfigAddCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetIn(strings.NewReader("1\n"))
			args := []string{"sample/gpt-6-sol", "--url", "https://example.invalid/v1/responses"}
			if noInteractive {
				args = append(args, "--no-interactive")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("redirected input must not adopt a suggestion")
			}
			cmd = newConfigAddCmd()
			cmd.SetOut(io.Discard)
			cmd.SetIn(strings.NewReader(""))
			cmd.SetArgs([]string{"sample/gpt-6-sol", "--url", "https://example.invalid/v1/responses", "--catalog", "openai/gpt-6-sol", "--variant", "high", "--compress", "gzip", "--api-key-env", "SAMPLE_KEY", "--no-interactive"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			path, _ := config.ConfigPath()
			rc, err := config.LoadResolvedConfig(path, "")
			if err != nil || rc.Config.Providers["sample"].Compress != "gzip" || rc.Config.ModelPools["default"][0] != "sample/gpt-6-sol@high" {
				t.Fatalf("script configuration: %+v, %v", rc, err)
			}
		})
	}
}

func TestConfigAddInvalidOptionsDoNotWrite(t *testing.T) {
	for _, opts := range []configAddOptions{{envVar: "$INVALID"}, {compress: "br"}, {variant: "missing"}} {
		dir := t.TempDir()
		t.Setenv("CHORD_CONFIG_HOME", dir)
		if err := runConfigAdd(context.Background(), io.Discard, "openai/gpt-6-sol", opts); err == nil {
			t.Fatalf("invalid options accepted: %+v", opts)
		}
		if _, err := os.Stat(filepath.Join(dir, "config.yaml")); !os.IsNotExist(err) {
			t.Fatalf("invalid options wrote config: %v", err)
		}
	}
}

func TestGuidedConfigAddOfficialDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	output, err := runGuidedConfigAdd(t, "openai/gpt-6-sol", "ny", configAddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "Catalog model:") || strings.Contains(output, "API key environment variable [") || strings.Count(output, "Resolved model facts:") != 1 {
		t.Fatalf("official setup should reuse documented defaults and show one preview: %s", output)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil || strings.Contains(string(raw), "compress:") {
		t.Fatalf("official compression must remain disabled: %s, %v", raw, err)
	}
}

func TestGuidedConfigAddDisablesExistingCompression(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	path := filepath.Join(dir, "config.yaml")
	initial := "providers:\n  sample:\n    type: responses\n    api_url: https://example.invalid/v1/responses\n    compress: gzip\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runGuidedConfigAdd(t, "sample/gpt-6-sol", "1-\ny\n1\ny", configAddOptions{}); err != nil {
		t.Fatal(err)
	}
	rc, err := config.LoadResolvedConfig(path, "")
	if err != nil || rc.Config.Providers["sample"].Compress != "" {
		t.Fatalf("explicit off did not disable compression: %+v, %v", rc, err)
	}
}

func TestGuidedConfigAddAnchoredConfigAndNewPool(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	t.Setenv("SAMPLE_KEY", "")
	original := `model_templates:
  base: &base
    limit: {context: 1050000, output: 128000}
providers:
  sample:
    type: responses
    api_url: https://example.invalid/v1/responses
    compress: gzip
    models:
      gpt-6-sol: *base
      model-2: *base
model_pools:
  default: []
  review: []
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.yaml"), []byte("sample: [$SAMPLE_KEY]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := runGuidedConfigAdd(t, "sample/gpt-6-sol", "1ymcoding\n21y", configAddOptions{}); err != nil || !strings.Contains(output, "Create a new model pool") {
		t.Fatalf("anchored config setup: %v\n%s", err, output)
	}
	rc, err := config.LoadResolvedConfig(path, "")
	if err != nil || rc.Config.Providers["sample"].Compress != "gzip" || rc.Config.ModelPools["coding"][0] != "sample/gpt-6-sol" {
		t.Fatalf("anchored config resolution: %+v, %v", rc, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "&base") || !strings.Contains(string(raw), "*base") {
		t.Fatalf("unused shared template should remain anchored: %s, %v", raw, err)
	}
	if before, after := configAddYAMLValue(t, []byte(original), "providers", "sample", "models", "model-2"), configAddYAMLValue(t, raw, "providers", "sample", "models", "model-2"); !reflect.DeepEqual(before, after) {
		t.Fatalf("other shared model changed: before=%#v after=%#v", before, after)
	}
}
