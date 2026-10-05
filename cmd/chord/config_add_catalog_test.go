package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestCatalogAddCreatesFirstConfigAndIsIdempotent(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	var output strings.Builder
	for range 2 {
		if err := runConfigAdd(context.Background(), &output, "openai/gpt-6.1-sol", configAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(configHome, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "- openai/gpt-6.1-sol") != 1 || !strings.Contains(string(data), "preset: openai") {
		t.Fatalf("unexpected first config: %s", data)
	}
	auth, err := os.ReadFile(filepath.Join(configHome, "auth.yaml"))
	if err != nil || !strings.Contains(string(auth), "$OPENAI_API_KEY") {
		t.Fatalf("auth did not record an environment reference: %s, %v", auth, err)
	}
}

func TestCatalogAddKeepsExistingGateway(t *testing.T) {
	provider := config.ProviderConfig{APIURL: "https://example.invalid/v1/messages", Type: config.ProviderTypeMessages}
	got, wire, opts, err := prepareCatalogAdd("openai/gpt-6.1-sol", provider, true, "gpt-6.1-sol", configAddOptions{})
	if err != nil || got.APIURL != provider.APIURL || got.Type != provider.Type || got.Preset != "" || wire != "gpt-6.1-sol" || opts.url != "" || opts.envVar != "" || opts.catalogID != "openai/gpt-6.1-sol" {
		t.Fatalf("existing gateway changed: %+v, %s, %+v, %v", got, wire, opts, err)
	}
}

func TestCatalogAddExplicitBorrowDoesNotPickOtherModelEndpoint(t *testing.T) {
	_, _, opts, err := prepareCatalogAdd("openai/gpt-6.1-sol", config.ProviderConfig{}, false, "gpt-6.1-sol", configAddOptions{catalogID: "anthropic/claude-opus-5-5"})
	if err != nil || opts.url != "" || opts.envVar != "" {
		t.Fatalf("explicit borrow chose an unrelated connection: %+v, %v", opts, err)
	}
}

func TestCatalogAddDocumentedConnectionAndUsableInputBudget(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	t.Setenv("MOONSHOT_API_KEY", "sample-secret-not-to-be-copied")
	var output strings.Builder
	if err := runConfigAdd(context.Background(), &output, "moonshotai/kimi-k3", configAddOptions{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "https://api.moonshot.ai/v1/chat/completions") || !strings.Contains(string(raw), "catalog: moonshotai/kimi-k3") {
		t.Fatalf("missing connection or explicit facts: %s", raw)
	}
	auth, err := os.ReadFile(filepath.Join(dir, "auth.yaml"))
	if err != nil || !strings.Contains(string(auth), "$MOONSHOT_API_KEY") || strings.Contains(string(auth), "sample-secret") {
		t.Fatalf("credential must remain an environment reference: %s, %v", auth, err)
	}
	resolved, err := config.LoadResolvedConfig(filepath.Join(dir, "config.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	model := resolved.Config.Providers["moonshotai"].Models["kimi-k3"]
	if model.Compat == nil || model.Compat.ReasoningContinuity == nil || model.Compat.ReasoningContinuity.ReasoningReplay != config.ReasoningReplayAll {
		t.Fatalf("official connection omitted required replay: %+v", model.Compat)
	}
	if model.Limit.EffectiveInputBudget(64000, 64000) != 984576 {
		t.Fatalf("unusable large-output model budget: %+v", model.Limit)
	}
}

func TestCatalogAddCreatesOAuthConfigBeforeLogin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	var output strings.Builder
	if err := runConfigAdd(context.Background(), &output, "codex/gpt-6.1-sol", configAddOptions{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil || !strings.Contains(string(raw), "preset: codex") || strings.Contains(string(raw), "compress:") || !strings.Contains(output.String(), "chord auth codex") {
		t.Fatalf("OAuth setup: %s, %s, %v", raw, output.String(), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "auth.yaml")); !os.IsNotExist(err) {
		t.Fatalf("offline add must not manufacture OAuth credentials: %v", err)
	}
}

func TestCatalogAddConcurrentProviderCreationDoesNotOverwriteEndpoint(t *testing.T) {
	original := []byte("providers:\n  sample:\n    api_url: https://example.invalid/other/messages\n")
	if _, err := editConfigYAMLForAdd(original, configAddEdit{providerName: "sample", providerNew: true, apiURL: "https://example.invalid/v1/responses"}); err == nil {
		t.Fatal("a concurrently created provider must not be overwritten")
	}
}

func TestCatalogAddPreservesUnavailableCredentialDeclaration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	t.Setenv("SAMPLE_CUSTOM_KEY", "")
	if err := os.WriteFile(filepath.Join(dir, "auth.yaml"), []byte("openai: [$SAMPLE_CUSTOM_KEY]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := runConfigAdd(context.Background(), &output, "openai/gpt-6.1-sol", configAddOptions{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "auth.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "openai: [$SAMPLE_CUSTOM_KEY]\n" {
		t.Fatalf("explicit credential changed: %s", data)
	}
}

func TestCatalogAddGatewayRecipeAndExplicitCompression(t *testing.T) {
	for _, compression := range []string{"", "gzip", "zstd"} {
		t.Run("compression="+compression, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", dir)
			path := filepath.Join(dir, "config.yaml")
			original := "providers:\n  sample:\n    type: chat-completions\n    api_url: https://example.invalid/v1/chat/completions\n"
			if compression != "" {
				original += "    compress: " + compression + "\n"
			}
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			opts := configAddOptions{catalogID: "moonshotai/kimi-k3"}
			for range 2 {
				if err := runConfigAdd(context.Background(), &output, "sample/alias", opts); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(raw), "catalog: moonshotai/kimi-k3") != 1 || strings.Contains(string(raw), "reasoning_replay:") {
				t.Fatalf("recipe should remain a catalog reference: %s", raw)
			}
			resolved, err := config.LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatal(err)
			}
			provider := resolved.Config.Providers["sample"]
			if provider.Compress != compression {
				t.Fatalf("compression = %q, want %q", provider.Compress, compression)
			}
			model := provider.Models["alias"]
			if model.Compat == nil || model.Compat.ReasoningContinuity == nil || model.Compat.ReasoningContinuity.ReasoningReplay != config.ReasoningReplayAll {
				t.Fatalf("missing gateway replay recipe: %+v", model.Compat)
			}
		})
	}
}

func TestCatalogAddDoesNotEnableOfficialCompression(t *testing.T) {
	for _, ref := range []string{"anthropic/claude-opus-5-5", "codex/gpt-6.1-sol"} {
		t.Run(ref, func(t *testing.T) {
			t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
			var output strings.Builder
			if err := runConfigAdd(context.Background(), &output, ref, configAddOptions{}); err != nil {
				t.Fatal(err)
			}
			path, err := config.ConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := config.LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatal(err)
			}
			name, _, _ := strings.Cut(ref, "/")
			if got := resolved.Config.Providers[name].Compress; got != "" {
				t.Fatalf("automatic compression = %q", got)
			}
		})
	}
}

// configAddWriteObserver changes external state at a visible prompt/save boundary.
type configAddWriteObserver struct {
	strings.Builder
	observe func(string)
}

func (w *configAddWriteObserver) Write(p []byte) (int, error) {
	n, err := w.Builder.Write(p)
	if w.observe != nil {
		w.observe(string(p))
	}
	return n, err
}

func TestConfigAddRechecksConfirmedProvider(t *testing.T) {
	for _, changedEndpoint := range []bool{false, true} {
		t.Run(fmt.Sprintf("endpointChanged=%t", changedEndpoint), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", dir)
			path := filepath.Join(dir, "config.yaml")
			original := "providers:\n  openai:\n    type: responses\n    api_url: https://example.invalid/v1/responses\n"
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "auth.yaml"), []byte("openai: [$SAMPLE_API_KEY]\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			concurrent := original + "log_level: debug\n"
			if changedEndpoint {
				concurrent = strings.ReplaceAll(concurrent, "example.invalid/v1", "example.invalid/other")
			}
			var out configAddWriteObserver
			updated := false
			out.observe = func(text string) {
				if !updated && strings.Contains(text, "Save configuration?") {
					updated = true
					if err := os.WriteFile(path, []byte(concurrent), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			opts := configAddOptions{terminal: &setupTerminal{reader: bufio.NewReader(strings.NewReader("ny")), out: &out}}
			err := runConfigAdd(context.Background(), &out, "openai/gpt-6.1-sol", opts)
			if !updated {
				t.Fatalf("confirmation was not reached: %v; %s", err, out.String())
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if changedEndpoint {
				if err == nil || !strings.Contains(err.Error(), "endpoint changed") || string(raw) != concurrent {
					t.Fatalf("err=%v config=%s", err, raw)
				}
			} else if err != nil || !strings.Contains(string(raw), "log_level: debug") || !strings.Contains(string(raw), "openai/gpt-6.1-sol") {
				t.Fatalf("unrelated update was not merged: err=%v config=%s", err, raw)
			}
		})
	}
}

func TestConfigAddRejectsInvalidCredentialsBeforeSaving(t *testing.T) {
	for _, invalid := range []string{"[", "[]", "openai: invalid"} {
		t.Run(invalid, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", dir)
			if err := os.WriteFile(filepath.Join(dir, "auth.yaml"), []byte(invalid), 0o600); err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			err := runConfigAdd(context.Background(), &out, "openai/gpt-6.1-sol", configAddOptions{envVar: "SAMPLE_API_KEY"})
			if err == nil || !strings.Contains(err.Error(), "before saving config") {
				t.Fatalf("err=%v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "config.yaml")); !os.IsNotExist(err) {
				t.Fatalf("config was written: %v", err)
			}
		})
	}
}

func TestConfigAddReportsCredentialsFailureAfterSaving(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	var out configAddWriteObserver
	out.observe = func(text string) {
		if strings.Contains(text, "Updated") {
			if err := os.WriteFile(filepath.Join(dir, "auth.yaml"), []byte("["), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	err := runConfigAdd(context.Background(), &out, "openai/gpt-6.1-sol", configAddOptions{envVar: "SAMPLE_API_KEY"})
	if err == nil || !strings.Contains(err.Error(), "configuration saved") || !strings.Contains(err.Error(), "$SAMPLE_API_KEY in auth.yaml") {
		t.Fatalf("partial success not explained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatalf("saved config missing: %v", err)
	}
}
