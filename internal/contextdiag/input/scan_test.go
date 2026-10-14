package input

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func diagnosticCategory(t *testing.T, categories []contextdiag.Category, kind string) contextdiag.Category {
	t.Helper()
	for _, c := range categories {
		if c.Kind == kind {
			return c
		}
	}
	t.Fatalf("missing category %s", kind)
	return contextdiag.Category{}
}
func TestDiagnosticEstimateUniqueCarriersAndSources(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: strings.Repeat("mirror", 100), Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "abc"}, {Type: message.ContentPartImage, Data: make([]byte, 1000)}}},
		{Role: message.RoleUser, Content: "env", DiagnosticSource: message.DiagnosticSourceInstructions},
		{Role: message.RoleUser, Content: "run", Kind: message.KindTurnOverlay},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "skill-1", Name: toolname.Skill, Args: json.RawMessage(`{"name":"sample"}`)}}},
		{Role: message.RoleTool, ToolCallID: "skill-1", Content: "loaded", ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleTool, ToolCallID: "missing", Content: "missing"},
		{Role: message.RoleAssistant, Content: strings.Repeat("mirror", 100), ResponsesOutput: []message.ResponsesOutputItem{{Type: "message"}}},
		{Role: message.RoleUser, Content: "local", Kind: message.KindQuestionState},
	}
	categories, limitations, complete := estimateDiagnosticInput(newDiagnosticBudget(context.Background()), "system", msgs, nil, Options{})
	if complete != contextdiag.Partial || !slices.Contains(limitations, "local_state_in_projection") {
		t.Fatal("unknown content presented as complete")
	}
	if c := diagnosticCategory(t, categories, contextdiag.Attachments); c.Estimate.Tokens != 0 || c.Estimate.UnknownItems != 1 {
		t.Fatalf("binary estimated: %+v", c)
	}
	if c := diagnosticCategory(t, categories, contextdiag.Instructions); c.Estimate.Tokens != 1 {
		t.Fatalf("environment classification: %+v", c)
	}
	results := diagnosticCategory(t, categories, contextdiag.ToolResults)
	if !slices.ContainsFunc(results.Sources, func(s contextdiag.Source) bool { return s.Kind == contextdiag.Skill && s.Label == "sample" }) {
		t.Fatalf("skill missing: %+v", results)
	}
	for _, c := range categories {
		sum := c.Other
		for _, source := range c.Sources {
			addEstimate(&sum, source.Estimate)
		}
		if sum != c.Estimate {
			t.Fatalf("non-additive category %s", c.Kind)
		}
	}
}
func TestDiagnosticDirectoryRulesAndSkillNotices(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleUser, Kind: message.KindDirectoryInstructionProjection, Content: "Follow the module instructions."},
		{Role: message.RoleUser, Kind: message.KindSkillDiscovery, Content: "The sample skill is available."},
	}
	categories, limitations, _ := estimateDiagnosticInput(newDiagnosticBudget(context.Background()), "", messages, nil, Options{})
	if slices.Contains(limitations, "source_unspecified") {
		t.Fatal("known directory and skill sources were not classified")
	}
	for _, kind := range []string{contextdiag.Instructions, contextdiag.Runtime} {
		if category := diagnosticCategory(t, categories, kind); category.Estimate.Tokens == 0 {
			t.Fatalf("missing source tokens: %+v", category)
		}
	}
}

