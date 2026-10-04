// Package gen builds the committed catalog artifact from the YAML source
// files under internal/modelcatalog/data or a fetched source snapshot. Startup
// reads catalog.json through go:embed; explicit refresh uses this package to
// validate upstream sources before installing them.
//
// The generated artifact is byte-stable: entries are sorted by identity, map
// keys are sorted by the encoder, and the sources must pass the same
// structural validation the runtime enforces at load, plus stricter
// generation-time checks (https sources, verifiable check dates, positive
// pricing). Regenerate with `go run ./cmd/modelcatalog-gen` from the
// repository root; the golden test in internal/modelcatalog fails when the
// committed artifact drifts from the sources.
package gen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
)

const (
	fileNameCatalog   = "catalog.yaml"
	fileNameEndpoints = "endpoints.yaml"
	fileNameModels    = "models.yaml"
	fileNameBindings  = "bindings.yaml"
	// fileNameSnapshot optionally records which upstream revision the source
	// directory was synced from. The chord repository writes it when pinning
	// a chord-models tag; the upstream repository itself has none, so
	// validating upstream sources directly produces an artifact without a
	// source record.
	fileNameSnapshot = "snapshot.yaml"
)

// sourceRef is one verification citation in the source files. The checked
// date must be a real calendar day so staleness reviews stay meaningful.
type sourceRef struct {
	URL     string `yaml:"url"`
	Checked string `yaml:"checked"`
}

func (s sourceRef) validate(where string) error {
	if !strings.HasPrefix(s.URL, "https://") {
		return fmt.Errorf("%s: source URL must be https, got %q", where, s.URL)
	}
	if err := checkDate(s.Checked, where); err != nil {
		return err
	}
	return nil
}

func checkDate(value, where string) error {
	if _, err := time.Parse(time.DateOnly, value); err != nil {
		return fmt.Errorf("%s: checked date %q must be a calendar day in YYYY-MM-DD form", where, value)
	}
	return nil
}

type catalogSource struct {
	Version string `yaml:"version"`
}

// snapshotMeta records the upstream revision a source directory was synced
// from; the generator copies it into the artifact's source record.
type snapshotMeta struct {
	Repository string `yaml:"repository"`
	Revision   string `yaml:"revision"`
}

type endpointsFile struct {
	Endpoints []endpointSource `yaml:"endpoints"`
}

type modelsFile struct {
	Models []modelSource `yaml:"models"`
}

type bindingsFile struct {
	Bindings []bindingSource `yaml:"bindings"`
}

type endpointSource struct {
	PresetID   string      `yaml:"preset_id"`
	Protocol   string      `yaml:"protocol"`
	RequestURL string      `yaml:"request_url"`
	AuthMethod string      `yaml:"auth_method"`
	EnvVar     string      `yaml:"env_var"`
	Compress   string      `yaml:"compress"`
	Docs       []sourceRef `yaml:"docs"`
}

type costSource struct {
	InputPerMillion  float64   `yaml:"input_per_million"`
	OutputPerMillion float64   `yaml:"output_per_million"`
	Currency         string    `yaml:"currency"`
	Checked          string    `yaml:"checked"`
	Source           sourceRef `yaml:"source"`
	Notes            string    `yaml:"notes"`
}

type modelSource struct {
	ID               string               `yaml:"id"`
	Released         string               `yaml:"released"`
	CodingSources    []sourceRef          `yaml:"coding_sources"`
	Connection       *connectionSource    `yaml:"connection"`
	Context          int                  `yaml:"context"`
	Input            int                  `yaml:"input"`
	Output           int                  `yaml:"output"`
	InputModalities  []string             `yaml:"input_modalities"`
	ReasoningOptions []string             `yaml:"reasoning_options"`
	Cost             *costSource          `yaml:"cost"`
	Sources          []sourceRef          `yaml:"sources"`
	ConfigProfile    *configProfileSource `yaml:"config_profile"`
}

