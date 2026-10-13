package tui

import (
	"encoding/json"
	"fmt"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/contextdiag"
)

// This explicit envelope is the export boundary. Never marshal the agent view
// directly: its binding and names are private, even without message bodies.
func exportContextDiagnostics(view agent.ContextDiagnosticsView) []byte {
	type exportSnapshot struct {
		WireFamily    string                 `json:"wire_family"`
		Transport     string                 `json:"transport"`
		Seq           uint64                 `json:"seq"`
		Attempt       uint64                 `json:"attempt"`
		Revision      uint64                 `json:"revision"`
		Agent         string                 `json:"agent"`
		Model         string                 `json:"model"`
		State         string                 `json:"state"`
		StartedAt     string                 `json:"started_at"`
		FinishedAt    string                 `json:"finished_at,omitempty"`
		EstimateScope string                 `json:"estimate_scope"`
		Estimator     string                 `json:"estimator"`
		Completeness  string                 `json:"completeness"`
		Categories    []contextdiag.Category `json:"categories"`
		Limitations   []string               `json:"limitations"`
		Reduction     contextdiag.Reduction  `json:"reduction"`
		Usage         contextdiag.Usage      `json:"usage"`
		Cache         contextdiag.Cache      `json:"cache"`
	}
	envelope := struct {
		SchemaVersion     int             `json:"schema_version"`
		Available         bool            `json:"available"`
		Historical        bool            `json:"historical"`
		UnavailableReason string          `json:"unavailable_reason,omitempty"`
		Snapshot          *exportSnapshot `json:"snapshot,omitempty"`
	}{SchemaVersion: 1, Available: view.Available, Historical: view.Historical}
	if !view.Available {
		if view.UnavailableReason == "unsupported" {
			envelope.UnavailableReason = "unsupported"
		} else {
			envelope.UnavailableReason = "not_observed"
		}
	}
	if view.Available {
		s := contextdiag.Clone(view.Snapshot)
		names := map[string]map[string]string{}
		anonymize := func(kind, label string) string {
			if names[kind] == nil {
				names[kind] = map[string]string{}
			}
			if name, ok := names[kind][label]; ok {
				return name
			}
			name := fmt.Sprintf("%s-%d", kind, len(names[kind])+1)
			names[kind][label] = name
			return name
		}
		for i := range s.Categories {
			for j := range s.Categories[i].Sources {
				source := &s.Categories[i].Sources[j]
				kind := source.Kind
				if kind != contextdiag.Tool && kind != contextdiag.Skill {
					kind = "source"
				}
				source.Label = anonymize(kind, source.Label)
			}
		}
		for i := range s.Reduction.Rules {
			if s.Reduction.Rules[i].Tool != "" {
				s.Reduction.Rules[i].Tool = anonymize(contextdiag.Tool, s.Reduction.Rules[i].Tool)
			}
		}
		scope := "main"
		if s.ID.Binding.TaskID != "main" {
			scope = "worker"
		}
		started, finished := s.StartedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"), ""
		if !s.FinishedAt.IsZero() {
			finished = s.FinishedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")
		}
		envelope.Snapshot = &exportSnapshot{WireFamily: s.WireFamily, Transport: s.Transport, Seq: s.ID.Seq, Attempt: s.ID.Attempt, Revision: s.Revision, Agent: scope + "/agent-1", Model: "model-1", State: s.State, StartedAt: started, FinishedAt: finished, EstimateScope: s.EstimateScope, Estimator: s.Estimator, Completeness: s.Completeness, Categories: s.Categories, Limitations: s.Limitations, Reduction: s.Reduction, Usage: s.Usage, Cache: s.Cache}
	}
	data, _ := json.MarshalIndent(envelope, "", "  ")
	return data
}

func (m *Model) exportFocusedContextDiagnostics() []byte {
	if m.mode == ModeContext {
		return exportContextDiagnostics(m.contextOverlay.view)
	}
	return exportContextDiagnostics(m.readContextDiagnostics())
}