func TestDiagnosticEstimateLimitsAndSchemas(t *testing.T) {
	schema := map[string]any{"enum": []any{"a", "b", "c"}}
	if _, ok := boundedDiagnosticJSON(schema); !ok {
		t.Fatal("valid enum rejected")
	}
	for _, v := range []any{map[string]any{"text": strings.Repeat("x", diagnosticSchemaBytes+1)}, make([]any, diagnosticSchemaNodes+1), func() {}} {
		if _, ok := boundedDiagnosticJSON(v); ok {
			t.Fatal("unbounded schema accepted")
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, ok := boundedDiagnosticJSON(cycle); ok {
		t.Fatal("cycle accepted")
	}
	defs := make([]message.ToolDefinition, 300)
	for i := range defs {
		defs[i] = message.ToolDefinition{Name: fmt.Sprintf("tool-%d", i), InputSchema: schema}
	}
	msgs := make([]message.Message, diagnosticMaxMessages+1)
	budget := newDiagnosticBudget(context.Background())
	budget.deadline = time.Now().Add(time.Hour)
	categories, limitations, complete := estimateDiagnosticInput(budget, "", msgs, defs, Options{})
	if complete != contextdiag.Partial || !slices.Contains(limitations, diagnosticScanIncomplete) || !slices.Contains(limitations, "sources_incomplete") {
		t.Fatal("limits not disclosed", limitations)
	}
	c := diagnosticCategory(t, categories, contextdiag.Tools)
	if len(c.Sources) != 5 || c.Other.Tokens == 0 {
		t.Fatal("sources not bounded")
	}
	b := newDiagnosticBudget(context.Background())
	b.deadline = time.Now().Add(-time.Second)
	baseline := buildDiagnosticBaseline(b, strings.Repeat("x", 100000), nil, nil)
	if baseline.systemOK || !b.incomplete {
		t.Fatal("time budget not enforced")
	}
	b = newDiagnosticBudget(context.Background())
	baseline = buildDiagnosticBaseline(b, strings.Repeat("x", diagnosticMaxHashBytes+1), nil, nil)
	if baseline.systemOK {
		t.Fatal("partial hash claimed complete")
	}
}
func TestDiagnosticCacheAllChangesAndPrefixShapes(t *testing.T) {
	build := func(system string, msgs []message.Message, defs []message.ToolDefinition) *Baseline {
		return buildDiagnosticBaseline(newDiagnosticBudget(context.Background()), system, msgs, defs)
	}
	a := message.Message{Role: message.RoleUser, Content: "aaa"}
	b := message.Message{Role: message.RoleUser, Content: "bbb"}
	old := build("system", []message.Message{a, b}, nil)
	for _, tc := range []struct {
		msgs  []message.Message
		state string
	}{{[]message.Message{a, b}, contextdiag.Same}, {[]message.Message{a, b, a}, contextdiag.Append}, {[]message.Message{a}, contextdiag.Truncate}, {[]message.Message{b, a}, contextdiag.Rewrite}} {
		result := Compare(old, build("system", tc.msgs, nil))
		if result.PrefixState != tc.state {
			t.Fatalf("got %s want %s", result.PrefixState, tc.state)
		}
	}
	c := Compare(old, build("changed", []message.Message{b}, []message.ToolDefinition{{Name: "sample"}}))
	if !reflect.DeepEqual(c.LocalChanges, []string{contextdiag.System, contextdiag.Tools, contextdiag.Messages}) || c.ExpectedUsable {
		t.Fatalf("lost simultaneous changes: %+v", c)
	}
	local := a
	local.InputID = "local"
	local.ToolStatus = message.ToolStatusError
	local.DiagnosticSource = message.DiagnosticSourceInstructions
	c = Compare(build("", []message.Message{a}, nil), build("", []message.Message{local}, nil))
	if c.PrefixState != contextdiag.Same {
		t.Fatal("local metadata changed prefix")
	}
	local.Parts = []message.ContentPart{{Type: message.ContentPartImage}}
	c = Compare(old, build("system", []message.Message{local}, nil))
	if c.PrefixState != contextdiag.Unknown || c.ExpectedUsable {
		t.Fatal("opaque comparison")
	}
}

func TestScanSharesBudgetAcrossEstimationAndFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		exhaust func(*diagnosticBudgetState)
	}{
		{"elapsed time", func(b *diagnosticBudgetState) { b.deadline = time.Now().Add(-time.Second) }},
		{"fragment count", func(b *diagnosticBudgetState) { b.fragments = diagnosticMaxFragments }},
		{"hash bytes", func(b *diagnosticBudgetState) { b.hashBytes = diagnosticMaxHashBytes }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scan := NewScan(context.Background(), "system", nil, nil, Options{})
			budget := scan.budget
			tc.exhaust(budget)
			result := scan.Finish(nil)
			if scan.budget != budget || result.Baseline.systemOK || result.Completeness != contextdiag.Partial || !slices.Contains(result.Limitations, diagnosticScanIncomplete) {
				t.Fatalf("fingerprinting reset or ignored the shared budget: %+v", result)
			}
		})
	}
}

