package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
)

// configShowCatalogEndpoint is one catalog endpoint contract in JSON output.
type configShowCatalogEndpoint struct {
	Preset     string `json:"preset"`
	Protocol   string `json:"protocol"`
	RequestURL string `json:"request_url"`
	AuthMethod string `json:"auth_method"`
	EnvVar     string `json:"env_var,omitempty"`
}

// configShowCatalogModel is one catalog binding in JSON output, annotated with
// whether the effective config already uses it.
type configShowCatalogModel struct {
	Endpoint         string                          `json:"endpoint"`
	WireModel        string                          `json:"wire_model_id"`
	CatalogModel     string                          `json:"catalog_model_id"`
	Context          int                             `json:"context"`
	Input            int                             `json:"input_limit,omitempty"`
	Output           int                             `json:"output_limit"`
	Modalities       []string                        `json:"input_modalities,omitempty"`
	Variants         map[string]modelcatalog.Variant `json:"variants,omitempty"`
	Cost             *modelcatalog.Cost              `json:"cost,omitempty"`
	Sources          []modelcatalog.Source           `json:"sources,omitempty"`
	ReasoningOptions []string                        `json:"reasoning_options,omitempty"`
	Responses        *modelcatalog.ResponsesContract `json:"responses,omitempty"`
	Configured       bool                            `json:"configured"`
}

// configShowCatalogReport is the JSON form of chord config show --catalog.
type configShowCatalogReport struct {
	Version string `json:"version"`
	// Source records the upstream revision the shown snapshot was generated
	// from, when the catalog knows one.
	Source *modelcatalog.CatalogSource `json:"source,omitempty"`
	// FromRefreshCache reports whether the shown catalog came from the
	// refresh cache rather than the embedded snapshot.
	FromRefreshCache bool                        `json:"from_refresh_cache"`
	CacheDetail      string                      `json:"cache_detail,omitempty"`
	Endpoints        []configShowCatalogEndpoint `json:"endpoints"`
	Models           []configShowCatalogModel    `json:"models"`
}

// renderConfigShowCatalog writes the read-only catalog view: what the
// effective catalog knows, which of its candidates the effective config
// already uses, and which snapshot the view was resolved against. It never
// mutates anything and works without any config file.
func renderConfigShowCatalog(out io.Writer, opts configShowOptions, rc *config.ResolvedConfig) error {
	origin := modelcatalog.OriginInfo()
	status := modelcatalog.CurrentCacheStatus()
	report := configShowCatalogReport{
		Version:          origin.Version,
		Source:           origin.Source,
		FromRefreshCache: origin.Cached,
		CacheDetail:      status.Detail,
		Endpoints:        catalogShowEndpoints(),
		Models:           catalogShowModels(rc.Config),
	}
	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	return renderConfigShowCatalogText(out, report)
}

func catalogShowEndpoints() []configShowCatalogEndpoint {
	contracts := modelcatalog.EndpointContracts()
	out := make([]configShowCatalogEndpoint, 0, len(contracts))
	for _, e := range contracts {
		out = append(out, configShowCatalogEndpoint{
			Preset:     e.PresetID,
			Protocol:   e.Protocol,
			RequestURL: e.RequestURL,
			AuthMethod: e.AuthMethod,
			EnvVar:     e.EnvVar,
		})
	}
	return out
}

// catalogShowModels lists every catalog binding sorted by endpoint and wire
// model ID, marking the ones the effective config defines or references.
func catalogShowModels(cfg *config.Config) []configShowCatalogModel {
	configured := catalogConfiguredModels(cfg)
	var out []configShowCatalogModel
	for _, endpoint := range modelcatalog.EndpointContracts() {
		for _, b := range modelcatalog.BindingsForEndpoint(endpoint.PresetID) {
			entry := configShowCatalogModel{
				Endpoint:     b.Endpoint,
				WireModel:    b.WireModelID,
				CatalogModel: b.ModelID,
				Configured:   configured[endpoint.PresetID][b.WireModelID],
			}
			if facts, ok := modelcatalog.Model(b.ModelID); ok {
				entry.Context = facts.Context
				entry.Input = facts.Input
				entry.Output = facts.Output
				entry.Modalities = facts.InputModalities
				entry.Cost = facts.Cost
				entry.Sources = facts.Sources
				entry.ReasoningOptions = facts.ReasoningOptions
			}
			entry.Variants = b.Variants
			entry.Responses = b.Responses
			out = append(out, entry)
		}
	}
	return out
}

