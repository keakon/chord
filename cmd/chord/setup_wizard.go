package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/term"
	tea "github.com/keakon/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
)

type SetupWizardOptions struct {
	In                       io.Reader
	Out                      io.Writer
	OpenTTY                  func() (*os.File, *os.File, error)
	ReadPassword             func(int) ([]byte, error)
	ShouldPromptIME          func() bool
	ShouldPromptPreventSleep func() bool
}

type setupTerminal struct {
	in           *os.File
	out          io.Writer
	reader       *bufio.Reader
	readPassword func(int) ([]byte, error)
	closer       func() error
}

type existingSetupCredentialState struct {
	AuthPath       string
	HasCredentials bool
}

type setupAuthRollback struct {
	path      string
	before    []byte
	existed   bool
	enabled   bool
	committed bool
}

var (
	runInitialSetupWizardFunc   = RunInitialSetupWizard
	defaultIMEPromptEnabledFunc = defaultIMEPromptEnabled
	runSetupCodexOAuthLoginFunc = runSetupCodexOAuthLogin
	writeInitialConfigFileFunc  = writeInitialConfigFile
)

func maybeRunInitialSetupWizard(cmd *cobra.Command) error {
	if cmd == nil || cmd.Parent() != nil {
		return nil
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	configPath, err := config.ConfigPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check config path %s: %w", configPath, err)
	}
	return runInitialSetupWizardFunc(ctx, SetupWizardOptions{})
}

