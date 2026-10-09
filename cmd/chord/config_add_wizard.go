package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
)

var errConfigAddCancelled = errors.New("model configuration cancelled")

// configAddPrompt shares the setup terminal reader and treats EOF and q as
// cancellation, so an interrupted wizard cannot silently accept defaults.
func configAddPrompt(terminal *setupTerminal, label, defaultValue string) (string, error) {
	value, err := promptText(terminal, label, defaultValue)
	if errors.Is(err, io.EOF) || strings.EqualFold(value, "q") || strings.EqualFold(value, "cancel") {
		return "", errConfigAddCancelled
	}
	return value, err
}

func guideConfigAdd(opts configAddOptions, providerName, wireModel string, provider config.ProviderConfig, exists bool, pools map[string][]string) (configAddOptions, error) {
	t := opts.terminal
	fmt.Fprintln(t.out, "Add model — press Esc/q in menus, or enter q in text prompts, to cancel.")
	preset := strings.ToLower(strings.TrimSpace(provider.Preset))
	if _, _, err := resolveConfigAddMode(preset, wireModel, opts.catalogID); errors.Is(err, errNoCatalogMatch) {
		id, err := chooseCatalogForAdd(t, wireModel, preset)
		if err != nil {
			return opts, err
		}
		opts.catalogID = id
	} else if err != nil {
		return opts, err
	}
	if exists {
		fmt.Fprintf(t.out, "Using provider %q: %s (existing credentials and settings are preserved).\n", providerName, redactConfigURL(provider.APIURL))
	} else if strings.TrimSpace(opts.url) == "" {
		fmt.Fprintln(t.out, "Enter the full API endpoint, e.g. https://gateway.example/v1/responses or /v1/chat/completions.")
		for {
			value, err := configAddPrompt(t, "API URL", "")
			if err != nil {
				return opts, err
			}
			u, err := url.Parse(value)
			if err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") && config.InferProviderTypeFromAPIURL(value) != "" {
				opts.url = value
				break
			}
			fmt.Fprintln(t.out, "Use an http(s) endpoint ending in /responses, /messages, /chat/completions or /models.")
		}
	}

	authPath, err := config.AuthPath()
	if err != nil {
		return opts, err
	}
	declarations, err := config.LoadCredentialDeclarations(authPath)
	if err != nil {
		return opts, fmt.Errorf("load credential declarations: %w", err)
	}
	if declarations.Declared(providerName) {
		opts.envVar = ""
	} else if provider.Preset != "codex" && opts.envVar == "" {
		fmt.Fprintln(t.out, "Use an environment variable for the API key; enter - to configure credentials later.")
		for {
			value, err := configAddPrompt(t, "API key environment variable", defaultAPIKeyEnvVar(providerName))
			if err != nil {
				return opts, err
			}
			if value == "-" {
				break
			}
			if validConfigAddEnvName(value) {
				opts.envVar = value
				break
			}
			fmt.Fprintln(t.out, "Enter a variable name such as GATEWAY_API_KEY, without $ or an API key value.")
		}
	}
	opts.customize, err = configAddYesNo(t, "Configure pool, reasoning variant and request compression?", false)
	if err != nil {
		return opts, err
	}
	if !opts.customize {
		return opts, nil
	}
	pool := opts.pool
	if pool == "" {
		pool = "default"
	}
	opts.pool, err = chooseConfigAddPool(t, pools, pool)
	if err != nil {
		return opts, err
	}
	compression := opts.compress
	if compression == "" {
		compression = provider.Compress
	}
	if compression == "" {
		compression = "off"
	}
	fmt.Fprintln(t.out, "Request compression applies to every model on this provider; enable it only if the endpoint supports it.")
	value, err := configAddMenu(t, "Request compression", []string{"off", config.RequestCompressionGzip, config.RequestCompressionZstd}, compression, "")
	if err != nil {
		return opts, err
	}
	if value != compression || opts.compress != "" {
		opts.compress = value
	}
	return opts, nil
}

func chooseConfigAddPool(t *setupTerminal, pools map[string][]string, defaultPool string) (string, error) {
	names := make([]string, 0, len(pools))
	for name := range pools {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return configAddPrompt(t, "New model pool", defaultPool)
	}
	for {
		name, err := configAddMenu(t, "Model pools", names, defaultPool, "Create a new model pool")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(name) != "" {
			return name, nil
		}
		fmt.Fprintln(t.out, "A model pool needs a name.")
	}
}

