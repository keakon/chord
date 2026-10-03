package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
)

// configAddCommand is the first write slice for daily model onboarding. It is
// fully offline, keeps the built-in catalog as the source of verified facts,
// and writes only selections and explicit user values: a pool reference for
// preset-bound models, a `catalog:` borrow for custom endpoints, never
// materialized defaults. Non-interactive by design (the first slice per the
// catalog plan); the interactive selector arrives with the editor work.
type configAddOptions struct {
	url         string
	catalogID   string
	pool        string
	envVar      string
	keepCurrent bool
}

func newConfigAddCmd() *cobra.Command {
	opts := &configAddOptions{}
	cmd := &cobra.Command{
		Use:   "add <provider>/<model>",
		Short: "Add a model reference to config.yaml from the built-in catalog",
		Long: `Add a model reference to config.yaml and append it to a model pool.

The model resolves against the built-in verified catalog. A wire name bound to
the provider's preset needs nothing else: context, modalities, reasoning
variants and field send rules fill in at load. For custom endpoints, pass
--catalog <id> to borrow the protocol-independent facts of a catalog model
under your own wire name.

When the wire name matches nothing, the closest verified models are listed;
adopting one is always an explicit --catalog choice, never automatic.

The command is fully offline. The candidate config is resolved in full before
anything is written, and the file is only replaced when that resolution
reports no errors.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigAdd(cmd.OutOrStdout(), args[0], *opts)
		},
	}
	cmd.Flags().StringVar(&opts.url, "url", "",
		"API URL when the provider does not exist yet (the path must end in a known protocol suffix)")
	cmd.Flags().StringVar(&opts.catalogID, "catalog", "",
		"catalog model ID to borrow facts from (custom endpoints)")
	cmd.Flags().StringVar(&opts.pool, "pool", "default", "model pool to append the reference to")
	cmd.Flags().StringVar(&opts.envVar, "api-key-env", "",
		"write $VAR as the provider credential in auth.yaml when it has none")
	cmd.Flags().BoolVar(&opts.keepCurrent, "keep-current", false,
		"acknowledge freshness advisories for this model without changing anything")
	return cmd
}

func runConfigAdd(out io.Writer, ref string, opts configAddOptions) error {
	providerName, wireModel := config.SplitProviderModelRef(strings.TrimSpace(ref))
	providerName, wireModel = strings.TrimSpace(providerName), strings.TrimSpace(wireModel)
	if providerName == "" || wireModel == "" {
		return fmt.Errorf("model reference must be <provider>/<model>")
	}
	if strings.Contains(wireModel, "@") {
		return fmt.Errorf("wire model names containing %q collide with the @variant reference syntax; rename the model entry", "@")
	}

	if opts.keepCurrent {
		if err := config.RecordCatalogAdvisoryAcknowledgment(providerName, wireModel); err != nil {
			return err
		}
		fmt.Fprintf(out, "Acknowledged freshness advisories for %s/%s under catalog version %s.\n",
			providerName, wireModel, modelcatalog.Version())
		return nil
	}

	globalPath, err := config.ConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	if _, statErr := os.Stat(globalPath); errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("no config.yaml at %s; run `chord` once in an interactive terminal to complete initial setup", globalPath)
	}

	rc, err := config.LoadResolvedConfig(globalPath, "")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	providerCfg, providerExists := rc.Config.Providers[providerName]
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
			if !printCatalogSuggestions(out, wireModel) {
				fmt.Fprintln(out, "No close catalog match for this wire name; configure the model manually or refresh the catalog in a future release.")
			}
		}
		return err
	}

	poolName := strings.TrimSpace(opts.pool)
	if poolName == "" {
		poolName = "default"
	}
	poolRef := providerName + "/" + wireModel

	edit := configAddEdit{
		providerName: providerName,
		wireModel:    wireModel,
		borrowID:     borrowID,
		poolName:     poolName,
		poolRef:      poolRef,
	}
	if !providerExists {
		edit.providerNew = true
		edit.providerType = strings.TrimSpace(config.InferProviderTypeFromAPIURL(strings.TrimSpace(opts.url)))
		edit.apiURL = strings.TrimSpace(opts.url)
		if edit.providerType == "" {
			return fmt.Errorf("cannot infer a provider type from %q; use a URL ending in /responses, /messages, /chat/completions or /models", opts.url)
		}
	}

	produce := func(current []byte) ([]byte, error) {
		return editConfigYAMLForAdd(current, edit)
	}

	// Validate the candidate exactly as it will exist on disk: run the real
	// resolver over the edited bytes before touching the live file.
	currentBytes, err := os.ReadFile(globalPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", globalPath, err)
	}
	edited, err := produce(currentBytes)
	if err != nil {
		return err
	}
	if err := validateCandidateConfig(out, edited, providerName, wireModel); err != nil {
		return err
	}

	if err := config.UpdateConfigFileLocked(globalPath, produce); err != nil {
		return fmt.Errorf("update %s: %w", globalPath, err)
	}
	printConfigAddSummary(out, edit, mode, poolName, poolRef)

	if opts.envVar != "" {
		if err := addProviderEnvCredential(providerName, strings.TrimSpace(opts.envVar), out); err != nil {
			return err
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
// Nothing here writes: an unmatched name fails with the suggestion list so
// adoption always stays an explicit --catalog choice.
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
		return "", "", fmt.Errorf("catalog model %q does not exist; run `chord config show --catalog` for the verified list", borrowID)
	}
	if preset != "" {
		if _, bound := modelcatalog.LookupBindingByModelID(preset, borrowID); !bound {
			return "", "", fmt.Errorf("catalog model %q is not bound to preset %q; keep the preset off this provider or borrow a model bound to it", borrowID, preset)
		}
	}
	return configAddBorrow, borrowID, nil
}

// printCatalogSuggestions renders the ranked suggestion list and reports
// whether anything was shown.
func printCatalogSuggestions(out io.Writer, wireModel string) bool {
	suggestions := modelcatalog.SuggestModels(wireModel, 5)
	if len(suggestions) == 0 {
		return false
	}
	fmt.Fprintf(out, "%q is not in the verified catalog for this endpoint. Closest verified models:\n", wireModel)
	for i, s := range suggestions {
		fmt.Fprintf(out, "  %d) %s (context %d / output %d)\n", i+1, s.ModelID, s.Facts.Context, s.Facts.Output)
	}
	fmt.Fprintln(out, "Borrow one explicitly, e.g.:")
	fmt.Fprintf(out, "  chord config add --catalog %s\n", suggestions[0].ModelID)
	return true
}

func addProviderEnvCredential(providerName, envVar string, out io.Writer) error {
	state, err := existingCredentialStateForProvider(providerName)
	if err != nil {
		return err
	}
	if state.HasCredentials {
		fmt.Fprintf(out, "Provider %q already has credentials in auth.yaml; --api-key-env ignored.\n", providerName)
		return nil
	}
	authPath, err := config.AuthPath()
	if err != nil {
		return fmt.Errorf("resolve auth path: %w", err)
	}
	if _, err := config.UpsertAPIKeyCredentialInFile(authPath, providerName, "$"+envVar); err != nil {
		return fmt.Errorf("write auth.yaml: %w", err)
	}
	fmt.Fprintf(out, "Wrote $%s for provider %q in %s.\n", envVar, providerName, authPath)
	return nil
}

// validateCandidateConfig resolves the edited bytes through the real loader
// and fails on any error-level diagnostic, so an unusable candidate never
// reaches disk.
func validateCandidateConfig(out io.Writer, edited []byte, providerName, wireModel string) error {
	tmp, err := os.CreateTemp("", "chord-config-add-*.yaml")
	if err != nil {
		return fmt.Errorf("create candidate temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(edited); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write candidate temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write candidate temp file: %w", err)
	}
	candidate, err := config.LoadResolvedConfig(tmpPath, "")
	if err != nil {
		return fmt.Errorf("candidate config does not resolve: %w", err)
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
	printConfigAddPreview(out, candidate, providerName, wireModel)
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
		fmt.Fprintf(out, "  providers.%s.models.%s.catalog: %s (borrows protocol-independent facts only)\n", edit.providerName, edit.wireModel, edit.borrowID)
	}
	fmt.Fprintf(out, "  model_pools.%s: append %s\n", poolName, poolRef)
}

type configAddEdit struct {
	providerName string
	wireModel    string
	providerNew  bool
	providerType string
	apiURL       string
	borrowID     string
	poolName     string
	poolRef      string
}

// editConfigYAMLForAdd applies the add edit to the raw config bytes with a
// yaml.Node round-trip, which preserves comments and ordering of everything
// it does not touch. Files that use anchors or aliases fail instead of being
// rewritten, because node editing cannot keep them faithful.
func editConfigYAMLForAdd(current []byte, e configAddEdit) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(current, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := rejectAnchoredConfig(&doc); err != nil {
		return nil, err
	}
	root := documentRootMapping(&doc)

	providersNode := ensureMappingChild(root, "providers")
	providerNode := ensureMappingChild(providersNode, e.providerName)
	if e.providerNew {
		setMappingScalar(providerNode, "type", e.providerType)
		setMappingScalar(providerNode, "api_url", e.apiURL)
	}
	if e.borrowID != "" {
		modelsNode := ensureMappingChild(providerNode, "models")
		modelNode := ensureMappingChild(modelsNode, e.wireModel)
		setMappingScalar(modelNode, "catalog", e.borrowID)
	}

	poolsNode := ensureMappingChild(root, "model_pools")
	poolNode := ensureSequenceChild(poolsNode, e.poolName)
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

// rejectAnchoredConfig walks the document and fails when any node defines or
// references an anchor: round-tripping such files through node editing can
// silently change merge semantics, so the edit is refused up front.
func rejectAnchoredConfig(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode || strings.TrimSpace(n.Anchor) != "" {
		return fmt.Errorf("config uses YAML anchors or aliases; automatic editing cannot preserve them — add the model by editing config.yaml directly")
	}
	for _, child := range n.Content {
		if err := rejectAnchoredConfig(child); err != nil {
			return err
		}
	}
	return nil
}

// documentRootMapping returns the document's root mapping, building an empty
// one for a blank file.
func documentRootMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
		return doc.Content[0]
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return doc
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && strings.TrimSpace(root.Value) == "" {
		root.Kind = yaml.MappingNode
		root.Tag = "!!map"
		root.Value = ""
		return root
	}
	if root.Kind != yaml.MappingNode {
		panic("config add: config root is not a mapping")
	}
	return root
}

// mappingValue returns the value node for key in a mapping, or nil.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// ensureMappingChild returns the mapping stored under key, creating an empty
// one when the key is missing. A non-mapping existing value is a conflict.
func ensureMappingChild(mapping *yaml.Node, key string) *yaml.Node {
	if existing := mappingValue(mapping, key); existing != nil {
		if existing.Kind != yaml.MappingNode {
			panic(fmt.Sprintf("config add: %q already exists and is not a mapping", key))
		}
		return existing
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, keyNode, valueNode)
	return valueNode
}

func setMappingScalar(mapping *yaml.Node, key, value string) {
	if existing := mappingValue(mapping, key); existing != nil {
		if existing.Kind != yaml.ScalarNode {
			panic(fmt.Sprintf("config add: %q already exists and is not a scalar", key))
		}
		existing.Value = value
		existing.Tag = "!!str"
		return
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	mapping.Content = append(mapping.Content, keyNode, valueNode)
}

func ensureSequenceChild(mapping *yaml.Node, key string) *yaml.Node {
	if existing := mappingValue(mapping, key); existing != nil {
		if existing.Kind != yaml.SequenceNode {
			panic(fmt.Sprintf("config add: model pool %q already exists and is not a list", key))
		}
		return existing
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	mapping.Content = append(mapping.Content, keyNode, valueNode)
	return valueNode
}

func appendSequenceValue(seq *yaml.Node, value string) bool {
	for _, item := range seq.Content {
		if item.Value == value {
			return false
		}
	}
	seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	return true
}