func RunInitialSetupWizard(ctx context.Context, opts SetupWizardOptions) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	configPath, err := config.ConfigPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check config path %s: %w", configPath, err)
	}

	if opts.OpenTTY == nil {
		opts.OpenTTY = tea.OpenTTY
	}
	if opts.ReadPassword == nil {
		opts.ReadPassword = func(fd int) ([]byte, error) { return term.ReadPassword(uintptr(fd)) }
	}
	if opts.ShouldPromptIME == nil {
		opts.ShouldPromptIME = defaultShouldPromptIME
	}
	if opts.ShouldPromptPreventSleep == nil {
		opts.ShouldPromptPreventSleep = defaultShouldPromptPreventSleep
	}
	termIO, err := openSetupTerminal(opts)
	if err != nil {
		return initialSetupRequiredError()
	}
	defer func() {
		if termIO != nil && termIO.closer != nil {
			_ = termIO.closer()
		}
	}()

	out := termIO.out
	if out == nil {
		out = io.Discard
	}

	fmt.Fprintln(out, "No config.yaml found.")
	fmt.Fprintln(out, "Let's set up Chord for first use.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Chord stores behavior settings in config.yaml and credentials in auth.yaml.")
	fmt.Fprintln(out, "I'll show the exact file locations after setup.")
	fmt.Fprintln(out)

	choice, err := promptChoice(termIO, "Choose a provider:\n  1) API key provider\n  2) Codex OAuth\n\nProvider", []string{"1", "2"}, "1")
	if err != nil {
		return err
	}

	cfgInput := initialSetupConfigInput{}
	var (
		authProviderName string
		authValue        string
		authPathShown    bool
		nextStep         string
		envVarReminder   string
		limitReminder    string
	)

	switch choice {
	case "2":
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Codex OAuth will configure a preset: codex provider, then sign in before setup completes.")
		fmt.Fprintln(out)
		cfgInput.Kind = initialSetupProviderCodex
		cfgInput.ProviderName, err = promptText(termIO, "Provider name", "codex")
		if err != nil {
			return err
		}
		nextStep = "chord doctor models"
	case "1":
		fallthrough
	default:
		fmt.Fprintln(out)
		fmt.Fprintln(out, "config.yaml stores provider, model, proxy, and local behavior settings. auth.yaml stores API keys separately.")
		fmt.Fprintln(out, "Set API URL to an endpoint whose path ends in one of these suffixes:")
		fmt.Fprintln(out, "  - /responses         OpenAI Responses API and compatible gateways (example: https://api.openai.com/v1/responses)")
		fmt.Fprintln(out, "  - /messages          Anthropic Messages API and compatible gateways (example: https://api.anthropic.com/v1/messages)")
		fmt.Fprintln(out, "  - /chat/completions  OpenAI Chat Completions compatible gateways (example: https://gateway.example.com/v1/chat/completions)")
		fmt.Fprintln(out, "  - /models            Gemini Generate Content base path (example: https://generativelanguage.googleapis.com/v1beta/models)")
		fmt.Fprintln(out)
		defaultAPIURL := defaultAPIURLForProviderType(config.ProviderTypeResponses)
		cfgInput.APIURL, err = promptText(termIO, "API URL", defaultAPIURL)
		if err != nil {
			return err
		}
		defaults := initialSetupDefaultsForAPIURL(cfgInput.APIURL)
		cfgInput.ProviderType = defaults.ProviderType
		if cfgInput.ProviderType == "" {
			providerTypeChoice, choiceErr := promptChoice(termIO, "Provider type\n  1) responses\n  2) messages\n  3) chat-completions\n  4) generate-content\n\nType", []string{"1", "2", "3", "4"}, "1")
			if choiceErr != nil {
				return choiceErr
			}
			switch providerTypeChoice {
			case "2":
				cfgInput.ProviderType = config.ProviderTypeMessages
			case "3":
				cfgInput.ProviderType = config.ProviderTypeChatCompletions
			case "4":
				cfgInput.ProviderType = config.ProviderTypeGenerateContent
			default:
				cfgInput.ProviderType = config.ProviderTypeResponses
			}
			defaults = initialSetupDefaultsForProviderType(cfgInput.ProviderType)
		}
		cfgInput.Kind = initialSetupProviderAPIKey
		cfgInput.ProviderName, err = promptText(termIO, "Provider name", defaults.ProviderName)
		if err != nil {
			return err
		}
		cfgInput.ModelName, err = promptText(termIO, "Model", defaults.ModelName)
		if err != nil {
			return err
		}
		cfgInput.ContextLimit = defaults.ContextLimit
		cfgInput.InputLimit = defaults.InputLimit
		cfgInput.OutputLimit = defaults.OutputLimit
		switch cfgInput.ProviderType {
		case config.ProviderTypeChatCompletions:
			limitReminder = "This compatible gateway uses starter limit defaults for chat-completions. If your provider documents different limits, edit config.yaml after setup."
		case config.ProviderTypeMessages:
			if !strings.EqualFold(strings.TrimSpace(cfgInput.APIURL), "https://api.anthropic.com/v1/messages") {
				limitReminder = "This compatible gateway uses starter limit defaults for messages. If your provider documents different limits, edit config.yaml after setup."
			}
		case config.ProviderTypeGenerateContent:
			if !strings.EqualFold(strings.TrimSpace(cfgInput.APIURL), "https://generativelanguage.googleapis.com/v1beta/models") {
				limitReminder = "This compatible gateway uses starter limit defaults for generate-content. If your provider documents different limits, edit config.yaml after setup."
			}
		case config.ProviderTypeResponses:
			if !strings.EqualFold(strings.TrimSpace(cfgInput.APIURL), defaultAPIURL) {
				limitReminder = "This compatible gateway uses starter limit defaults for responses. If your provider documents different limits, edit config.yaml after setup."
			}
		}
		authProviderName = cfgInput.ProviderName
		authState, err := existingCredentialStateForProvider(authProviderName)
		if err != nil {
			return err
		}
		authPathShown = authState.AuthPath != ""
		if authState.HasCredentials {
			fmt.Fprintln(out)
			fmt.Fprintf(out, "Found existing credentials for provider %q in auth.yaml. Chord will reuse them unless you add another key now.\n", strings.TrimSpace(authProviderName))
			fmt.Fprintln(out)
		}
		credentialChoices := []string{"1", "2", "3", "4"}
		if !authState.HasCredentials {
			credentialChoices = []string{"1", "2", "3"}
		}
		credentialChoice, choiceErr := promptChoice(termIO, apiKeyCredentialPrompt(authState.HasCredentials, defaultAPIKeyEnvVar(cfgInput.ProviderName)), credentialChoices, apiKeyDefaultCredentialChoice(authState.HasCredentials))
		if choiceErr != nil {
			return choiceErr
		}
		switch credentialChoice {
		case "1":
			if authState.HasCredentials {
				// Reuse existing auth.yaml without changes.
			} else {
				authValue, authPathShown, err = promptAPIKeyCredential(termIO, cfgInput.ProviderName)
				if err != nil {
					return err
				}
			}
		case "2":
			if authState.HasCredentials {
				authValue, authPathShown, err = promptAPIKeyCredential(termIO, cfgInput.ProviderName)
				if err != nil {
					return err
				}
			} else {
				var envName string
				envName, err = promptText(termIO, "Environment variable name", defaultAPIKeyEnvVar(cfgInput.ProviderName))
				if err != nil {
					return err
				}
				envName = strings.TrimSpace(envName)
				if envName == "" {
					envName = defaultAPIKeyEnvVar(cfgInput.ProviderName)
				}
				authValue = "$" + envName
				authPathShown = true
				envVarReminder = envName
			}
		case "3":
			if authState.HasCredentials {
				var envName string
				envName, err = promptText(termIO, "Environment variable name", defaultAPIKeyEnvVar(cfgInput.ProviderName))
				if err != nil {
					return err
				}
				envName = strings.TrimSpace(envName)
				if envName == "" {
					envName = defaultAPIKeyEnvVar(cfgInput.ProviderName)
				}
				authValue = "$" + envName
				authPathShown = true
				envVarReminder = envName
			}
		case "4":
			// Skip for now.
		}
		nextStep = "chord doctor models"
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "If you need a proxy for model access, Chord can write it into config.yaml now. Leave it empty to keep proxy settings out of Chord config.")
	fmt.Fprintln(out, "Examples: http://127.0.0.1:1080 or socks5://127.0.0.1:1080")
	proxyEnabled, err := promptYesNo(termIO, "Do you need a proxy for model access? [y/N]: ", false)
	if err != nil {
		return err
	}
	if proxyEnabled {
		cfgInput.Proxy, err = promptText(termIO, "Proxy URL", "")
		if err != nil {
			return err
		}
	}

	if opts.ShouldPromptIME() {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "IME switching is helpful if you use a CJK input method and want Normal-mode shortcuts to stay reliable. Chord passes the target value through to im-select / im-select.exe.")
		defaultIMEYes := defaultIMEPromptEnabledFunc()
		prompt := "Configure IME switching for Normal mode? [y/N]: "
		if defaultIMEYes {
			prompt = "Configure IME switching for Normal mode? [Y/n]: "
		}
		if imeTarget, ok, err := promptOptionalText(termIO, prompt, "com.apple.keylayout.ABC", defaultIMEYes); err != nil {
			return err
		} else if ok {
			cfgInput.IMESwitchTarget = imeTarget
		}
	}

	if opts.ShouldPromptPreventSleep() {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "On macOS, prevent_sleep keeps the system awake while long-running agent work is active.")
		preventSleep, err := promptYesNo(termIO, "Enable prevent_sleep on macOS while agents are active? [Y/n]: ", true)
		if err != nil {
			return err
		}
		cfgInput.PreventSleep = &preventSleep
	}

	configData, err := buildInitialSetupConfigYAML(cfgInput)
	if err != nil {
		return err
	}
	var authRollback setupAuthRollback
	if authProviderName != "" && authValue != "" {
		authPath, err := config.AuthPath()
		if err != nil {
			return err
		}
		authRollback, err = snapshotSetupAuthFile(authPath)
		if err != nil {
			return fmt.Errorf("prepare auth.yaml rollback: %w", err)
		}
		defer func() {
			if authRollback.enabled && !authRollback.committed {
				_ = authRollback.restore()
			}
		}()
		if changed, err := config.UpsertAPIKeyCredentialInFile(authPath, authProviderName, authValue); err != nil {
			return fmt.Errorf("write auth.yaml: %w", err)
		} else if changed {
			authPathShown = true
		}
	}
	if err := writeInitialConfigFileFunc(configPath, configData); err != nil {
		return err
	}
	configWritten := true
	if cfgInput.Kind == initialSetupProviderCodex {
		providerCfg, globalProxy, err := resolveSetupCodexProvider(strings.TrimSpace(cfgInput.ProviderName), strings.TrimSpace(cfgInput.Proxy))
		if err != nil {
			if configWritten {
				_ = os.Remove(configPath)
			}
			return err
		}
		if err := runSetupCodexOAuthLoginFunc(ctx, termIO, strings.TrimSpace(cfgInput.ProviderName), providerCfg, globalProxy); err != nil {
			if configWritten {
				_ = os.Remove(configPath)
			}
			return err
		}
		authPathShown = true
	}
	authRollback.committed = true

	configHome, err := config.ConfigHomeDir()
	if err != nil {
		return err
	}
	finalConfigPath, err := config.ConfigPath()
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Setup complete.")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Config home:\n  %s\n\n", configHome)
	fmt.Fprintf(out, "Config:\n  %s\n", finalConfigPath)
	if authPathShown {
		authPath, err := config.AuthPath()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "\nAuth:\n  %s\n", authPath)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Edit config.yaml to change provider, model, proxy, IME switching, or prevent_sleep.")
	if authPathShown {
		fmt.Fprintln(out, "Edit auth.yaml to change API keys or OAuth credentials.")
	}
	if envVarReminder != "" {
		fmt.Fprintf(out, "Set %s in your shell profile or secret manager before starting Chord.\n", envVarReminder)
	}
	if limitReminder != "" {
		fmt.Fprintln(out, limitReminder)
	}
	if nextStep != "" {
		fmt.Fprintf(out, "\nNext step:\n  %s\n", nextStep)
	}
	return nil
}

