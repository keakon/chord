package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestRunInitialSetupWizardWritesFilesAndHidesAPIKey(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{
		"1", // provider choice
		"",  // api url default
		"",  // provider name default
		"",  // model default
		"1", // store hidden API key now
		"n", // proxy
	}
	if defaultShouldPromptIME() {
		if defaultIMEPromptEnabled() {
			inputs = append(inputs, "n")
		} else {
			inputs = append(inputs, "n")
		}
	}
	if defaultShouldPromptPreventSleep() {
		inputs = append(inputs, "")
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}
	readPassword := func(int) ([]byte, error) { return []byte("sk-test-secret"), nil }

	if err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{OpenTTY: openTTY, ReadPassword: readPassword}); err != nil {
		t.Fatalf("RunInitialSetupWizard: %v", err)
	}

	configPath := filepath.Join(configHome, "config.yaml")
	authPath := filepath.Join(configHome, "auth.yaml")
	cfg, err := config.LoadConfigFromPath(configPath)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if cfg.Providers["openai"].Type != config.ProviderTypeResponses {
		t.Fatalf("provider type = %q", cfg.Providers["openai"].Type)
	}
	if got := cfg.ModelPools["default"]; len(got) != 1 || got[0] != "openai/gpt-6.1-sol" {
		t.Fatalf("model_pools.default = %#v", got)
	}
	if defaultShouldPromptPreventSleep() {
		if cfg.PreventSleep == nil || *cfg.PreventSleep {
			t.Fatalf("prevent_sleep = %#v", cfg.PreventSleep)
		}
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	if got := auth["openai"]; len(got) != 1 || got[0].APIKey != "sk-test-secret" {
		t.Fatalf("auth[openai] = %#v", got)
	}
	authInfo, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("Stat(auth.yaml): %v", err)
	}
	if mode := authInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("auth mode = %o, want 600", mode)
	}

	outData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(output): %v", err)
	}
	output := string(outData)
	if strings.Contains(output, "sk-test-secret") {
		t.Fatalf("wizard output leaked API key: %s", output)
	}
	if !strings.Contains(output, configPath) || !strings.Contains(output, authPath) {
		t.Fatalf("wizard output did not include real paths: %s", output)
	}
	if !strings.Contains(output, "chord doctor models --model 'openai/gpt-6.1-sol'") {
		t.Fatalf("wizard output missing next step: %s", output)
	}
	if !strings.Contains(output, "Model access depends on your account and endpoint") {
		t.Fatalf("wizard output missing model access guidance: %s", output)
	}
	if !strings.Contains(output, "Edit config.yaml") || !strings.Contains(output, "Edit auth.yaml") {
		t.Fatalf("wizard output missing edit guidance: %s", output)
	}
}

func TestRunInitialSetupWizardReusesExistingAuthCredential(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.WriteFile(filepath.Join(configHome, "auth.yaml"), []byte("openai:\n  - old-openai-key\n"), 0o600); err != nil {
		t.Fatalf("write auth fixture: %v", err)
	}

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{
		"1", // provider choice
		"",  // api url default
		"",  // provider name default
		"",  // model default
		"1", // reuse existing auth
		"n", // proxy
	}
	if defaultShouldPromptIME() {
		inputs = append(inputs, "n")
	}
	if defaultShouldPromptPreventSleep() {
		inputs = append(inputs, "")
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}
	readPasswordCalled := false
	readPassword := func(int) ([]byte, error) {
		readPasswordCalled = true
		return []byte("should-not-be-read"), nil
	}

	if err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{OpenTTY: openTTY, ReadPassword: readPassword}); err != nil {
		t.Fatalf("RunInitialSetupWizard: %v", err)
	}
	if readPasswordCalled {
		t.Fatal("expected existing auth reuse to skip hidden input")
	}
	auth, err := config.LoadAuthConfig(filepath.Join(configHome, "auth.yaml"))
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	if got := auth["openai"]; len(got) != 1 || got[0].APIKey != "old-openai-key" {
		t.Fatalf("auth[openai] = %#v", got)
	}
	outData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(output): %v", err)
	}
	output := string(outData)
	if !strings.Contains(output, "Found existing credentials") {
		t.Fatalf("wizard output missing reuse notice: %s", output)
	}
	if !strings.Contains(output, filepath.Join(configHome, "auth.yaml")) {
		t.Fatalf("wizard output missing auth path: %s", output)
	}
}