func TestScanNativeDefinitionsRemainLazyAndShareCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stage := "estimate"
	var stages []string
	definitions := func() []map[string]any {
		stages = append(stages, stage)
		if len(stages) == 1 {
			cancel()
		}
		return []map[string]any{{"type": "sample"}}
	}
	// Enough fragments make the scan observe the supplier's cancellation during
	// message estimation, before deciding whether provider projection is allowed.
	messages := make([]message.Message, 128)
	for i := range messages {
		messages[i] = message.Message{Role: message.RoleUser, Content: "sample"}
	}
	scan := NewScan(ctx, "system", messages, nil, Options{NativeDefinitions: definitions})
	if scan.CanProject() || !reflect.DeepEqual(stages, []string{"estimate"}) {
		t.Fatalf("native estimation did not share cancellation: stages=%v, canProject=%v", stages, scan.CanProject())
	}
	stage = "fingerprint"
	result := scan.Finish(messages)
	if !reflect.DeepEqual(stages, []string{"estimate", "fingerprint"}) || result.Completeness != contextdiag.Partial || result.Baseline.toolsOK || result.Baseline.messagesOK {
		t.Fatalf("native evaluation order or partial evidence changed: stages=%v result=%+v", stages, result)
	}
}

func TestScanNativeSupplierFollowsLocalEstimation(t *testing.T) {
	budget := newDiagnosticBudget(context.Background())
	called := false
	options := Options{NativeDefinitions: func() []map[string]any {
		called = true
		if budget.fragments == 0 || budget.hashBytes != 0 {
			t.Fatalf("native definitions evaluated outside input estimation: %+v", budget)
		}
		return []map[string]any{{"type": "sample"}}
	}}
	estimateDiagnosticInput(budget, "system", nil, []message.ToolDefinition{{Name: "sample", InputSchema: map[string]any{"type": "object"}}}, options)
	if !called {
		t.Fatal("native definitions not estimated")
	}
}

func TestBaselineDerivationPreservesPublishedEvidence(t *testing.T) {
	messages := []message.Message{{Role: message.RoleUser, Content: "sample"}}
	baseline := NewScan(context.Background(), "system", messages, nil, Options{}).Finish(messages).Baseline
	original := *baseline
	original.messages = slices.Clone(original.messages)
	domain := [32]byte{1}
	scoped := baseline.ForDomain(domain)
	unknown := scoped.WithoutMessageComparison()
	if !reflect.DeepEqual(*baseline, original) || !scoped.messagesOK || unknown.messagesOK || unknown.domain != domain {
		t.Fatal("derived baseline mutated prior request evidence")
	}
	if Compare(baseline, baseline).PrefixState != contextdiag.Same || Compare(baseline, scoped).BaselineState != contextdiag.DomainChanged || Compare(scoped, unknown).PrefixState != contextdiag.Unknown {
		t.Fatal("baseline derivation lost domain or projection evidence")
	}
}

func TestScanReadsOverridesAfterNativeFingerprint(t *testing.T) {
	var observations []string
	scan := NewScan(context.Background(), "system", nil, nil, Options{
		NativeDefinitions: func() []map[string]any {
			observations = append(observations, "native")
			return []map[string]any{{"type": "sample"}}
		},
		Overrides: func() bool {
			observations = append(observations, "overrides")
			return true
		},
	})
	if !reflect.DeepEqual(observations, []string{"native"}) {
		t.Fatalf("configuration read before fingerprint: %v", observations)
	}
	// Exhausting the remaining budget also verifies the original limitation
	// ordering when overrides and incomplete projection both apply.
	scan.budget.deadline = time.Now().Add(-time.Second)
	result := scan.Finish(nil)
	if !reflect.DeepEqual(observations, []string{"native", "native", "overrides"}) {
		t.Fatalf("request evidence observed out of order: %v", observations)
	}
	want := []string{diagnosticOverhead, diagnosticProjection, diagnosticOverrides, diagnosticScanIncomplete, diagnosticProjection}
	if !reflect.DeepEqual(result.Limitations, want) {
		t.Fatalf("limitations=%v, want %v", result.Limitations, want)
	}
}