func resolveSetupCodexProvider(providerName string, globalProxy string) (config.ProviderConfig, string, error) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		providerName = "codex"
	}
	providerCfg := config.ProviderConfig{Preset: config.ProviderPresetCodex}
	if strings.TrimSpace(globalProxy) != "" {
		proxy := strings.TrimSpace(globalProxy)
		providerCfg.Proxy = &proxy
	}
	normalized, meta, err := config.NormalizeProviderPreset(providerCfg)
	if err != nil {
		return config.ProviderConfig{}, "", fmt.Errorf("provider %q has invalid OAuth config: %w", providerName, err)
	}
	if !meta.Enabled {
		return config.ProviderConfig{}, "", fmt.Errorf("provider %q is not configured for preset: codex login; configure `preset: codex`", providerName)
	}
	return normalized, strings.TrimSpace(globalProxy), nil
}

func runSetupCodexOAuthLogin(ctx context.Context, termIO *setupTerminal, providerName string, providerCfg config.ProviderConfig, globalProxy string) error {
	in := termIO.in
	if in == nil {
		return initialSetupRequiredError()
	}
	out := termIO.out
	if out == nil {
		out = io.Discard
	}
	return runAuthLoginBrowserWithIO(in, out, providerName, providerCfg, globalProxy, ctx, openBrowser, clipboard.WriteAll)
}