func TestRunInitialSetupWizardReportsEnvVarReminder(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{
		"1", // provider choice
		"https://gateway.example.com/v1/chat/completions",
		"gateway-openai", // provider name
		"custom-model",
		"2", // env placeholder
		"",  // accept default env var name
		"n", // proxy
	}
	if defaultShouldPromptIME() {
		inputs = append(inputs, "n")
	}
	if defaultShouldPromptPreventSleep() {
		inputs = append(inputs, "")
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}

	if err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{OpenTTY: openTTY, ReadPassword: func(int) ([]byte, error) { return nil, nil }}); err != nil {
		t.Fatalf("RunInitialSetupWizard: %v", err)
	}
	authData, err := os.ReadFile(filepath.Join(configHome, "auth.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(auth.yaml): %v", err)
	}
	if !strings.Contains(string(authData), "$GATEWAY_OPENAI_API_KEY") {
		t.Fatalf("auth.yaml missing env placeholder: %s", string(authData))
	}
	outData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(output): %v", err)
	}
	output := string(outData)
	if !strings.Contains(output, "Set GATEWAY_OPENAI_API_KEY") {
		t.Fatalf("wizard output missing env var reminder: %s", output)
	}
	if !strings.Contains(output, "starter limit defaults for chat-completions") {
		t.Fatalf("wizard output missing limit reminder: %s", output)
	}
}

func TestRunInitialSetupWizardUsesControllingTTYWhenStdinRedirected(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	redirectedInputPath := filepath.Join(t.TempDir(), "redirected-input.txt")
	if err := os.WriteFile(redirectedInputPath, []byte("ignored\n"), 0o600); err != nil {
		t.Fatalf("write redirected input: %v", err)
	}
	redirectedIn, err := os.Open(redirectedInputPath)
	if err != nil {
		t.Fatalf("Open(redirected input): %v", err)
	}
	defer redirectedIn.Close()

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{
		"1", // api-key provider
		"",  // api url default
		"",  // provider name default
		"",  // model default
		"3", // skip credential for now
		"n", // proxy
	}
	if defaultShouldPromptIME() {
		inputs = append(inputs, "n")
	}
	if defaultShouldPromptPreventSleep() {
		inputs = append(inputs, "")
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTYCalls := 0
	openTTY := func() (*os.File, *os.File, error) {
		openTTYCalls++
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}

	if err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{
		In:           redirectedIn,
		Out:          io.Discard,
		OpenTTY:      openTTY,
		ReadPassword: func(int) ([]byte, error) { return nil, nil },
	}); err != nil {
		t.Fatalf("RunInitialSetupWizard: %v", err)
	}
	if openTTYCalls != 1 {
		t.Fatalf("openTTYCalls = %d, want 1", openTTYCalls)
	}
	cfg, err := config.LoadConfigFromPath(filepath.Join(configHome, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if _, ok := cfg.Providers["openai"]; !ok {
		t.Fatalf("expected openai provider in config: %#v", cfg.Providers)
	}
}

func TestRunInitialSetupWizardSkipsIMEWhenUnsupported(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{
		"1", // api-key provider
		"",  // api url default
		"",  // provider name default
		"",  // model default
		"3", // skip credential for now
		"n", // proxy
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}

	if err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{
		OpenTTY:                  openTTY,
		ReadPassword:             func(int) ([]byte, error) { return nil, nil },
		ShouldPromptIME:          func() bool { return false },
		ShouldPromptPreventSleep: func() bool { return false },
	}); err != nil {
		t.Fatalf("RunInitialSetupWizard: %v", err)
	}
	outData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(output): %v", err)
	}
	output := string(outData)
	if strings.Contains(output, "IME switching is helpful") {
		t.Fatalf("unexpected IME prompt in output: %s", output)
	}
}

