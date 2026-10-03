package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

// configShowOptions carries the chord config show flags.
type configShowOptions struct {
	JSON    bool
	Path    string
	Catalog bool
}

// configShowOrigin is one tracked raw declaration of the effective config.
type configShowOrigin struct {
	Path  string `json:"path"`
	Layer string `json:"layer"`
	File  string `json:"file,omitempty"`
	Line  int    `json:"line,omitempty"`
	Col   int    `json:"col,omitempty"`
	Note  string `json:"note,omitempty"`
}

// configShowBudget lists the budget facts of one configured model: the stored
// limits, whether they are explicit or derived, and the request budgets the
// runtime computes from them.
type configShowBudget struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Context        int    `json:"context"`
	ContextDerived bool   `json:"context_derived"`
	Input          int    `json:"input_limit"`
	InputExplicit  bool   `json:"input_explicit"`
	Output         int    `json:"output_limit"`
	InputBudget    int    `json:"input_budget"`
	OutputBudget   int    `json:"output_budget"`
}

// configShowReport is the JSON form of chord config show.
type configShowReport struct {
	Config          map[string]any               `json:"config"`
	Origins         []configShowOrigin           `json:"origins,omitempty"`
	Budgets         []configShowBudget           `json:"model_budgets,omitempty"`
	Diagnostics     []config.Diagnostic          `json:"diagnostics,omitempty"`
	RequestSettings []configShowResponsesRequest `json:"request_settings,omitempty"`
	OK              bool                         `json:"ok"`
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect configuration",
	}
	cmd.AddCommand(newConfigAddCmd(), newConfigShowCmd(), newConfigRefreshCatalogCmd())
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	opts := configShowOptions{}
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the effective config with field origins and diagnostics",
		Long: `Show the effective configuration (project merged over global) with the
origin of every tracked field, model budget facts, and structured
diagnostics.

With --catalog, show the built-in model catalog instead: verified
endpoint contracts, models, and reasoning variants, each marked as
configured or not configured in the effective config. The catalog view
is read-only reference; it never previews into your files.

The command is fully offline: it reads the config files and environment
variables only. It never initializes LLM clients, refreshes OAuth state,
probes the network, or writes any file. JSON ok is false when an error diagnostic exists. Diagnostics are listed
without changing the exit status; chord doctor config is the pass/fail entry.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := configShowFlagsError(opts); err != nil {
				return err
			}
			return runConfigShow(cmd.OutOrStdout(), opts)
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Write a JSON report")
	cmd.Flags().BoolVar(&opts.Catalog, "catalog", false,
		"Show the built-in model catalog (endpoints, models, variants) instead of the effective config")
	cmd.Flags().StringVar(&opts.Path, "path", "",
		"Restrict the config section to a dotted config path (for example providers.sample.models.model-1)")
	return cmd
}

// configShowFlagsError rejects flag combinations that mix incompatible views.
func configShowFlagsError(opts configShowOptions) error {
	if opts.Catalog && opts.Path != "" {
		return fmt.Errorf("--path filters the effective config and cannot be combined with --catalog")
	}
	return nil
}

func runConfigShow(out io.Writer, opts configShowOptions) error {
	globalPath, err := config.ConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	projectPath := ""
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		projectPath = config.ProjectConfigPath(cwd)
	}
	rc, err := config.LoadResolvedConfig(globalPath, projectPath)
	if err != nil {
		return err
	}
	if opts.Catalog {
		return renderConfigShowCatalog(out, opts, rc)
	}
	return renderConfigShowResult(out, opts, rc)
}

func renderConfigShowResult(out io.Writer, opts configShowOptions, rc *config.ResolvedConfig) error {
	diagnostics := make([]config.Diagnostic, len(rc.Diagnostics))
	for i, diagnostic := range rc.Diagnostics {
		diagnostics[i] = redactShowDiagnostic(diagnostic)
	}
	origins := configShowOrigins(rc.Index)
	budgets := configShowBudgets(rc.Config, rc.Index)
	cfgMap := configToShowMap(rc.Config)
	requests := configShowResponses(rc.Config, opts.Path)

	if opts.Path != "" {
		subtree, err := filterShowMap(cfgMap, opts.Path)
		if err != nil {
			return err
		}
		cfgMap = subtree
		origins = filterShowOrigins(origins, opts.Path)
		budgets = filterShowBudgets(budgets, opts.Path)
	}

	if opts.JSON {
		report := configShowReport{
			Config:          cfgMap,
			Origins:         origins,
			Budgets:         budgets,
			Diagnostics:     diagnostics,
			RequestSettings: requests,
			OK:              !hasConfigErrors(rc.Diagnostics),
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	return renderConfigShowText(out, cfgMap, origins, budgets, diagnostics, opts.Path, requests)
}

func renderConfigShowText(out io.Writer, cfgMap map[string]any, origins []configShowOrigin, budgets []configShowBudget, diags []config.Diagnostic, pathFilter string, requests []configShowResponsesRequest) error {
	if pathFilter != "" {
		fmt.Fprintf(out, "Effective config at %s (project merged over global):\n\n", pathFilter)
	} else {
		fmt.Fprintln(out, "Effective config (project merged over global):")
	}
	fmt.Fprintln(out)
	if len(cfgMap) == 0 {
		fmt.Fprintln(out, "  (empty)")
	} else {
		data, err := yaml.Marshal(cfgMap)
		if err != nil {
			return fmt.Errorf("render config: %w", err)
		}
		_, err = out.Write(data)
		if err != nil {
			return err
		}
	}

	if len(origins) > 0 {
		fmt.Fprintln(out, "\nOrigins (tracked declarations, lowest-priority layer first):")
		current := ""
		for _, o := range origins {
			if o.Path != current {
				current = o.Path
				fmt.Fprintf(out, "  %s\n", o.Path)
			}
			loc := o.File
			if o.Line > 0 {
				loc += fmt.Sprintf(":%d", o.Line)
			}
			note := ""
			if o.Note != "" {
				note = " (" + o.Note + ")"
			}
			fmt.Fprintf(out, "    %s %s%s\n", o.Layer, loc, note)
		}
	}

	if len(budgets) > 0 {
		fmt.Fprintln(out, "\nModel budgets:")
		for _, b := range budgets {
			contextNote := "explicit"
			if b.ContextDerived {
				contextNote = "derived from input+output"
			}
			inputNote := "explicit"
			if !b.InputExplicit {
				inputNote = "derived from context/output"
			}
			fmt.Fprintf(out, "  %s/%s: context=%d (%s), input=%d (%s), output=%d; request input budget=%d, output budget=%d\n",
				b.Provider, b.Model, b.Context, contextNote, b.Input, inputNote, b.Output, b.InputBudget, b.OutputBudget)
		}
	}

	if len(requests) > 0 {
		fmt.Fprintln(out, "\nResponses request settings (values and field emission; before request_overrides; tool fields require tools, variants may override values):")
		data, err := yaml.Marshal(requests)
		if err != nil {
			return fmt.Errorf("render request settings: %w", err)
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
	}

	fmt.Fprintln(out, "\nDiagnostics:")
	if len(diags) == 0 {
		fmt.Fprintln(out, "  none")
	} else {
		for _, d := range diags {
			line := "  [" + string(d.Severity) + "] " + d.String()
			if d.Fallback != "" {
				line += " (fallback: " + d.Fallback + ")"
			}
			fmt.Fprintln(out, line)
		}
	}
	return nil
}

// configToShowMap serializes the effective config into a redacted generic map
// for display. Values that may carry credentials are masked; structure and
// non-sensitive values stay intact.
func configToShowMap(cfg *config.Config) map[string]any {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}
	if out == nil {
		out = map[string]any{}
	}
	redactShowConfigValue(out, nil)
	return out
}

// redactShowConfigValue masks credential-bearing leaves in place. It is
// conservative: free-form containers such as request override headers are
// masked wholesale because they cannot be classified by key.
func redactShowConfigValue(node any, path []string) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			full := append(slices.Clip(path), key)
			switch {
			case strings.EqualFold(key, "headers"):
				redactAllLeafValues(child)
			case sensitiveShowKey(key):
				value[key] = "[redacted]"
			case isConfigURLKey(key):
				if s, ok := child.(string); ok {
					value[key] = redactShowURL(s)
				}
			default:
				redactShowConfigValue(child, full)
			}
		}
	case []any:
		for _, item := range value {
			redactShowConfigValue(item, path)
		}
	}
}

func redactAllLeafValues(node any) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if _, isMap := child.(map[string]any); isMap {
				redactAllLeafValues(child)
				continue
			}
			if _, isSeq := child.([]any); isSeq {
				redactAllLeafValues(child)
				continue
			}
			value[key] = "[redacted]"
		}
	case []any:
		for _, item := range value {
			redactAllLeafValues(item)
		}
	}
}

func sensitiveShowKey(key string) bool {
	k := strings.ToLower(key)
	if strings.HasSuffix(k, "_url") || strings.HasSuffix(k, "_file") || strings.HasSuffix(k, "_path") {
		return false
	}
	// Separator styles vary between snake_case config keys and URL query
	// parameters; fold them so "api-key" and "api_key" match alike.
	normalized := strings.NewReplacer("-", "_", ".", "_").Replace(k)
	switch {
	case strings.Contains(normalized, "secret"),
		strings.Contains(normalized, "password"),
		strings.Contains(normalized, "authorization"),
		strings.Contains(normalized, "api_key"),
		strings.Contains(normalized, "apikey"),
		strings.Contains(normalized, "bearer"):
		return true
	case normalized == "token", normalized == "refresh", normalized == "access", normalized == "key":
		return true
	case strings.HasSuffix(normalized, "_token"):
		return true
	}
	return false
}

func isConfigURLKey(key string) bool {
	k := strings.ToLower(key)
	return strings.HasSuffix(k, "_url") || k == "url" || k == "proxy"
}

// redactShowURL masks URL credentials while keeping the endpoint readable.
func redactShowURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[redacted URL]"
	}
	query, queryErr := url.ParseQuery(parsed.RawQuery)
	changed := false
	if queryErr != nil {
		parsed.RawQuery = "[redacted]"
		changed = true
	}
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword("[redacted]", "[redacted]")
		} else {
			parsed.User = url.User("[redacted]")
		}
		changed = true
	}
	for key, values := range query {
		if !sensitiveShowKey(key) {
			continue
		}
		for i := range values {
			values[i] = "[redacted]"
		}
		changed = true
	}
	if !changed {
		return raw
	}
	if queryErr == nil {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

var showDiagnosticURL = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>]+`)