func defaultShouldPromptIME() bool {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		return true
	default:
		return false
	}
}

func defaultIMEPromptEnabled() bool {
	binary := "im-select"
	if runtime.GOOS == "windows" {
		binary = "im-select.exe"
	}
	_, err := exec.LookPath(binary)
	return err == nil
}

func defaultShouldPromptPreventSleep() bool {
	return runtime.GOOS == "darwin"
}

func existingCredentialStateForProvider(providerName string) (*existingSetupCredentialState, error) {
	authPath, err := config.AuthPath()
	if err != nil {
		return nil, err
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		return nil, fmt.Errorf("load auth: %w", err)
	}
	providerName = strings.TrimSpace(providerName)
	hasCredentials := len(auth[providerName]) > 0
	return &existingSetupCredentialState{AuthPath: authPath, HasCredentials: hasCredentials}, nil
}

func apiKeyCredentialPrompt(hasExisting bool, envVar string) string {
	if hasExisting {
		return "How should Chord handle credentials for this provider?\n  1) Reuse existing auth.yaml credentials\n  2) Add another API key now (hidden; stored in auth.yaml)\n  3) Write environment variable placeholder to auth.yaml\n  4) Skip for now\n\nCredential option"
	}
	return fmt.Sprintf("How should Chord store the API key?\n  1) Paste key now (hidden; stored in auth.yaml)\n  2) Write environment variable placeholder ($%s)\n  3) Skip for now\n\nCredential option", envVar)
}

func apiKeyDefaultCredentialChoice(hasExisting bool) string {
	if hasExisting {
		return "1"
	}
	return "1"
}