func chooseCatalogForAdd(t *setupTerminal, wireModel, preset string) (string, error) {
	suggestions := modelcatalog.SuggestModels(wireModel, 5)
	suggestions = slices.DeleteFunc(suggestions, func(s modelcatalog.Suggestion) bool {
		_, _, err := resolveConfigAddMode(preset, wireModel, s.ModelID)
		return err != nil
	})
	fmt.Fprintf(t.out, "Choose the catalog model behind %q (your request model name stays unchanged):\n", wireModel)
	labels := make([]string, 0, len(suggestions))
	for _, s := range suggestions {
		labels = append(labels, fmt.Sprintf("%s (context %d / output %d)", s.ModelID, s.Facts.Context, s.Facts.Output))
	}
	fmt.Fprintln(t.out, "No model is selected automatically.")
	for {
		value, err := configAddMenu(t, "Catalog model", labels, "", "Enter a verified catalog ID")
		if err != nil {
			return "", err
		}
		if index := slices.Index(labels, value); index >= 0 {
			value = suggestions[index].ModelID
		}
		if _, _, err := resolveConfigAddMode(preset, wireModel, value); err == nil {
			return value, nil
		}
		fmt.Fprintln(t.out, "Choose a verified catalog ID compatible with this provider's preset.")
	}
}

func validConfigAddEnvName(value string) bool {
	for i, c := range []byte(value) {
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return value != ""
}

func configureConfigAddVariant(t *setupTerminal, path string, edit *configAddEdit) error {
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read config: %w", err)
	}
	// Resolve the base model first; an optional variant must come from the
	// actual inherited recipe, including any explicit user overrides.
	baseEdit := *edit
	baseEdit.poolRef = edit.providerName + "/" + edit.wireModel
	edited, err := editConfigYAMLForAdd(current, baseEdit)
	if err != nil {
		return err
	}
	resolved, err := loadConfigAddCandidate(edited)
	if err != nil {
		return err
	}
	variants := resolved.Config.Providers[edit.providerName].Models[edit.wireModel].Variants
	if len(variants) == 0 {
		fmt.Fprintln(t.out, "This model has no configured reasoning variants; using its default.")
		return nil
	}
	names := make([]string, 0, len(variants))
	for name := range variants {
		names = append(names, name)
	}
	slices.Sort(names)
	names = append([]string{"default"}, names...)
	defaultVariant := "default"
	if _, variant, ok := strings.Cut(edit.poolRef, "@"); ok {
		defaultVariant = variant
	}
	for {
		value, err := configAddMenu(t, "Available reasoning variants (default uses model defaults)", names, defaultVariant, "Enter a reasoning variant")
		if err != nil {
			return err
		}
		if value == "default" {
			edit.poolRef = baseEdit.poolRef
			return nil
		}
		if _, ok := variants[value]; ok {
			edit.poolRef = baseEdit.poolRef + "@" + value
			return nil
		}
		fmt.Fprintln(t.out, "Choose one of the available variants or default.")
	}
}

func confirmConfigAdd(t *setupTerminal, providerName, wireModel string, provider config.ProviderConfig, edit configAddEdit, envVar string) error {
	apiURL := provider.APIURL
	if edit.providerNew {
		apiURL = edit.apiURL
	}
	compression := provider.Compress
	if edit.compress != "" {
		compression = edit.compress
	}
	if compression == "" {
		compression = "off"
	}
	fmt.Fprintf(t.out, "Configuration to save:\n  model: %s/%s\n  API URL: %s\n  pool: %s -> %s\n  request compression: %s\n", providerName, wireModel, redactConfigURL(apiURL), edit.poolName, edit.poolRef, compression)
	if edit.borrowID != "" {
		fmt.Fprintf(t.out, "  catalog recipe: %s (explicit settings take priority over catalog defaults)\n", edit.borrowID)
	}
	if envVar != "" {
		fmt.Fprintf(t.out, "  credential: $%s\n", envVar)
	}
	save, err := configAddYesNo(t, "Save configuration?", true)
	if err != nil {
		return err
	}
	if !save {
		return errConfigAddCancelled
	}
	return nil
}