func redactShowDiagnostic(d config.Diagnostic) config.Diagnostic {
	redact := func(value string) string { return showDiagnosticURL.ReplaceAllStringFunc(value, redactShowURL) }
	d.Message = redact(d.Message)
	d.Fallback = redact(d.Fallback)
	d.Scope = redact(d.Scope)
	return d
}

// filterShowMap narrows the rendered config map to a dotted path. A path
// ending on a leaf wraps that leaf so it still renders as YAML.
func filterShowMap(m map[string]any, path string) (map[string]any, error) {
	cur := m
	segs := strings.Split(path, ".")
	for i, seg := range segs {
		child, ok := cur[seg]
		if !ok {
			return nil, fmt.Errorf("config path %q not found", path)
		}
		if i == len(segs)-1 {
			if leafMap, ok := child.(map[string]any); ok {
				return leafMap, nil
			}
			return map[string]any{seg: child}, nil
		}
		next, ok := child.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("config path %q not found", path)
		}
		cur = next
	}
	return cur, nil
}

func filterShowOrigins(origins []configShowOrigin, path string) []configShowOrigin {
	prefix := path + "."
	var out []configShowOrigin
	for _, o := range origins {
		if o.Path == path || strings.HasPrefix(o.Path, prefix) {
			out = append(out, o)
		}
	}
	return out
}

