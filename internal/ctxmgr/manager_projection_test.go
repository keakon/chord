package ctxmgr

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestProjectionSeparatesObservedAndFrozenOutput(t *testing.T) {
	for _, observed := range []bool{true, false} {
		t.Run(map[bool]string{true: "observed", false: "frozen"}[observed], func(t *testing.T) {
			m := NewManagerWithTokenBudgets(10000, 10000, 10000, 0, 0.8)
			m.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("a", 300)})
			usage := message.TokenUsage{InputTokens: 100, OutputTokens: 20}
			m.UpdateFromUsage(usage)
			if !observed {
				m.NoteMissingUsage()
				usage = message.TokenUsage{}
			}
			baseline := m.EffectiveContextTokens()
			// Content that arrived after usage but before response settlement
			// must remain outside the charged prefix.
			m.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("b", 90)})
			m.AppendResponse(message.Message{
				Role: message.RoleAssistant, Content: strings.Repeat("c", 60), Usage: &usage,
			})
			want := baseline + 30
			if !observed {
				want += 20
			}
			if got := m.AutoCompactDecision().ProjectedContextTokens; got != want {
				t.Fatalf("projection = %d, want %d", got, want)
			}
			if got := m.EffectiveContextTokens(); got != baseline {
				t.Fatalf("projection changed the reading: %d != %d", got, baseline)
			}
			m.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("d", 300)})
			if got := m.AutoCompactDecision().ProjectedContextTokens; got != want+100 {
				t.Fatalf("tool growth projection = %d, want %d", got, want+100)
			}
		})
	}
}

func TestProjectionIsAdvisoryAndRebasesOnFreshUsage(t *testing.T) {
	m := NewManagerWithTokenBudgets(1000, 1000, 1000, 0, 0.8)
	m.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("a", 300)})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 100})
	if got := m.AutoCompactDecision().ProjectedContextTokens; got != 100 {
		t.Fatalf("projection without growth = %d", got)
	}
	m.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("b", 3000)})
	decision := m.AutoCompactDecision()
	if decision.ProjectedContextTokens != 1100 || decision.ShouldCompact || decision.EffectiveInputTokens != 100 {
		t.Fatalf("projection must not arm compaction: %+v", decision)
	}
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 500})
	if got := m.AutoCompactDecision().ProjectedContextTokens; got != 500 {
		t.Fatalf("new usage retained old growth: %d", got)
	}
}

func TestProjectionChargesImagesWithoutPayloadByteInflation(t *testing.T) {
	m := NewManager(100000, 0.8)
	m.Append(message.Message{Role: message.RoleUser, Parts: []message.ContentPart{{Type: message.ContentPartImage}}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 200})
	if m.CalibratedRatio() != 0 {
		t.Fatal("mixed usage must not create a text sample")
	}
	m.Append(message.Message{Role: message.RoleTool, Parts: []message.ContentPart{
		{Type: message.ContentPartText, Text: strings.Repeat("a", 90)},
		{Type: message.ContentPartImage, Data: make([]byte, 100000)},
	}})
	want := 200 + 30 + message.ImagePartEstimateTokens
	if got := m.AutoCompactDecision().ProjectedContextTokens; got != want {
		t.Fatalf("image projection = %d, want %d", got, want)
	}
}

func TestProjectionInvalidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		invalidate func(*Manager)
	}{
		{"clear", (*Manager).ClearLastTokenUsage},
		{"model", (*Manager).InvalidateSizeObservation},
		{"restore", func(m *Manager) { m.RestoreMessages(m.Snapshot()) }},
		{"restored_reading", func(m *Manager) { m.RestoreContextReading(500) }},
		{"rewrite", func(m *Manager) { _ = m.ReplacePrefixAtomic(1, nil, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(1000, 0.8)
			m.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("a", 300)})
			m.UpdateFromUsage(message.TokenUsage{InputTokens: 100})
			tc.invalidate(m)
			m.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("b", 3000)})
			if got := m.AutoCompactDecision().ProjectedContextTokens; got != 0 {
				t.Fatalf("invalidated projection = %d", got)
			}
		})
	}
	m := NewManager(1000, 0.8)
	m.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("a", 3000)})
	m.NoteMissingUsage()
	if got := m.AutoCompactDecision().ProjectedContextTokens; got != 0 {
		t.Fatalf("unknown reading produced a projection: %d", got)
	}

	// A fresh usage reading establishes a new basis after invalidation: the
	// growth accumulated before it is already part of that reading, not a
	// pending projection, and later appends are charged against the new basis.
	m = NewManager(1000, 0.8)
	m.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("a", 300)})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 100})
	m.ClearLastTokenUsage()
	m.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("b", 300)})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 200})
	if got := m.AutoCompactDecision().ProjectedContextTokens; got != 200 {
		t.Fatalf("new reading inherited invalidated growth: %d", got)
	}
	m.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("d", 300)})
	if got := m.AutoCompactDecision().ProjectedContextTokens; got != 300 {
		t.Fatalf("new basis did not charge fresh growth: %d", got)
	}
}