func TestRunInitialSetupWizardCodexSkeletonSupportsAuthCommand(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	// The provider resolution behind the wizard carries the connect-level
	// trust contract; run outside any checkout so no project inputs gate it.
	t.Chdir(t.TempDir())

	origLogin := runSetupCodexOAuthLoginFunc
	defer func() { runSetupCodexOAuthLoginFunc = origLogin }()
	loginCalled := false
	runSetupCodexOAuthLoginFunc = func(ctx context.Context, termIO *setupTerminal, providerName string, providerCfg config.ProviderConfig, globalProxy string) error {
		loginCalled = true
		if providerName != "codex" {
			return fmt.Errorf("unexpected provider %q", providerName)
		}
		return nil
	}

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{
		"2", // codex
		"",  // provider name default
		"n", // proxy
	}
	if defaultShouldPromptIME() {
		inputs = append(inputs, "n")
	}
	if defaultShouldPromptPreventSleep() {
		inputs = append(inputs, "")
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}

	if err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{OpenTTY: openTTY, ReadPassword: func(int) ([]byte, error) { return nil, nil }}); err != nil {
		t.Fatalf("RunInitialSetupWizard: %v", err)
	}
	outData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(output): %v", err)
	}
	if strings.Contains(string(outData), "Model [gpt-6.1-sol]:") {
		t.Fatalf("unexpected Codex model prompt in output: %s", string(outData))
	}
	if !loginCalled {
		t.Fatal("expected Codex OAuth login to run during setup")
	}
	providerName, providerCfg, _, err := resolveAuthLoginProvider(context.Background(), "codex", strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("resolveAuthLoginProvider(codex): %v", err)
	}
	if providerName != "codex" {
		t.Fatalf("providerName = %q, want codex", providerName)
	}
	if !strings.EqualFold(strings.TrimSpace(providerCfg.Preset), config.ProviderPresetCodex) {
		t.Fatalf("provider preset = %q, want %q", providerCfg.Preset, config.ProviderPresetCodex)
	}
	wantModels := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol"}
	for _, model := range wantModels {
		if _, ok := providerCfg.Models[model]; !ok {
			t.Fatalf("missing configured codex model %q", model)
		}
	}
}

func TestRunInitialSetupWizardRollsBackNewAuthWhenConfigWriteFails(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	origWrite := writeInitialConfigFileFunc
	defer func() { writeInitialConfigFileFunc = origWrite }()
	writeInitialConfigFileFunc = func(string, []byte) error { return errors.New("config write failed") }

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{
		"1",
		"",
		"",
		"",
		"1",
		"n",
	}
	if defaultShouldPromptIME() {
		inputs = append(inputs, "n")
	}
	if defaultShouldPromptPreventSleep() {
		inputs = append(inputs, "")
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}

	err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{OpenTTY: openTTY, ReadPassword: func(int) ([]byte, error) { return []byte("sk-test-secret"), nil }})
	if err == nil || !strings.Contains(err.Error(), "config write failed") {
		t.Fatalf("RunInitialSetupWizard error = %v, want config write failure", err)
	}
	authPath := filepath.Join(configHome, "auth.yaml")
	if _, statErr := os.Stat(authPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("auth.yaml should be rolled back on config failure, stat err=%v", statErr)
	}
}

