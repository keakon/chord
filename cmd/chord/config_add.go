package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
)

const configAddEndpointHint = "Use an http(s) endpoint ending in /responses, /messages or /chat/completions, or a Gemini version root ending in /v1beta or /v1alpha (the official Gemini endpoint also accepts /v1). Remove a trailing /models from Gemini URLs."

// configAddOptions controls offline model onboarding. Preset-bound models
// write pool references; other connections inherit same-protocol recipes
// through catalog references. Request compression remains an explicit choice.
// Interactive terminals can complete missing choices before any file is written.
type configAddOptions struct {
	url            string
	catalogID      string
	pool           string
	envVar         string
	keepCurrent    bool
	refreshCatalog bool
	noInteractive  bool
	compress       string
	variant        string
	terminal       *setupTerminal
	customize      bool
}

func newConfigAddCmd() *cobra.Command {
	opts := &configAddOptions{}
	cmd := &cobra.Command{
		Use:   "add [<provider>/<model>]",
		Short: "Add a model reference to config.yaml from the model catalog",
		Long: `Add a model reference to config.yaml and append it to a model pool.

Run without arguments to browse the verified providers and models and pick
one interactively, or add a known catalog ID directly:
  chord config add openai/gpt-6.1-sol

For a new provider, Chord fills the documented API URL and records its API
key environment variable. This also creates your first config.yaml. Export
the API key before starting Chord. Existing providers keep their URL and
credentials. Models without a documented connection still require --url.

The model resolves against the verified model catalog. A wire name bound to
the provider's preset needs nothing else: context, modalities, reasoning
variants and field send rules fill in at load. For custom endpoints, pass
--catalog <id> to inherit the same-protocol recipe of a catalog model
under your own wire name. An explicit --catalog selection takes priority over
the preset binding and any saved catalog choice.

In an interactive terminal, choose a suggested catalog model, fill missing
connection details and optionally configure the pool, reasoning variant and
request compression. Numbered choices and y/n respond immediately, without
Enter. Choose an existing pool or create one. Press m to enter a catalog ID.
Review the resolved configuration before saving. Press Esc/q to cancel menus
or enter q in text prompts. Suggestions never bind a model without your choice.
Pass --no-interactive for scripts. Redirected input or output also disables
prompts; unmatched names then show suggestions and return an error.

The command is offline by default. Pass --refresh-catalog to first pull the
latest tagged snapshot of the upstream model catalog repository; if that
network step fails, the command continues with the catalog already in
effect. The candidate config is resolved in full before anything is written,
and the file is only replaced when that resolution reports no errors.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.terminal = nil
			if !opts.noInteractive {
				in, inOK := cmd.InOrStdin().(*os.File)
				out, outOK := cmd.OutOrStdout().(*os.File)
				if inOK && outOK && term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd()) {
					terminal, err := openSetupTerminal(SetupWizardOptions{In: in, Out: out})
					if err != nil {
						return fmt.Errorf("open configuration terminal: %w", err)
					}
					opts.terminal = terminal
				}
			}
			ref := ""
			if len(args) > 0 {
				ref = args[0]
			}
			return runConfigAdd(cmd.Context(), cmd.OutOrStdout(), ref, *opts)
		},
	}
	cmd.Flags().StringVar(&opts.url, "url", "",
		"API URL when the provider does not exist yet (the path must end in a known protocol suffix)")
	cmd.Flags().StringVar(&opts.catalogID, "catalog", "",
		"catalog model ID to use instead of the automatic or saved binding")
	cmd.Flags().StringVar(&opts.pool, "pool", "default", "model pool to append the reference to")
	cmd.Flags().StringVar(&opts.envVar, "api-key-env", "",
		"write $VAR as the provider credential in auth.yaml when it has none")
	cmd.Flags().BoolVar(&opts.keepCurrent, "keep-current", false,
		"acknowledge freshness advisories for this model without changing anything")
	cmd.Flags().BoolVar(&opts.refreshCatalog, "refresh-catalog", false,
		"first pull the latest tagged catalog snapshot from the upstream repository (network)")
	cmd.Flags().BoolVar(&opts.noInteractive, "no-interactive", false, "disable guided choices and confirmation (for scripts)")
	cmd.Flags().StringVar(&opts.compress, "compress", "", "provider request compression: off, gzip or zstd (default: preserve existing setting)")
	cmd.Flags().StringVar(&opts.variant, "variant", "", "reasoning variant to use in the model pool")
	return cmd
}

func runConfigAdd(ctx context.Context, out io.Writer, ref string, opts configAddOptions) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if errors.Is(err, errConfigAddCancelled) {
			fmt.Fprintln(out, "Cancelled. No configuration was changed.")
			err = nil
		}
	}()
	ref = strings.TrimSpace(ref)
	providerName, wireModel := config.SplitProviderModelRef(ref)
	providerName, wireModel = strings.TrimSpace(providerName), strings.TrimSpace(wireModel)
	if ref == "" {
		if opts.keepCurrent {
			return fmt.Errorf("--keep-current needs an explicit <provider>/<model> reference")
		}
	} else {
		if providerName == "" || wireModel == "" {
			return fmt.Errorf("model reference must be <provider>/<model>")
		}
		if opts.keepCurrent {
			if err := config.RecordCatalogAdvisoryAcknowledgment(providerName, wireModel); err != nil {
				return err
			}
			fmt.Fprintf(out, "Acknowledged freshness advisories for %s/%s under catalog version %s.\n",
				providerName, wireModel, modelcatalog.Version())
			return nil
		}
		if strings.Contains(wireModel, "@") {
			return fmt.Errorf("wire model names containing %q collide with the @variant reference syntax; rename the model entry", "@")
		}
	}

	globalPath, err := config.ConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	if opts.refreshCatalog {
		// An explicit user opt-in to network access. A failure keeps the
		// catalog already in effect — the add itself stays fully usable.
		if err := refreshCatalogForCommand(ctx, out); err != nil {
			fmt.Fprintf(out, "Catalog refresh failed; continuing with the catalog already in effect: %v\n", err)
		}
	}

	rc, err := config.LoadResolvedConfig(globalPath, "")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if ref == "" {
		if opts.terminal == nil {
			return fmt.Errorf("pass <provider>/<model> to add a model, for example `chord config add openai/gpt-6.1-sol`; browsing the catalog needs an interactive terminal")
		}
		providerName, wireModel, err = browseConfigAdd(opts.terminal, rc.Config)
		if err != nil {
			return err
		}
		ref = providerName + "/" + wireModel
	}
	providerCfg, providerExists := rc.Config.Providers[providerName]
	providerCfg, wireModel, opts, err = prepareCatalogAdd(ref, providerCfg, providerExists, wireModel, opts)
	if err != nil && !(opts.terminal != nil && errors.Is(err, errCatalogConnectionRequired)) {
		return err
	}
	if opts.terminal != nil {
		opts, err = guideConfigAdd(opts, providerName, wireModel, providerCfg, providerExists, rc.Config.ModelPools)
		if err != nil {
			return err
		}
	}
	if opts.envVar != "" && !validConfigAddEnvName(opts.envVar) {
		return fmt.Errorf("invalid API key environment variable name %q", opts.envVar)
	}
	if providerExists && strings.TrimSpace(opts.url) != "" &&
		!strings.EqualFold(strings.TrimSpace(opts.url), strings.TrimSpace(providerCfg.APIURL)) {
		return fmt.Errorf("provider %q already points at %s; --url %s would change its endpoint", providerName, providerCfg.APIURL, opts.url)
	}
	if !providerExists && strings.TrimSpace(opts.url) == "" {
		return fmt.Errorf("provider %q is not configured; pass --url to create it", providerName)
	}

	preset := strings.ToLower(strings.TrimSpace(providerCfg.Preset))
	mode, borrowID, err := resolveConfigAddMode(preset, wireModel, opts.catalogID)
	if err != nil {
		if errors.Is(err, errNoCatalogMatch) {
			apiURL := strings.TrimSpace(opts.url)
			if providerExists {
				apiURL = strings.TrimSpace(providerCfg.APIURL)
			}
			if !printCatalogSuggestions(out, wireModel, preset, apiURL) {
				fmt.Fprintln(out, "No close catalog match for this wire name; configure the model manually or refresh the catalog with `chord config refresh-catalog`.")
			}
		}
		return err
	}

	poolName := strings.TrimSpace(opts.pool)
	if poolName == "" {
		poolName = "default"
	}
	poolRef := providerName + "/" + wireModel
	if opts.variant != "" {
		poolRef += "@" + opts.variant
	}
	if opts.compress != "" && opts.compress != "off" && opts.compress != config.RequestCompressionGzip && opts.compress != config.RequestCompressionZstd {
		return fmt.Errorf("invalid request compression %q; choose off, gzip or zstd", opts.compress)
	}

	edit := configAddEdit{
		providerName: providerName,
		wireModel:    wireModel,
		borrowID:     borrowID,
		poolName:     poolName,
		poolRef:      poolRef,
		compress:     opts.compress,
		providerType: providerCfg.Type,
		apiURL:       providerCfg.APIURL,
		preset:       providerCfg.Preset,
	}
	if !providerExists {
		edit.providerNew = true
		edit.providerType = strings.TrimSpace(config.InferProviderTypeFromAPIURL(strings.TrimSpace(opts.url)))
		edit.apiURL = strings.TrimSpace(opts.url)
		edit.preset = providerCfg.Preset
		if edit.providerType == "" {
			return fmt.Errorf("cannot infer a provider type from %q; %s", opts.url, configAddEndpointHint)
		}
	}
	if opts.customize {
		if err := configureConfigAddVariant(opts.terminal, globalPath, &edit); err != nil {
			return err
		}
		poolRef = edit.poolRef
	}

	// Parse credentials before writing config.yaml, including in scripts.
	// The write rechecks under its own lock; later I/O failures are reported
	// as partial success with credential recovery instructions.
	if opts.envVar != "" {
		authPath, err := config.AuthPath()
		if err != nil {
			return fmt.Errorf("resolve auth path: %w", err)
		}
		if _, err := config.LoadAuthConfig(authPath); err != nil {
			return fmt.Errorf("read credentials before saving config: %w", err)
		}
	}
	produce := func(current []byte, showPreview bool) ([]byte, error) {
		edited, err := editConfigYAMLForAdd(current, edit)
		if err != nil {
			return nil, err
		}
		if err := validateCandidateConfig(out, edited, edit, showPreview); err != nil {
			return nil, err
		}
		return edited, nil
	}
	if opts.terminal != nil {
		current, readErr := os.ReadFile(globalPath)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("read config preview: %w", readErr)
		}
		if _, err := produce(current, true); err != nil {
			return err
		}
		if err := confirmConfigAdd(opts.terminal, providerName, wireModel, providerCfg, edit, opts.envVar); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := config.CreateOrUpdateConfigFileLocked(globalPath, func(current []byte) ([]byte, error) {
		return produce(current, opts.terminal == nil)
	}); err != nil {
		return fmt.Errorf("update %s: %w", globalPath, err)
	}
	printConfigAddSummary(out, edit, mode, poolName, poolRef)
	if providerCfg.Preset == "codex" {
		fmt.Fprintf(out, "Sign in with: chord auth %s\n", providerName)
	}

	if opts.envVar != "" {
		if err := addProviderEnvCredential(providerName, strings.TrimSpace(opts.envVar), out); err != nil {
			return fmt.Errorf("configuration saved to %s, but credentials were not saved; after resolving the error, set provider %q to $%s in auth.yaml: %w", globalPath, providerName, strings.TrimSpace(opts.envVar), err)
		}
	}
	return nil
}

type configAddMode string

const (
	configAddExact  configAddMode = "exact"
	configAddBorrow configAddMode = "borrow"
)

// errNoCatalogMatch marks the unmatched-wire-name failure, the only case
// where printing the suggestion list is helpful.
var errNoCatalogMatch = errors.New("no catalog match")

// resolveConfigAddMode decides how the wire name maps onto the catalog.
// Nothing here writes: an unmatched name requires an explicit catalog choice,
// supplied either by a flag or by the interactive guide.
func resolveConfigAddMode(preset, wireModel, catalogID string) (configAddMode, string, error) {
	if strings.TrimSpace(catalogID) == "" {
		if preset != "" {
			if _, ok := modelcatalog.LookupBinding(preset, wireModel); ok {
				return configAddExact, "", nil
			}
		}
		return "", "", fmt.Errorf("%w: model %q is not in the verified catalog for this endpoint", errNoCatalogMatch, wireModel)
	}
	borrowID := strings.TrimSpace(catalogID)
	if _, ok := modelcatalog.Model(borrowID); !ok {
		return "", "", fmt.Errorf("catalog model %q does not exist; run `chord config add` without arguments to browse the verified catalog, or see https://github.com/keakon/chord-models", borrowID)
	}
	if preset != "" {
		if _, bound := modelcatalog.LookupBindingByModelID(preset, borrowID); !bound {
			return "", "", fmt.Errorf("catalog model %q is not bound to preset %q; keep the preset off this provider or borrow a model bound to it", borrowID, preset)
		}
	}
	return configAddBorrow, borrowID, nil
}

// printCatalogSuggestions renders the ranked verified suggestions followed by
// refreshed candidate entries, and reports whether anything was shown.
func printCatalogSuggestions(out io.Writer, wireModel, preset, apiURL string) bool {
	suggestions := modelcatalog.SuggestModels(wireModel, 5)
	candidates := catalogCandidateOrder(modelcatalog.SuggestCandidates(wireModel, 5), preset, apiURL)
	if len(suggestions) == 0 && len(candidates) == 0 {
		return false
	}
	if len(suggestions) > 0 {
		fmt.Fprintf(out, "%q is not in the verified catalog for this endpoint. Closest verified models:\n", wireModel)
		for i, s := range suggestions {
			fmt.Fprintf(out, "  %d) %s (context %d / output %d)\n", i+1, s.ModelID, s.Facts.Context, s.Facts.Output)
		}
	}
	if len(candidates) > 0 {
		if len(suggestions) > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, "Refreshed catalog candidates (discovery entries with their sources; never defaults):")
		for i, c := range candidates {
			scope := fmt.Sprintf("scope %q", c.Candidate.Scope)
			if catalogScopeMatches(c.Candidate.Scope, preset, apiURL) {
				scope += " — matches this endpoint"
			} else {
				scope += " — different provider; reference values, your endpoint may differ"
			}
			fmt.Fprintf(out, "  %d) %s on %s\n", i+1, c.Candidate.WireModelID, scope)
			if c.Candidate.ModelID != "" {
				fmt.Fprintf(out, "     refers to verified model %s\n", c.Candidate.ModelID)
			}
			if observed := candidateObservedFacts(c.Candidate); observed != "" {
				fmt.Fprintf(out, "     observed: %s\n", observed)
			}
			for _, s := range c.Candidate.Sources {
				fmt.Fprintf(out, "     source: %s (checked %s)\n", s.URL, s.Checked)
			}
		}
	}
	if example := suggestionExampleID(suggestions, candidates); example != "" {
		fmt.Fprintln(out, "Adopt explicitly, e.g.:")
		fmt.Fprintf(out, "  chord config add <provider>/<model> --catalog %s\n", example)
	} else {
		fmt.Fprintln(out, "Candidates are reference values; configure the model manually in config.yaml if one fits.")
	}
	return true
}

// suggestionExampleID picks the model ID the adoption example shows: the
// verified model behind the top candidate when there is one — the closest
// sighting of the typed name — otherwise the top verified suggestion.
func suggestionExampleID(suggestions []modelcatalog.Suggestion, candidates []modelcatalog.CandidateSuggestion) string {
	for _, c := range candidates {
		if c.Candidate.ModelID != "" {
			return c.Candidate.ModelID
		}
	}
	if len(suggestions) > 0 {
		return suggestions[0].ModelID
	}
	return ""
}

// candidateObservedFacts renders the observed reference values a candidate
// carries; unobserved values stay absent rather than reading as defaults.
func candidateObservedFacts(c modelcatalog.Candidate) string {
	var parts []string
	if c.Context > 0 {
		parts = append(parts, fmt.Sprintf("context %d", c.Context))
	}
	if c.Input > 0 {
		parts = append(parts, fmt.Sprintf("input %d", c.Input))
	}
	if c.Output > 0 {
		parts = append(parts, fmt.Sprintf("output %d", c.Output))
	}
	if len(c.InputModalities) > 0 {
		parts = append(parts, "modalities "+strings.Join(c.InputModalities, ","))
	}
	return strings.Join(parts, ", ")
}

// catalogCandidateOrder lists scope-matched candidates first, keeping the
// ranked order within each group: the endpoint match is a presentation
// preference, the ranking itself stays the engine's.
func catalogCandidateOrder(candidates []modelcatalog.CandidateSuggestion, preset, apiURL string) []modelcatalog.CandidateSuggestion {
	if len(candidates) == 0 {
		return nil
	}
	matched := make([]modelcatalog.CandidateSuggestion, 0, len(candidates))
	for _, c := range candidates {
		if catalogScopeMatches(c.Candidate.Scope, preset, apiURL) {
			matched = append(matched, c)
		}
	}
	if len(matched) == 0 || len(matched) == len(candidates) {
		return candidates
	}
	rest := make([]modelcatalog.CandidateSuggestion, 0, len(candidates)-len(matched))
	for _, c := range candidates {
		if !catalogScopeMatches(c.Candidate.Scope, preset, apiURL) {
			rest = append(rest, c)
		}
	}
	return append(matched, rest...)
}

// catalogScopeMatches reports whether a candidate's provider scope plausibly
// describes the endpoint being configured: the provider's preset, or a
// distinctive name inside the API URL host. It only chooses display order and
// wording; it never decides what gets configured.
func catalogScopeMatches(scope, preset, apiURL string) bool {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		return false
	}
	if preset != "" && scope == strings.ToLower(strings.TrimSpace(preset)) {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return host != "" && strings.Contains(host, scope)
}

func addProviderEnvCredential(providerName, envVar string, out io.Writer) error {
	authPath, err := config.AuthPath()
	if err != nil {
		return fmt.Errorf("resolve auth path: %w", err)
	}
	changed, err := config.AddAPIKeyCredentialIfUndeclared(authPath, providerName, "$"+envVar)
	if err != nil {
		return fmt.Errorf("write auth.yaml: %w", err)
	}
	if !changed {
		fmt.Fprintf(out, "Provider %q already declares credentials in auth.yaml; --api-key-env ignored.\n", providerName)
		return nil
	}
	fmt.Fprintf(out, "Wrote $%s for provider %q in %s.\n", envVar, providerName, authPath)
	fmt.Fprintf(out, "Set the key before starting Chord: export %s=\"your-api-key\"\n", envVar)
	return nil
}

// loadConfigAddCandidate resolves edited bytes without replacing config.yaml.
func loadConfigAddCandidate(edited []byte) (*config.ResolvedConfig, error) {
	tmp, err := os.CreateTemp("", "chord-config-add-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("create candidate temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(edited); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write candidate temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("write candidate temp file: %w", err)
	}
	candidate, err := config.LoadResolvedConfig(tmpPath, "")
	if err != nil {
		return nil, fmt.Errorf("candidate config does not resolve: %w", err)
	}
	return candidate, nil
}

func validateCandidateConfig(out io.Writer, edited []byte, edit configAddEdit, showPreview bool) error {
	candidate, err := loadConfigAddCandidate(edited)
	if err != nil {
		return err
	}
	if hasConfigErrors(candidate.Diagnostics) {
		fmt.Fprintln(out, "The candidate config has errors; nothing was written:")
		for _, diagnostic := range candidate.Diagnostics {
			if diagnostic.Severity == config.DiagnosticSeverityError {
				fmt.Fprintf(out, "  - %s: %s\n", diagnostic.Path, diagnostic.Message)
			}
		}
		return fmt.Errorf("candidate config resolution failed")
	}
	provider, exists := candidate.Config.Providers[edit.providerName]
	if !exists {
		return fmt.Errorf("candidate config discarded provider %q", edit.providerName)
	}
	if provider.APIURL != edit.apiURL || provider.Type != edit.providerType || provider.Preset != edit.preset {
		return fmt.Errorf("provider %q endpoint changed during this edit; run the command again to review its current endpoint", edit.providerName)
	}
	model, exists := provider.Models[edit.wireModel]
	if !exists || (edit.borrowID != "" && (model.Catalog == nil || model.Catalog.ID != edit.borrowID)) {
		return fmt.Errorf("candidate config discarded model %q", edit.poolRef)
	}
	if !slices.Contains(candidate.Config.ModelPools[edit.poolName], edit.poolRef) {
		return fmt.Errorf("candidate config discarded pool reference %q", edit.poolRef)
	}
	for _, diagnostic := range candidate.Diagnostics {
		if diagnostic.Severity == config.DiagnosticSeverityWarning {
			fmt.Fprintf(out, "Warning: %s\n", redactConfigDiagnostic(diagnostic).String())
		}
	}
	if showPreview {
		printConfigAddPreview(out, candidate, edit.providerName, edit.wireModel)
	}
	return nil
}

func printConfigAddPreview(out io.Writer, candidate *config.ResolvedConfig, providerName, wireModel string) {
	provider, ok := candidate.Config.Providers[providerName]
	if !ok {
		return
	}
	model, ok := provider.Models[wireModel]
	if !ok {
		return
	}
	fmt.Fprintln(out, "Resolved model facts:")
	fmt.Fprintf(out, "  context: %d\n", model.Limit.Context)
	if model.Limit.Input > 0 {
		fmt.Fprintf(out, "  input: %d\n", model.Limit.Input)
	}
	fmt.Fprintf(out, "  output: %d\n", model.Limit.Output)
	if model.Modalities != nil && len(model.Modalities.Input) > 0 {
		fmt.Fprintf(out, "  input modalities: %s\n", strings.Join(model.Modalities.Input, ", "))
	}
}

func printConfigAddSummary(out io.Writer, edit configAddEdit, mode configAddMode, poolName, poolRef string) {
	fmt.Fprintln(out, "Updated config:")
	if edit.providerNew {
		fmt.Fprintf(out, "  providers.%s: new provider (type %s, api_url %s)\n", edit.providerName, edit.providerType, edit.apiURL)
	}
	switch mode {
	case configAddExact:
		fmt.Fprintf(out, "  %s: verified preset binding; facts fill in at load, no model entry written\n", poolRef)
	case configAddBorrow:
		fmt.Fprintf(out, "  providers.%s.models.%s.catalog: %s (inherits model recipe for the same protocol)\n", edit.providerName, edit.wireModel, edit.borrowID)
	}
	fmt.Fprintf(out, "  model_pools.%s: append %s\n", poolName, poolRef)
}

type configAddEdit struct {
	providerName string
	wireModel    string
	providerNew  bool
	providerType string
	apiURL       string
	preset       string
	borrowID     string
	poolName     string
	poolRef      string
	compress     string // empty preserves the provider setting; "off" explicitly disables it
}

// editConfigYAMLForAdd applies the add edit to the raw config bytes with a
// yaml.Node round-trip, which preserves comments and ordering of everything
// it does not touch. Shared YAML values are copied before their selected
// path changes, leaving other alias and merge users semantically unchanged.
func editConfigYAMLForAdd(current []byte, e configAddEdit) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(current, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	editor := configAddYAML{doc: &doc}
	root, err := documentRootMapping(&doc)
	if err != nil {
		return nil, err
	}
	providersNode, err := editor.child(root, "providers", yaml.MappingNode, "!!map")
	if err != nil {
		return nil, err
	}
	existingProvider, err := effectiveConfigValue(providersNode, e.providerName)
	if err != nil {
		return nil, err
	}
	if e.providerNew && existingProvider != nil {
		return nil, fmt.Errorf("provider %q was created during this edit; run the command again to use its current endpoint", e.providerName)
	}
	providerNode, err := editor.child(providersNode, e.providerName, yaml.MappingNode, "!!map")
	if err != nil {
		return nil, err
	}
	if e.providerNew {
		for _, field := range []struct{ key, value string }{{"preset", e.preset}, {"type", e.providerType}, {"api_url", e.apiURL}} {
			if field.value != "" {
				if err := editor.scalar(providerNode, field.key, field.value); err != nil {
					return nil, err
				}
			}
		}
	}
	if e.compress != "" {
		value := e.compress
		if value == "off" {
			value = ""
		}
		if err := editor.scalar(providerNode, "compress", value); err != nil {
			return nil, err
		}
	}
	if e.borrowID != "" {
		modelsNode, err := editor.child(providerNode, "models", yaml.MappingNode, "!!map")
		if err != nil {
			return nil, err
		}
		modelNode, err := editor.child(modelsNode, e.wireModel, yaml.MappingNode, "!!map")
		if err != nil {
			return nil, err
		}
		if err := editor.scalar(modelNode, "catalog", e.borrowID); err != nil {
			return nil, err
		}
	}
	poolsNode, err := editor.child(root, "model_pools", yaml.MappingNode, "!!map")
	if err != nil {
		return nil, err
	}
	poolNode, err := editor.child(poolsNode, e.poolName, yaml.SequenceNode, "!!seq")
	if err != nil {
		return nil, err
	}
	appendSequenceValue(poolNode, e.poolRef)

	var edited bytes.Buffer
	enc := yaml.NewEncoder(&edited)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return edited.Bytes(), nil
}

// documentRootMapping returns the document's root mapping, building an empty
// one for a blank file.
func documentRootMapping(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind == 0 || (doc.Kind == yaml.DocumentNode && len(doc.Content) == 0) {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	if doc.Kind != yaml.DocumentNode {
		return nil, fmt.Errorf("config root must be a YAML document")
	}
	root := doc.Content[0]
	if root.Tag == "!!null" {
		*root = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: root.HeadComment, LineComment: root.LineComment}
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config root must be a mapping")
	}
	return root, nil
}

func appendSequenceValue(seq *yaml.Node, value string) bool {
	for _, item := range seq.Content {
		if item.Kind == yaml.AliasNode && item.Alias != nil {
			item = item.Alias
		}
		if item.Value == value {
			return false
		}
	}
	seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	return true
}