func filterShowBudgets(budgets []configShowBudget, path string) []configShowBudget {
	fullPath := path
	if !strings.HasPrefix(fullPath, "providers.") {
		fullPath = "providers." + fullPath
	}
	prefix := fullPath + "."
	var out []configShowBudget
	for _, b := range budgets {
		modelPath := "providers." + b.Provider + ".models." + b.Model
		if modelPath == fullPath || strings.HasPrefix(modelPath, prefix) {
			out = append(out, b)
		}
	}
	return out
}

// configShowOrigins flattens the sparse index into display entries sorted by
// path so text and JSON output are deterministic.
func configShowOrigins(idx *config.SourceIndex) []configShowOrigin {
	if idx == nil {
		return nil
	}
	var out []configShowOrigin
	add := func(path string, o config.Origin, note string) {
		out = append(out, configShowOrigin{
			Path:  path,
			Layer: string(o.Layer),
			File:  o.File,
			Line:  o.Line,
			Col:   o.Col,
			Note:  note,
		})
	}
	providerNames := make([]string, 0, len(idx.Providers))
	for name := range idx.Providers {
		providerNames = append(providerNames, name)
	}
	slices.Sort(providerNames)
	for _, provider := range providerNames {
		po := idx.Providers[provider]
		for _, o := range po.Preset {
			add("providers."+provider+".preset", o, "")
		}
		modelNames := make([]string, 0, len(po.Models))
		for name := range po.Models {
			modelNames = append(modelNames, name)
		}
		slices.Sort(modelNames)
		for _, model := range modelNames {
			mo := po.Models[model]
			base := "providers." + provider + ".models." + model
			for _, o := range mo.Defined {
				add(base, o, "")
			}
			for _, o := range mo.Catalog {
				add(base+".catalog", o, "explicit catalog binding")
			}
			blocks := make([]string, 0, len(mo.Blocks))
			for block := range mo.Blocks {
				blocks = append(blocks, block)
			}
			slices.Sort(blocks)
			for _, block := range blocks {
				for _, decl := range mo.Blocks[block] {
					note := ""
					if decl.Null {
						note = "explicit null, clears lower layers"
					}
					add(base+"."+block, decl.Origin, note)
				}
			}
			for _, o := range mo.Limit.Context {
				add(base+".limit.context", o, "")
			}
			for _, o := range mo.Limit.Input {
				add(base+".limit.input", o, "")
			}
			for _, o := range mo.Limit.Output {
				add(base+".limit.output", o, "")
			}
		}
	}
	poolNames := make([]string, 0, len(idx.ModelPools))
	for pool := range idx.ModelPools {
		poolNames = append(poolNames, pool)
	}
	slices.Sort(poolNames)
	for _, pool := range poolNames {
		for i, ref := range idx.ModelPools[pool] {
			add("model_pools."+pool, ref.Origin, fmt.Sprintf("[%d] %s", i, ref.Ref))
		}
	}
	slices.SortStableFunc(out, func(a, b configShowOrigin) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Layer, b.Layer)
	})
	return out
}