type connectionSource struct {
	RequestURL  string      `yaml:"request_url"`
	WireModelID string      `yaml:"wire_model_id"`
	EnvVar      string      `yaml:"env_var"`
	Sources     []sourceRef `yaml:"sources"`
}

type variantSource struct {
	ReasoningEffort string `yaml:"reasoning_effort"`
	ThinkingType    string `yaml:"thinking_type"`
	ThinkingEffort  string `yaml:"thinking_effort"`
}

type responsesSource struct {
	SendStore             *bool `yaml:"send_store"`
	SendReasoningInclude  *bool `yaml:"send_reasoning_include"`
	SendToolChoice        *bool `yaml:"send_tool_choice"`
	SendPromptCacheKey    *bool `yaml:"send_prompt_cache_key"`
	SendMaxOutputTokens   *bool `yaml:"send_max_output_tokens"`
	SendParallelToolCalls *bool `yaml:"send_parallel_tool_calls"`
}

type bindingSource struct {
	Endpoint        string                      `yaml:"endpoint"`
	WireModelID     string                      `yaml:"wire_model_id"`
	ModelID         string                      `yaml:"model_id"`
	Variants        map[string]variantSource    `yaml:"variants"`
	Responses       *responsesSource            `yaml:"responses"`
	Limit           *modelcatalog.LimitOverride `yaml:"limit"`
	InputModalities []string                    `yaml:"input_modalities"`
	ConfigProfile   *configProfileSource        `yaml:"config_profile"`
}

type configProfileSource struct {
	Model      map[string]any           `yaml:"model"`
	Compat     map[string]any           `yaml:"compat"`
	Compaction *compactionProfileSource `yaml:"compaction"`
	Sources    []sourceRef              `yaml:"sources"`
}

type compactionProfileSource struct {
	Threshold *float64 `yaml:"threshold"`
	Reminder  *float64 `yaml:"reminder"`
	Notes     string   `yaml:"notes"`
}

// Load reads and validates the source files in dir and returns the sorted,
// runtime-validated catalog.
func Load(dir string) (*modelcatalog.Catalog, error) {
	var meta catalogSource
	if err := decodeYAMLFile(filepath.Join(dir, fileNameCatalog), &meta); err != nil {
		return nil, err
	}
	if err := modelcatalog.ValidateVersion(meta.Version); err != nil {
		return nil, err
	}
	var endpointsFile endpointsFile
	if err := decodeYAMLFile(filepath.Join(dir, fileNameEndpoints), &endpointsFile); err != nil {
		return nil, err
	}
	var modelsFile modelsFile
	if err := decodeYAMLFile(filepath.Join(dir, fileNameModels), &modelsFile); err != nil {
		return nil, err
	}
	var bindingsFile bindingsFile
	if err := decodeYAMLFile(filepath.Join(dir, fileNameBindings), &bindingsFile); err != nil {
		return nil, err
	}
	source, err := loadSnapshotMeta(dir)
	if err != nil {
		return nil, err
	}
	c, err := buildCatalog(meta.Version, source, endpointsFile.Endpoints, modelsFile.Models, bindingsFile.Bindings)
	if err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("catalog sources fail structural validation: %w", err)
	}
	if err := config.ValidateCatalogProfiles(c); err != nil {
		return nil, err
	}
	return c, nil
}

// loadSnapshotMeta reads the optional snapshot.yaml. A missing file is the
// normal case when validating the upstream repository directly.
func loadSnapshotMeta(dir string) (*modelcatalog.CatalogSource, error) {
	data, err := os.ReadFile(filepath.Join(dir, fileNameSnapshot))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, fileNameSnapshot), err)
	}
	var meta snapshotMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Join(dir, fileNameSnapshot), err)
	}
	if !strings.HasPrefix(meta.Repository, "https://") {
		return nil, fmt.Errorf("%s: repository %q must be an https URL", fileNameSnapshot, meta.Repository)
	}
	if strings.TrimSpace(meta.Revision) == "" {
		return nil, fmt.Errorf("%s: revision is required", fileNameSnapshot)
	}
	return &modelcatalog.CatalogSource{Repository: meta.Repository, Revision: meta.Revision}, nil
}