func TestRunInitialSetupWizardRestoresExistingAuthWhenConfigWriteFails(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	origWrite := writeInitialConfigFileFunc
	defer func() { writeInitialConfigFileFunc = origWrite }()
	writeInitialConfigFileFunc = func(string, []byte) error { return errors.New("config write failed") }
	authPath := filepath.Join(configHome, "auth.yaml")
	origAuth := []byte("openai:\n  - old-openai-key\n")
	if err := os.WriteFile(authPath, origAuth, 0o600); err != nil {
		t.Fatalf("write auth fixture: %v", err)
	}

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{
		"1",
		"",
		"",
		"",
		"2",
		"n",
	}
	if defaultShouldPromptIME() {
		inputs = append(inputs, "n")
	}
	if defaultShouldPromptPreventSleep() {
		inputs = append(inputs, "")
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}

	err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{OpenTTY: openTTY, ReadPassword: func(int) ([]byte, error) { return []byte("sk-replacement-secret"), nil }})
	if err == nil || !strings.Contains(err.Error(), "config write failed") {
		t.Fatalf("RunInitialSetupWizard error = %v, want config write failure", err)
	}
	got, readErr := os.ReadFile(authPath)
	if readErr != nil {
		t.Fatalf("ReadFile(auth.yaml): %v", readErr)
	}
	if string(got) != string(origAuth) {
		t.Fatalf("auth.yaml = %q, want original %q", got, origAuth)
	}
}

func TestRunInitialSetupWizardCodexLoginFailureRemovesConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	origLogin := runSetupCodexOAuthLoginFunc
	defer func() { runSetupCodexOAuthLoginFunc = origLogin }()
	runSetupCodexOAuthLoginFunc = func(context.Context, *setupTerminal, string, config.ProviderConfig, string) error {
		return errors.New("oauth failed")
	}

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{"2", "", "n"}
	if defaultShouldPromptIME() {
		inputs = append(inputs, "n")
	}
	if defaultShouldPromptPreventSleep() {
		inputs = append(inputs, "")
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}

	err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{OpenTTY: openTTY, ReadPassword: func(int) ([]byte, error) { return nil, nil }})
	if err == nil || !strings.Contains(err.Error(), "oauth failed") {
		t.Fatalf("RunInitialSetupWizard error = %v, want oauth failure", err)
	}
	configPath := filepath.Join(configHome, "config.yaml")
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected config.yaml to be removed after failed oauth, stat err=%v", statErr)
	}
}

func TestRunInitialSetupWizardShowsProxyExamples(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{"1", "", "", "", "3", "n"}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}

	if err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{
		OpenTTY:                  openTTY,
		ReadPassword:             func(int) ([]byte, error) { return nil, nil },
		ShouldPromptIME:          func() bool { return false },
		ShouldPromptPreventSleep: func() bool { return false },
	}); err != nil {
		t.Fatalf("RunInitialSetupWizard: %v", err)
	}
	outData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(output): %v", err)
	}
	output := string(outData)
	if !strings.Contains(output, "http://127.0.0.1:1080") || !strings.Contains(output, "socks5://127.0.0.1:1080") {
		t.Fatalf("wizard output missing proxy examples: %s", output)
	}
}