func openSetupTerminal(opts SetupWizardOptions) (*setupTerminal, error) {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.ReadPassword == nil {
		opts.ReadPassword = func(fd int) ([]byte, error) { return term.ReadPassword(uintptr(fd)) }
	}

	inFile, inOK := opts.In.(*os.File)
	outFile, outOK := opts.Out.(*os.File)
	if inOK && outOK && term.IsTerminal(inFile.Fd()) && term.IsTerminal(outFile.Fd()) {
		return &setupTerminal{in: inFile, out: opts.Out, reader: bufio.NewReader(inFile), readPassword: opts.ReadPassword}, nil
	}
	if opts.OpenTTY == nil {
		return nil, fmt.Errorf("interactive terminal unavailable")
	}

	ttyIn, ttyOut, err := opts.OpenTTY()
	if err != nil {
		return nil, err
	}
	return &setupTerminal{
		in:           ttyIn,
		out:          ttyOut,
		reader:       bufio.NewReader(ttyIn),
		readPassword: opts.ReadPassword,
		closer: func() error {
			var firstErr error
			if ttyIn != nil {
				if err := ttyIn.Close(); err != nil {
					firstErr = err
				}
			}
			if ttyOut != nil {
				if err := ttyOut.Close(); err != nil && firstErr == nil {
					firstErr = err
				}
			}
			return firstErr
		},
	}, nil
}

func promptChoice(termIO *setupTerminal, prompt string, allowed []string, defaultChoice string) (string, error) {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, v := range allowed {
		allowedSet[v] = struct{}{}
	}
	for {
		value, err := promptLine(termIO, prompt+" ["+defaultChoice+"]: ")
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return defaultChoice, nil
		}
		if _, ok := allowedSet[value]; ok {
			return value, nil
		}
		fmt.Fprintln(termIO.out, "Please choose one of the listed options.")
	}
}

func snapshotSetupAuthFile(path string) (setupAuthRollback, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return setupAuthRollback{}, fmt.Errorf("auth path is empty")
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return setupAuthRollback{path: path, enabled: true}, nil
	case err != nil:
		return setupAuthRollback{}, err
	default:
		return setupAuthRollback{path: path, before: data, existed: true, enabled: true}, nil
	}
}

func (r setupAuthRollback) restore() error {
	if !r.enabled {
		return nil
	}
	if r.existed {
		if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
			return fmt.Errorf("restore auth config dir: %w", err)
		}
		if err := os.WriteFile(r.path, r.before, 0o600); err != nil {
			return fmt.Errorf("restore auth config: %w", err)
		}
		return nil
	}
	if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove auth config: %w", err)
	}
	return nil
}

func promptText(termIO *setupTerminal, label, defaultValue string) (string, error) {
	prompt := label
	if defaultValue != "" {
		prompt += " [" + defaultValue + "]"
	}
	prompt += ": "
	value, err := promptLine(termIO, prompt)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func promptOptionalText(termIO *setupTerminal, prompt string, defaultValue string, defaultYes bool) (string, bool, error) {
	value, err := promptYesNo(termIO, prompt, defaultYes)
	if err != nil {
		return "", false, err
	}
	if !value {
		return "", false, nil
	}
	text, err := promptText(termIO, "IME target", defaultValue)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(text) == "" {
		return "", false, nil
	}
	return text, true, nil
}

func promptYesNo(termIO *setupTerminal, prompt string, defaultYes bool) (bool, error) {
	defaultLabel := "N"
	if defaultYes {
		defaultLabel = "Y"
	}
	for {
		value, err := promptLine(termIO, prompt)
		if err != nil {
			return false, err
		}
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			return defaultYes, nil
		}
		switch value {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintf(termIO.out, "Please answer y or n (default %s).\n", defaultLabel)
		}
	}
}

func promptLine(termIO *setupTerminal, prompt string) (string, error) {
	if termIO == nil {
		return "", fmt.Errorf("setup terminal is nil")
	}
	if _, err := fmt.Fprint(termIO.out, prompt); err != nil {
		return "", err
	}
	line, err := termIO.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func promptAPIKeyCredential(termIO *setupTerminal, providerName string) (string, bool, error) {
	if termIO == nil {
		return "", false, fmt.Errorf("setup terminal is nil")
	}
	if _, err := fmt.Fprintf(termIO.out, "API key (%s): ", providerName); err != nil {
		return "", false, err
	}
	if termIO.in == nil {
		return "", false, fmt.Errorf("interactive terminal unavailable")
	}
	readPassword := termIO.readPassword
	if readPassword == nil {
		readPassword = func(fd int) ([]byte, error) { return term.ReadPassword(uintptr(fd)) }
	}
	secret, err := readPassword(int(termIO.in.Fd()))
	if err != nil {
		return "", false, err
	}
	value := strings.TrimSpace(string(secret))
	if value == "" {
		return "", false, fmt.Errorf("API key cannot be empty")
	}
	if _, err := fmt.Fprintln(termIO.out); err != nil {
		return "", false, err
	}
	return value, true, nil
}
