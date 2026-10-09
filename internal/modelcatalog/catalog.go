// Package modelcatalog holds the built-in, versioned catalog of verified
// endpoint contracts and model facts. It ships with the binary and is read
// only: entries carry their verification sources, unknown facts stay absent,
// and nothing here mutates at runtime.
//
// The package defines only small domain data. It must not depend on the
// config, agent, or llm packages; the config package adapts catalog entries
// into its own field definitions.
package modelcatalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

//go:embed catalog.json
var catalogData []byte

// Source records where a fact was verified.
type Source struct {
	URL     string `json:"url"`
	Checked string `json:"checked"` // YYYY-MM-DD
}

// Cost is per-token pricing in currency per million tokens. It is recorded
// only when verified; unknown pricing stays absent so it is never shown as
// free.
type Cost struct {
	InputPerMillion  float64 `json:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million"`
	Currency         string  `json:"currency"`
	Checked          string  `json:"checked"`
	Source           Source  `json:"source"`
	Notes            string  `json:"notes,omitempty"`
}

// ModelFacts are protocol-independent facts about one model. Input is the
// independent input-side contract when the provider publishes one; zero means
// the input budget derives from context/output.
type ModelFacts struct {
	ID               string         `json:"id"` // stable catalog ID, e.g. "openai/gpt-6.1-sol"
	Released         string         `json:"released,omitempty"`
	CodingSources    []Source       `json:"coding_sources,omitempty"`
	Connection       *Connection    `json:"connection,omitempty"`
	Context          int            `json:"context"`
	Input            int            `json:"input,omitempty"`
	Output           int            `json:"output"`
	InputModalities  []string       `json:"input_modalities,omitempty"` // default: text only
	ReasoningOptions []string       `json:"reasoning_options,omitempty"`
	Cost             *Cost          `json:"cost,omitempty"`
	Sources          []Source       `json:"sources"`
	Profile          *ConfigProfile `json:"config_profile,omitempty"`
}

// Endpoint is one verified endpoint contract: what a preset stands for.
type Endpoint struct {
	PresetID   string   `json:"preset_id"`
	Protocol   string   `json:"protocol"` // chat-completions | messages | responses | generate-content
	RequestURL string   `json:"request_url"`
	AuthMethod string   `json:"auth_method"`        // bearer | anthropic-api-key | api-key | x-goog-api-key | oauth
	EnvVar     string   `json:"env_var"`            // default credential environment variable
	Compress   string   `json:"compress,omitempty"` // verified upstream request-body compression, if any
	Docs       []Source `json:"docs"`
}

// Variant is one named reasoning tier of a binding. Only verified knobs are
// recorded; the config adapter maps them onto its own variant types.
type Variant struct {
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ThinkingType    string `json:"thinking_type,omitempty"`
	ThinkingEffort  string `json:"thinking_effort,omitempty"`
}

// ResponsesContract records optional Responses fields that a verified binding
// sends by default. These defaults do not assert that omitted fields are
// rejected. A nil field leaves the normal protocol default in effect.
type ResponsesContract struct {
	SendStore             *bool `json:"send_store,omitempty"`
	SendReasoningInclude  *bool `json:"send_reasoning_include,omitempty"`
	SendToolChoice        *bool `json:"send_tool_choice,omitempty"`
	SendPromptCacheKey    *bool `json:"send_prompt_cache_key,omitempty"`
	SendMaxOutputTokens   *bool `json:"send_max_output_tokens,omitempty"`
	SendParallelToolCalls *bool `json:"send_parallel_tool_calls,omitempty"`
}

// Binding ties one endpoint to one wire model ID served under that endpoint,
// with the endpoint-specific defaults (variants) verified for the route.
type Binding struct {
	Endpoint        string             `json:"endpoint"` // preset ID
	WireModelID     string             `json:"wire_model_id"`
	ModelID         string             `json:"model_id"`
	Variants        map[string]Variant `json:"variants,omitempty"`
	Responses       *ResponsesContract `json:"responses,omitempty"`
	Limit           *LimitOverride     `json:"limit,omitempty"`
	InputModalities []string           `json:"input_modalities,omitempty"`
	Profile         *ConfigProfile     `json:"config_profile,omitempty"`
}

// CatalogSource records which upstream revision a catalog snapshot was
// generated from. The embedded snapshot records the chord-models tag it was
// synced from; the refresh cache records the tag it was fetched from.
type CatalogSource struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"` // upstream tag the snapshot was pinned to
}