func TestRunInitialSetupWizardInfersGeminiDefaultsFromModelsEndpoint(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{
		"1",
		"https://generativelanguage.googleapis.com/v1beta/",
		"",
		"",
		"3",
		"n",
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}

	if err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{
		OpenTTY:                  openTTY,
		ReadPassword:             func(int) ([]byte, error) { return nil, nil },
		ShouldPromptIME:          func() bool { return false },
		ShouldPromptPreventSleep: func() bool { return false },
	}); err != nil {
		t.Fatalf("RunInitialSetupWizard: %v", err)
	}
	cfg, err := config.LoadConfigFromPath(filepath.Join(configHome, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	prov := cfg.Providers["gemini"]
	if prov.Type != config.ProviderTypeGenerateContent {
		t.Fatalf("provider type = %q, want %q", prov.Type, config.ProviderTypeGenerateContent)
	}
	if prov.APIURL != "https://generativelanguage.googleapis.com/v1beta/" {
		t.Fatalf("provider api_url = %q", prov.APIURL)
	}
	if _, ok := prov.Models["gemini-3.8-flash"]; !ok {
		t.Fatalf("expected default gemini model, got %#v", prov.Models)
	}
}

func TestRunInitialSetupWizardUsesABCDefaultWhenIMEPromptDefaultsYes(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	inputPath := filepath.Join(t.TempDir(), "wizard-input.txt")
	inputs := []string{
		"1",
		"",
		"",
		"",
		"3",
		"n",
		"",
		"", // accept the default IME target with an explicit Enter
	}
	if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write wizard input: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "wizard-output.txt")
	openTTY := func() (*os.File, *os.File, error) {
		in, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, err
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = in.Close()
			return nil, nil, err
		}
		return in, out, nil
	}

	origDefaultIMEPromptEnabled := defaultIMEPromptEnabledFunc
	defaultIMEPromptEnabledFunc = func() bool { return true }
	defer func() { defaultIMEPromptEnabledFunc = origDefaultIMEPromptEnabled }()

	if err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{
		OpenTTY:                  openTTY,
		ReadPassword:             func(int) ([]byte, error) { return nil, nil },
		ShouldPromptIME:          func() bool { return true },
		ShouldPromptPreventSleep: func() bool { return false },
	}); err != nil {
		t.Fatalf("RunInitialSetupWizard: %v", err)
	}
	cfg, err := config.LoadConfigFromPath(filepath.Join(configHome, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if cfg.IMESwitchTarget != "com.apple.keylayout.ABC" {
		t.Fatalf("ime_switch_target = %q, want %q", cfg.IMESwitchTarget, "com.apple.keylayout.ABC")
	}
	outData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(output): %v", err)
	}
	if !strings.Contains(string(outData), "Configure IME switching for Normal mode? [Y/n]:") {
		t.Fatalf("wizard output missing default-yes IME prompt: %s", string(outData))
	}
}

func TestRunInitialSetupWizardPreventSleepChoice(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prompt bool
		input  string
		want   bool
	}{
		{name: "default", prompt: true},
		{name: "yes", prompt: true, input: "yes", want: true},
		{name: "no", prompt: true, input: "no"},
		{name: "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", configHome)
			inputPath := filepath.Join(t.TempDir(), "input.txt")
			inputs := []string{"1", "", "", "", "3", "n"}
			if tc.prompt {
				inputs = append(inputs, tc.input)
			}
			if err := os.WriteFile(inputPath, []byte(strings.Join(inputs, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			outputPath := filepath.Join(t.TempDir(), "output.txt")
			err := RunInitialSetupWizard(context.Background(), SetupWizardOptions{
				OpenTTY: func() (*os.File, *os.File, error) {
					in, err := os.Open(inputPath)
					if err != nil {
						return nil, nil, err
					}
					out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
					if err != nil {
						_ = in.Close()
						return nil, nil, err
					}
					return in, out, nil
				},
				ReadPassword:             func(int) ([]byte, error) { return nil, nil },
				ShouldPromptIME:          func() bool { return false },
				ShouldPromptPreventSleep: func() bool { return tc.prompt },
			})
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfigFromPath(filepath.Join(configHome, "config.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.prompt {
				if cfg.PreventSleep == nil || *cfg.PreventSleep != tc.want {
					t.Fatalf("prevent_sleep = %v, want %v", cfg.PreventSleep, tc.want)
				}
			} else if cfg.PreventSleep != nil {
				t.Fatalf("unsupported platform stored prevent_sleep = %v", cfg.PreventSleep)
			}
			output, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(output), "Enable prevent_sleep while agents are active? [y/N]:"); got != tc.prompt {
				t.Fatalf("prompt present = %v, want %v", got, tc.prompt)
			}
		})
	}
}