// catalogConfiguredModels maps preset ID -> wire model IDs that the effective
// config either defines under a provider with that preset or references from
// a model pool.
func catalogConfiguredModels(cfg *config.Config) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	mark := func(preset, wireModel string, model config.ModelConfig) {
		if model.Catalog != nil && model.Catalog.ID != "" {
			binding, ok := modelcatalog.LookupBindingByModelID(preset, model.Catalog.ID)
			if !ok {
				return
			}
			wireModel = binding.WireModelID
		}
		if preset == "" || wireModel == "" {
			return
		}
		if out[preset] == nil {
			out[preset] = map[string]bool{}
		}
		out[preset][wireModel] = true
	}
	if cfg == nil {
		return out
	}
	for _, provider := range cfg.Providers {
		preset := strings.ToLower(strings.TrimSpace(provider.Preset))
		if !modelcatalog.IsManagedPreset(preset) {
			continue
		}
		for name, model := range provider.Models {
			mark(preset, name, model)
		}
	}
	for _, refs := range cfg.ModelPools {
		for _, ref := range refs {
			base, _ := config.ParseModelRef(strings.TrimSpace(ref))
			providerName, modelName := config.SplitProviderModelRef(base)
			provider, ok := cfg.Providers[providerName]
			preset := strings.ToLower(strings.TrimSpace(provider.Preset))
			if !ok || !modelcatalog.IsManagedPreset(preset) {
				continue
			}
			mark(preset, modelName, provider.Models[modelName])
		}
	}
	return out
}

func renderConfigShowCatalogText(out io.Writer, report configShowCatalogReport) error {
	fmt.Fprintf(out, "Model catalog version %s (%s):\n\n", report.Version, catalogOriginLabel(report))
	if report.CacheDetail != "" {
		fmt.Fprintf(out, "Refresh cache not in effect: %s\n\n", report.CacheDetail)
	}

	fmt.Fprintln(out, "Endpoints:")
	for _, e := range report.Endpoints {
		auth := e.AuthMethod
		if e.EnvVar != "" {
			auth += " (" + e.EnvVar + ")"
		}
		fmt.Fprintf(out, "  %-10s %-17s %s  auth=%s\n", e.Preset, e.Protocol, e.RequestURL, auth)
	}

	fmt.Fprintln(out, "\nModels (configured = defined or referenced by a pool in the effective config):")
	if len(report.Models) == 0 {
		fmt.Fprintln(out, "  (none)")
		return nil
	}
	for _, m := range report.Models {
		status := "not configured"
		if m.Configured {
			status = "configured"
		}
		fmt.Fprintf(out, "  %s / %s  [%s]\n", m.Endpoint, m.WireModel, status)
		facts := fmt.Sprintf("    context=%d, output=%d", m.Context, m.Output)
		if m.Input > 0 {
			facts += fmt.Sprintf(", input=%d", m.Input)
		}
		fmt.Fprintln(out, facts)
		if len(m.Variants) > 0 {
			names := make([]string, 0, len(m.Variants))
			for name := range m.Variants {
				names = append(names, name)
			}
			slices.Sort(names)
			labels := make([]string, 0, len(names))
			for _, name := range names {
				labels = append(labels, catalogVariantLabel(name, m.Variants[name]))
			}
			fmt.Fprintf(out, "    variants: %s\n", strings.Join(labels, ", "))
		}
		if len(m.ReasoningOptions) > 0 {
			fmt.Fprintf(out, "    reasoning options: %s\n", strings.Join(m.ReasoningOptions, ", "))
		}
		if m.Responses != nil {
			fmt.Fprintf(out, "    Responses fields: send_store=%v, send_parallel_tool_calls=%v\n", responseFieldEnabled(m.Responses.SendStore, true), responseFieldEnabled(m.Responses.SendParallelToolCalls, true))
		}
		for _, source := range m.Sources {
			fmt.Fprintf(out, "    source: %s (verified %s)\n", source.URL, source.Checked)
		}
		if m.Cost != nil {
			fmt.Fprintf(out, "    cost: %g/%g %s per million tokens (verified %s)\n",
				m.Cost.InputPerMillion, m.Cost.OutputPerMillion, m.Cost.Currency, m.Cost.Checked)
		}
	}
	return nil
}

// catalogOriginLabel describes where the shown catalog snapshot came from:
// the refresh cache, or the embedded snapshot with its recorded upstream
// revision when one was synced in.
func catalogOriginLabel(report configShowCatalogReport) string {
	switch {
	case report.FromRefreshCache && report.Source != nil:
		return fmt.Sprintf("refresh cache of %s @ %s; read-only reference, not your config", report.Source.Repository, report.Source.Revision)
	case report.FromRefreshCache:
		return "refresh cache; read-only reference, not your config"
	case report.Source != nil:
		return fmt.Sprintf("embedded snapshot of %s @ %s; read-only reference, not your config", report.Source.Repository, report.Source.Revision)
	default:
		return "embedded snapshot; read-only reference, not your config"
	}
}

// catalogVariantLabel renders one variant as its name plus the knobs it sets,
// so thinking-style tiers stay distinguishable from reasoning efforts.
func catalogVariantLabel(name string, v modelcatalog.Variant) string {
	var knobs []string
	if v.ReasoningEffort != "" {
		knobs = append(knobs, "reasoning_effort="+v.ReasoningEffort)
	}
	if v.ThinkingType != "" {
		knobs = append(knobs, "thinking_type="+v.ThinkingType)
	}
	if v.ThinkingEffort != "" {
		knobs = append(knobs, "thinking_effort="+v.ThinkingEffort)
	}
	if len(knobs) == 0 {
		return name
	}
	return name + " (" + strings.Join(knobs, ", ") + ")"
}