// Generate produces the committed artifact bytes for the sources in dir.
func Generate(dir string) ([]byte, error) {
	c, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if _, err := LoadCandidates(dir, c); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal catalog: %w", err)
	}
	return append(data, '\n'), nil
}

func buildCatalog(version string, source *modelcatalog.CatalogSource, endpointsSrc []endpointSource, modelsSrc []modelSource, bindingsSrc []bindingSource) (*modelcatalog.Catalog, error) {
	endpoints := make([]modelcatalog.Endpoint, 0, len(endpointsSrc))
	for _, e := range endpointsSrc {
		docs := make([]modelcatalog.Source, 0, len(e.Docs))
		for _, d := range e.Docs {
			if err := d.validate(fmt.Sprintf("endpoint %q doc", e.PresetID)); err != nil {
				return nil, err
			}
			docs = append(docs, modelcatalog.Source{URL: d.URL, Checked: d.Checked})
		}
		endpoints = append(endpoints, modelcatalog.Endpoint{
			PresetID:   e.PresetID,
			Protocol:   e.Protocol,
			RequestURL: e.RequestURL,
			AuthMethod: e.AuthMethod,
			EnvVar:     e.EnvVar,
			Compress:   e.Compress,
			Docs:       docs,
		})
	}

	models := make([]modelcatalog.ModelFacts, 0, len(modelsSrc))
	for _, m := range modelsSrc {
		var connection *modelcatalog.Connection
		if m.Connection != nil {
			connection = &modelcatalog.Connection{RequestURL: m.Connection.RequestURL, WireModelID: m.Connection.WireModelID, EnvVar: m.Connection.EnvVar}
			for _, s := range m.Connection.Sources {
				if err := s.validate(fmt.Sprintf("model %q connection", m.ID)); err != nil {
					return nil, err
				}
				connection.Sources = append(connection.Sources, modelcatalog.Source{URL: s.URL, Checked: s.Checked})
			}
		}
		if m.Released != "" {
			if err := checkDate(m.Released, fmt.Sprintf("model %q release", m.ID)); err != nil {
				return nil, err
			}
		}
		codingSources := make([]modelcatalog.Source, 0, len(m.CodingSources))
		for _, s := range m.CodingSources {
			if err := s.validate(fmt.Sprintf("model %q coding evidence", m.ID)); err != nil {
				return nil, err
			}
			codingSources = append(codingSources, modelcatalog.Source{URL: s.URL, Checked: s.Checked})
		}
		sources := make([]modelcatalog.Source, 0, len(m.Sources))
		for _, s := range m.Sources {
			if err := s.validate(fmt.Sprintf("model %q source", m.ID)); err != nil {
				return nil, err
			}
			sources = append(sources, modelcatalog.Source{URL: s.URL, Checked: s.Checked})
		}
		var cost *modelcatalog.Cost
		if m.Cost != nil {
			converted, err := convertCost(*m.Cost, fmt.Sprintf("model %q cost", m.ID))
			if err != nil {
				return nil, err
			}
			cost = converted
		}
		models = append(models, modelcatalog.ModelFacts{
			ID:               m.ID,
			Released:         m.Released,
			CodingSources:    codingSources,
			Connection:       connection,
			Context:          m.Context,
			Input:            m.Input,
			Output:           m.Output,
			InputModalities:  slices.Clone(m.InputModalities),
			ReasoningOptions: slices.Clone(m.ReasoningOptions),
			Cost:             cost,
			Sources:          sources,
			Profile:          convertConfigProfile(m.ConfigProfile),
		})
	}

	bindings := make([]modelcatalog.Binding, 0, len(bindingsSrc))
	for _, b := range bindingsSrc {
		converted := modelcatalog.Binding{
			Endpoint:        b.Endpoint,
			WireModelID:     b.WireModelID,
			ModelID:         b.ModelID,
			Limit:           b.Limit,
			InputModalities: slices.Clone(b.InputModalities),
			Profile:         convertConfigProfile(b.ConfigProfile),
		}
		if len(b.Variants) > 0 {
			converted.Variants = make(map[string]modelcatalog.Variant, len(b.Variants))
			for name, v := range b.Variants {
				converted.Variants[name] = modelcatalog.Variant{
					ReasoningEffort: v.ReasoningEffort,
					ThinkingType:    v.ThinkingType,
					ThinkingEffort:  v.ThinkingEffort,
				}
			}
		}
		if b.Responses != nil {
			converted.Responses = &modelcatalog.ResponsesContract{
				SendStore:             b.Responses.SendStore,
				SendReasoningInclude:  b.Responses.SendReasoningInclude,
				SendToolChoice:        b.Responses.SendToolChoice,
				SendPromptCacheKey:    b.Responses.SendPromptCacheKey,
				SendMaxOutputTokens:   b.Responses.SendMaxOutputTokens,
				SendParallelToolCalls: b.Responses.SendParallelToolCalls,
			}
		}
		bindings = append(bindings, converted)
	}

	// Canonical order makes the artifact byte-stable regardless of how the
	// source files arrange their entries.
	slices.SortFunc(endpoints, func(a, b modelcatalog.Endpoint) int {
		return strings.Compare(a.PresetID, b.PresetID)
	})
	slices.SortFunc(models, func(a, b modelcatalog.ModelFacts) int {
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(bindings, func(a, b modelcatalog.Binding) int {
		if n := strings.Compare(a.Endpoint, b.Endpoint); n != 0 {
			return n
		}
		return strings.Compare(a.WireModelID, b.WireModelID)
	})

	return &modelcatalog.Catalog{
		Version:   version,
		Source:    source,
		Endpoints: endpoints,
		Models:    models,
		Bindings:  bindings,
	}, nil
}

func convertCost(c costSource, where string) (*modelcatalog.Cost, error) {
	// Missing pricing stays absent so it is never shown as free; a present
	// cost must be a real, dated price.
	if c.InputPerMillion <= 0 || c.OutputPerMillion <= 0 {
		return nil, fmt.Errorf("%s: prices must be positive; omit the cost block for unknown pricing instead of recording zero", where)
	}
	if strings.TrimSpace(c.Currency) == "" {
		return nil, fmt.Errorf("%s: currency is required", where)
	}
	if err := checkDate(c.Checked, where); err != nil {
		return nil, err
	}
	if err := c.Source.validate(where); err != nil {
		return nil, err
	}
	return &modelcatalog.Cost{
		InputPerMillion:  c.InputPerMillion,
		OutputPerMillion: c.OutputPerMillion,
		Currency:         c.Currency,
		Checked:          c.Checked,
		Source:           modelcatalog.Source{URL: c.Source.URL, Checked: c.Source.Checked},
		Notes:            c.Notes,
	}, nil
}

func convertConfigProfile(src *configProfileSource) *modelcatalog.ConfigProfile {
	if src == nil {
		return nil
	}
	p := &modelcatalog.ConfigProfile{Model: src.Model, Compat: src.Compat, Compaction: nil}
	for _, s := range src.Sources {
		p.Sources = append(p.Sources, modelcatalog.Source{URL: s.URL, Checked: s.Checked})
	}
	if src.Compaction != nil {
		c := src.Compaction
		p.Compaction = &modelcatalog.CompactionProfile{Threshold: c.Threshold, Reminder: c.Reminder, Notes: c.Notes}
	}
	return p
}

func decodeYAMLFile[T any](path string, out *T) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