// Catalog is the validated in-memory form of the embedded data.
type Catalog struct {
	Version   string            `json:"version"`
	Source    *CatalogSource    `json:"source,omitempty"`
	Endpoints []Endpoint        `json:"endpoints"`
	Models    []ModelFacts      `json:"models"`
	Bindings  []Binding         `json:"bindings"`
	byPreset  map[string]int    `json:"-"`
	byModelID map[string]int    `json:"-"`
	byBinding map[[2]string]int `json:"-"`
}

func mustLoadCatalog() *Catalog {
	c, err := loadCatalog(catalogData)
	if err != nil {
		panic(fmt.Sprintf("modelcatalog: embedded catalog invalid: %v", err))
	}
	return c
}

func loadCatalog(data []byte) (*Catalog, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var c Catalog
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	c.buildIndexes()
	return &c, nil
}

var validProtocols = map[string]bool{
	"chat-completions": true,
	"messages":         true,
	"responses":        true,
	"generate-content": true,
}

var validAuthMethods = map[string]bool{
	"bearer":            true,
	"anthropic-api-key": true,
	"api-key":           true,
	"x-goog-api-key":    true,
	"oauth":             true,
}

var validModalities = map[string]bool{
	"text":  true,
	"image": true,
	"pdf":   true,
	"audio": true,
	"video": true,
}

// Validate re-runs the structural checks enforced at load time. The code
// generator calls it so a source file that fails these checks never ships.
func (c *Catalog) Validate() error {
	return c.validate()
}