// configShowBudgets reports the stored limits and the request budgets the
// runtime derives from them, using the same output-cap semantics as the LLM
// client.
func configShowBudgets(cfg *config.Config, idx *config.SourceIndex) []configShowBudget {
	if cfg == nil {
		return nil
	}
	var out []configShowBudget
	providerNames := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		providerNames = append(providerNames, name)
	}
	slices.Sort(providerNames)
	for _, provider := range providerNames {
		p := cfg.Providers[provider]
		modelNames := make([]string, 0, len(p.Models))
		for name := range p.Models {
			modelNames = append(modelNames, name)
		}
		slices.Sort(modelNames)
		for _, model := range modelNames {
			m := p.Models[model]
			inputExplicit := false
			if idx != nil {
				_, inputExplicit = idx.ExplicitInputContract(provider, model)
			}
			out = append(out, configShowBudget{
				Provider:       provider,
				Model:          model,
				Context:        m.Limit.Context,
				ContextDerived: m.Limit.ContextDerived,
				Input:          m.Limit.Input,
				InputExplicit:  inputExplicit && m.Limit.Input > 0,
				Output:         m.Limit.Output,
				InputBudget:    m.Limit.EffectiveInputBudget(cfg.MaxOutputTokens, llm.DefaultOutputTokenMax),
				OutputBudget:   m.Limit.EffectiveOutputBudget(cfg.MaxOutputTokens, llm.DefaultOutputTokenMax),
			})
		}
	}
	return out
}