func TestSelectChoiceBySingleKey(t *testing.T) {
	allowed := map[byte]struct{}{'1': {}, '2': {}}
	prompt := "Provider [1]: "
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{name: "digit submits without enter", input: "1", want: "1"},
		{name: "second digit submits without enter", input: "2", want: "2"},
		{name: "carriage return selects default", input: "\r", want: "1"},
		{name: "newline selects default", input: "\n", want: "1"},
		{name: "bracketed paste consumed", input: "\x1b[200~1x2\x1b[201~", want: "1"},
		{name: "multiline paste consumed", input: "\x1b[200~1\r\n2\x1b[201~", want: "1"},
		{name: "arrow sequence swallowed", input: "\x1b[A1", want: "1"},
		{name: "bare escape restarts setup", input: "\x1b1", wantErr: errWizardRestart},
		{name: "bare escape after arrow sequence restarts setup", input: "\x1b[A\x1b", wantErr: errWizardRestart},
		{name: "ctrl-d is EOF", input: "\x04", wantErr: io.EOF},
		{name: "ctrl-c interrupts", input: "\x03", wantErr: errSingleKeyInterrupt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			value, err := selectChoiceBySingleKey(bufio.NewReader(strings.NewReader(tc.input)), out, prompt, allowed, '1', "Please choose one of the listed options.", true, nil)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if value != tc.want {
				t.Fatalf("value = %q, want %q", value, tc.want)
			}
			if !strings.Contains(out.String(), tc.want+"\r\n") {
				t.Fatalf("output missing echo of %q: %q", tc.want, out.String())
			}
		})
	}
}

func TestSelectChoiceBySingleKeyRepromptsOnInvalidKey(t *testing.T) {
	out := &bytes.Buffer{}
	_, err := selectChoiceBySingleKey(bufio.NewReader(strings.NewReader("x")), out, "Provider [1]: ", map[byte]struct{}{'1': {}, '2': {}}, '1', "Please choose one of the listed options.", true, nil)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	if !strings.Contains(out.String(), "Please choose one of the listed options.") || !strings.Contains(out.String(), "Provider [1]: ") {
		t.Fatalf("output missing reprompt message and prompt: %q", out.String())
	}
}

func TestSelectChoiceBySingleKeyNoDefault(t *testing.T) {
	// The project trust prompt has no default answer: Enter reprompts instead
	// of submitting a phantom choice.
	out := &bytes.Buffer{}
	_, err := selectChoiceBySingleKey(
		bufio.NewReader(strings.NewReader("\n")), out, "Choice: ",
		map[byte]struct{}{'1': {}, '2': {}, '3': {}}, 0,
		"Enter 1 to apply, 2 to skip.", false, nil)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	if !strings.Contains(out.String(), "Enter 1 to apply, 2 to skip.") {
		t.Fatalf("output missing reprompt after Enter: %q", out.String())
	}

	// A bare Esc is ignored when escRestarts is false (project trust prompt);
	// it restarts setup only in the setup wizard.
	out.Reset()
	value, err := selectChoiceBySingleKey(
		bufio.NewReader(strings.NewReader("\x1b2")), out, "Choice: ",
		map[byte]struct{}{'1': {}, '2': {}, '3': {}}, 0,
		"Enter 1 to apply, 2 to skip.", false, nil)
	if err != nil || value != "2" {
		t.Fatalf("value/err = %q/%v, want 2/nil", value, err)
	}
	if strings.Contains(out.String(), "^[") {
		t.Fatalf("bare Esc should be silently ignored, got %q", out.String())
	}
}

func TestPromptChoiceSingleKeyFallsBackWithoutTTY(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	termIO := &setupTerminal{in: f, out: &bytes.Buffer{}, reader: bufio.NewReader(f)}
	value, handled, err := promptChoiceSingleKey(termIO, "Provider [1]: ", []string{"1", "2"}, "1", "Please choose one of the listed options.")
	if handled {
		t.Fatal("handled = true, want false for non-TTY input")
	}
	if value != "" || err != nil {
		t.Fatalf("value/err = %q/%v, want empty/nil", value, err)
	}
}

func TestPromptLineKeepsEscAsTextWithoutTTY(t *testing.T) {
	// Esc-to-restart is terminal-only; on non-TTY input Esc stays plain text
	// so piped scripts cannot spin the restart loop.
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("\x1b\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	termIO := &setupTerminal{in: f, out: &bytes.Buffer{}, reader: bufio.NewReader(f), escRestarts: true}
	line, err := promptLine(termIO, "Provider [1]: ")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if line != "\x1b" {
		t.Fatalf("line = %q, want %q", line, "\x1b")
	}
}