func (c *Catalog) validate() error {
	if strings.TrimSpace(c.Version) == "" {
		return fmt.Errorf("catalog version is required")
	}
	if c.Source != nil {
		if err := validateProvenanceURL(c.Source.Repository); err != nil {
			return fmt.Errorf("catalog source repository: %w", err)
		}
		if strings.TrimSpace(c.Source.Revision) == "" {
			return fmt.Errorf("catalog source revision is required when a source is recorded")
		}
	}
	c.byPreset = make(map[string]int, len(c.Endpoints))
	for i, e := range c.Endpoints {
		if strings.TrimSpace(e.PresetID) == "" {
			return fmt.Errorf("endpoint %d: preset_id is required", i)
		}
		if _, dup := c.byPreset[e.PresetID]; dup {
			return fmt.Errorf("endpoint %q: duplicate preset_id", e.PresetID)
		}
		if !validProtocols[e.Protocol] {
			return fmt.Errorf("endpoint %q: unknown protocol %q", e.PresetID, e.Protocol)
		}
		if !validAuthMethods[e.AuthMethod] {
			return fmt.Errorf("endpoint %q: unknown auth method %q", e.PresetID, e.AuthMethod)
		}
		u, err := url.Parse(e.RequestURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("endpoint %q: invalid request URL %q", e.PresetID, e.RequestURL)
		}
		if strings.TrimSpace(e.EnvVar) == "" && e.AuthMethod != "oauth" {
			return fmt.Errorf("endpoint %q: env_var is required for non-OAuth endpoints", e.PresetID)
		}
		if e.Compress != "" && e.Compress != "gzip" && e.Compress != "zstd" {
			return fmt.Errorf("endpoint %q: unknown request compression %q", e.PresetID, e.Compress)
		}
		for _, s := range e.Docs {
			if err := validateMetadataSource(s); err != nil {
				return fmt.Errorf("endpoint %q documentation: %w", e.PresetID, err)
			}
		}
		c.byPreset[e.PresetID] = i
	}
	c.byModelID = make(map[string]int, len(c.Models))
	for i, m := range c.Models {
		if err := validateModelMetadata(m); err != nil {
			return err
		}
		if strings.TrimSpace(m.ID) == "" {
			return fmt.Errorf("model %d: id is required", i)
		}
		if _, dup := c.byModelID[m.ID]; dup {
			return fmt.Errorf("model %q: duplicate id", m.ID)
		}
		if m.Context <= 0 || m.Output <= 0 {
			return fmt.Errorf("model %q: context and output must be positive", m.ID)
		}
		if m.Input < 0 || (m.Input > 0 && m.Input > m.Context) {
			return fmt.Errorf("model %q: input %d exceeds context %d", m.ID, m.Input, m.Context)
		}
		for _, mod := range m.InputModalities {
			if !validModalities[mod] {
				return fmt.Errorf("model %q: unknown modality %q", m.ID, mod)
			}
		}
		if len(m.Sources) == 0 {
			return fmt.Errorf("model %q: at least one verification source is required", m.ID)
		}
		if err := m.Profile.validate(m.ID); err != nil {
			return err
		}
		for _, s := range m.Sources {
			if err := validateMetadataSource(s); err != nil {
				return fmt.Errorf("model %q source: %w", m.ID, err)
			}
		}
		c.byModelID[m.ID] = i
	}
	c.byBinding = make(map[[2]string]int, len(c.Bindings))
	for i, b := range c.Bindings {
		if _, ok := c.byPreset[b.Endpoint]; !ok {
			return fmt.Errorf("binding %d: unknown endpoint %q", i, b.Endpoint)
		}
		if _, ok := c.byModelID[b.ModelID]; !ok {
			return fmt.Errorf("binding %s/%s: unknown model %q", b.Endpoint, b.WireModelID, b.ModelID)
		}
		if strings.TrimSpace(b.WireModelID) == "" {
			return fmt.Errorf("binding %s: wire_model_id is required", b.ModelID)
		}
		key := [2]string{b.Endpoint, b.WireModelID}
		if _, dup := c.byBinding[key]; dup {
			return fmt.Errorf("binding %s/%s: duplicate wire model ID", b.Endpoint, b.WireModelID)
		}
		c.byBinding[key] = i
		if err := validateBindingOverrides(b, c.Models[c.byModelID[b.ModelID]]); err != nil {
			return err
		}
		if err := b.Profile.validate(b.ModelID); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) buildIndexes() {
	if c.byPreset == nil {
		c.byPreset = make(map[string]int, len(c.Endpoints))
		for i, e := range c.Endpoints {
			c.byPreset[e.PresetID] = i
		}
		c.byModelID = make(map[string]int, len(c.Models))
		for i, m := range c.Models {
			c.byModelID[m.ID] = i
		}
		c.byBinding = make(map[[2]string]int, len(c.Bindings))
		for i, b := range c.Bindings {
			c.byBinding[[2]string{b.Endpoint, b.WireModelID}] = i
		}
	}
}

// Version returns the version of the catalog the process resolves against.
func Version() string { return effective().Version }

// EndpointContract returns the contract recorded for a preset ID.
func EndpointContract(presetID string) (Endpoint, bool) {
	catalog := effective()
	i, ok := catalog.byPreset[presetID]
	if !ok {
		return Endpoint{}, false
	}
	return cloneEndpoint(catalog.Endpoints[i]), true
}

// EndpointContracts lists all verified endpoint contracts, ordered by preset ID.
func EndpointContracts() []Endpoint {
	catalog := effective()
	out := make([]Endpoint, len(catalog.Endpoints))
	for i, endpoint := range catalog.Endpoints {
		out[i] = cloneEndpoint(endpoint)
	}
	slices.SortFunc(out, func(a, b Endpoint) int { return strings.Compare(a.PresetID, b.PresetID) })
	return out
}

// Model returns the facts recorded for a stable catalog model ID.
func Model(id string) (ModelFacts, bool) {
	catalog := effective()
	i, ok := catalog.byModelID[id]
	if !ok {
		return ModelFacts{}, false
	}
	return cloneModelFacts(catalog.Models[i]), true
}

// BindingFacts resolves endpoint overrides without changing the model identity.
func BindingFacts(b Binding) (ModelFacts, bool) {
	facts, ok := Model(b.ModelID)
	return b.ApplyTo(facts), ok
}

// LookupBinding resolves the binding for one wire model ID served by a preset.
func LookupBinding(presetID, wireModelID string) (Binding, bool) {
	catalog := effective()
	i, ok := catalog.byBinding[[2]string{presetID, wireModelID}]
	if !ok {
		return Binding{}, false
	}
	return cloneBinding(catalog.Bindings[i]), true
}

// LookupBindingByModelID resolves a stable catalog model through a preset's
// verified binding. It is used for explicit catalog aliases whose wire model
// name differs from the catalog identity.
func LookupBindingByModelID(presetID, modelID string) (Binding, bool) {
	for _, b := range effective().Bindings {
		if b.Endpoint == presetID && b.ModelID == modelID {
			return cloneBinding(b), true
		}
	}
	return Binding{}, false
}

// BindingsForEndpoint lists a preset's bindings ordered by wire model ID.
func BindingsForEndpoint(presetID string) []Binding {
	var out []Binding
	for _, b := range effective().Bindings {
		if b.Endpoint == presetID {
			out = append(out, cloneBinding(b))
		}
	}
	slices.SortFunc(out, func(a, b Binding) int { return strings.Compare(a.WireModelID, b.WireModelID) })
	return out
}

// IsManagedPreset reports whether a preset ID is a catalog-managed endpoint
// contract. The codex preset keeps its own stricter handling in the config
// package but is also catalog-managed for model facts.
func IsManagedPreset(presetID string) bool {
	_, ok := effective().byPreset[presetID]
	return ok
}
