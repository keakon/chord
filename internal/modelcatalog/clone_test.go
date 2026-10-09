package modelcatalog

import (
	"encoding/json"
	"testing"
)

func TestCatalogAccessorsKeepSnapshotImmutable(t *testing.T) {
	previous := current()
	t.Cleanup(func() { effectiveStatePtr.Store(previous) })
	profile := &ConfigProfile{
		Model:      map[string]any{"thinking": map[string]any{"values": []any{"sample"}}},
		Compat:     map[string]any{"sample": map[string]any{"enabled": true}},
		Compaction: &CompactionProfile{Threshold: new(0.8)},
		Sources:    []Source{{URL: "https://example.invalid/profile", Checked: "2026-10-01"}},
	}
	source := Source{URL: "https://example.invalid/model", Checked: "2026-10-01"}
	catalog := &Catalog{
		Version:   "2026-10-01.1",
		Source:    &CatalogSource{Repository: "https://example.invalid/catalog", Revision: "v2026-10-01.1"},
		Endpoints: []Endpoint{{PresetID: "sample", Docs: []Source{source}}},
		Models: []ModelFacts{{
			ID: "sample/model", Sources: []Source{source}, CodingSources: []Source{source},
			InputModalities: []string{"text"}, ReasoningOptions: []string{"high"}, Profile: profile,
			Connection: &Connection{Sources: []Source{source}}, Cost: &Cost{InputPerMillion: 1},
		}},
		Bindings: []Binding{{
			Endpoint: "sample", WireModelID: "test-model", ModelID: "sample/model",
			Variants:  map[string]Variant{"high": {ReasoningEffort: "high"}},
			Responses: &ResponsesContract{SendStore: new(false)}, Limit: &LimitOverride{Output: new(100)},
			InputModalities: []string{"text"}, Profile: profile,
		}},
	}
	catalog.buildIndexes()
	candidates := []Candidate{{WireModelID: "candidate", Sources: []Source{source}, CodingSources: []Source{source}, InputModalities: []string{"text"}}}
	effectiveStatePtr.Store(&effectiveState{catalog: catalog, candidates: candidates})
	state := &CacheFile{Catalog: catalog, Candidates: candidates}
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	mutateProfile := func(p *ConfigProfile) {
		p.Model["thinking"].(map[string]any)["values"].([]any)[0] = "changed"
		p.Compat["sample"].(map[string]any)["enabled"] = false
		*p.Compaction.Threshold = 0.1
		p.Sources[0].URL = "changed"
	}
	mutateModel := func(m ModelFacts) {
		m.Sources[0].URL = "changed"
		m.CodingSources[0].URL = "changed"
		m.InputModalities[0] = "changed"
		m.ReasoningOptions[0] = "changed"
		m.Connection.Sources[0].URL = "changed"
		m.Cost.InputPerMillion = 100
		mutateProfile(m.Profile)
	}
	mutateBinding := func(b Binding) {
		b.Variants["high"] = Variant{ReasoningEffort: "changed"}
		*b.Responses.SendStore = true
		*b.Limit.Output = 200
		b.InputModalities[0] = "changed"
		mutateProfile(b.Profile)
	}
	for name, mutate := range map[string]func(){
		"origin":           func() { OriginInfo().Source.Revision = "changed" },
		"endpoint":         func() { e, _ := EndpointContract("sample"); e.Docs[0].URL = "changed" },
		"endpoints":        func() { EndpointContracts()[0].Docs[0].URL = "changed" },
		"model":            func() { m, _ := Model("sample/model"); mutateModel(m) },
		"binding":          func() { b, _ := LookupBinding("sample", "test-model"); mutateBinding(b) },
		"binding by model": func() { b, _ := LookupBindingByModelID("sample", "sample/model"); mutateBinding(b) },
		"bindings":         func() { mutateBinding(BindingsForEndpoint("sample")[0]) },
		"candidates": func() {
			c := EffectiveCandidates()[0]
			c.Sources[0].URL = "changed"
			c.CodingSources[0].URL = "changed"
			c.InputModalities[0] = "changed"
		},
		"merged profile": func() {
			mutateProfile(MergeConfigProfiles(profile, nil))
			mutateProfile(MergeConfigProfiles(nil, profile))
		},
	} {
		t.Run(name, func(t *testing.T) {
			mutate()
			after, err := json.Marshal(state)
			if err != nil || string(after) != string(before) {
				t.Fatalf("accessor changed the shared snapshot: err=%v", err)
			}
		})
	}
}
