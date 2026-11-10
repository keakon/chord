package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/jsonx"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCheckpointConstraintsRequireLiveUserSource(t *testing.T) {
	const quote = "Use the standard library."
	for _, tc := range []struct {
		name    string
		history []message.Message
	}{
		{"missing", nil},
		{"assistant", []message.Message{{Role: message.RoleAssistant, Content: quote}}},
		{"tool", []message.Message{{Role: message.RoleTool, Content: quote}}},
		{"notice", []message.Message{{Role: message.RoleUser, Kind: message.KindLoopNotice, Content: quote}}},
		{"file", []message.Message{{Role: message.RoleUser, Content: "Inspect this file.\n<file path=\"sample.md\">\n" + quote + "\n</file>"}}},
		{"file part", []message.Message{{Role: message.RoleUser, Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "Inspect this file."},
			{Type: message.ContentPartText, Text: "<file path=\"sample.md\">\n" + quote + "\n</file>"},
		}}}},
		{"archived", []message.Message{
			{Role: message.RoleUser, Content: quote},
			{Role: message.RoleUser, IsCompactionSummary: true, Content: "Historical quote: " + quote},
		}},
		{"retired", []message.Message{{Role: message.RoleUser, IsCompactionSummary: true,
			Content: withCompactionAnchors("", compactionAnchors{SupersededConstraints: []string{quote}})}}},
		{"ambiguous", []message.Message{
			{Role: message.RoleUser, Content: "For the parser: " + quote},
			{Role: message.RoleUser, Content: "For the renderer: " + quote},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if selected, err := resolveCheckpointConstraints(tc.history, []string{quote}); err == nil || len(selected) != 0 {
				t.Fatalf("unverified constraint accepted: selected=%+v err=%v", selected, err)
			}
		})
	}
}

func TestCheckpointConstraintsPreserveCompleteConditions(t *testing.T) {
	const source = "For this parser only, use the standard library unless a dependency is approved."
	selected, err := resolveCheckpointConstraints([]message.Message{{Role: message.RoleUser, Content: source}}, []string{"use the standard library"})
	if err != nil || len(selected) != 1 || selected[0].Excerpt != source || selected[0].RequestSource != checkpointRequestSource(source) {
		t.Fatalf("complete source not preserved: selected=%+v err=%v", selected, err)
	}
	// The limit counts runes, not bytes, and a conditional instruction that
	// exceeds it must be rejected rather than promoted after truncation.
	for _, tc := range []struct {
		name, quote, source string
		wantErr             bool
	}{
		{"ascii_at_limit", "xxx", strings.Repeat("x", compactAnchorsConstraintChars), false},
		{"ascii_over_limit", "xxx", strings.Repeat("x", compactAnchorsConstraintChars+1), true},
		{"cjk_at_limit", "保持", strings.Repeat("保持", compactAnchorsConstraintChars/2), false},
		{"cjk_over_limit", "保持", strings.Repeat("保持", compactAnchorsConstraintChars/2+1), true},
		{"conditional_over_limit", "Only for the parser", "Only for the parser, " + strings.Repeat("unless a dependency is approved; ", 12), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, err := resolveCheckpointConstraints([]message.Message{{Role: message.RoleUser, Content: tc.source}}, []string{tc.quote})
			if (err != nil) != tc.wantErr {
				t.Fatalf("source length %d runes: err=%v", len([]rune(tc.source)), err)
			}
			if !tc.wantErr && (len(selected) != 1 || selected[0].Excerpt != tc.source) {
				t.Fatalf("complete source not preserved: %+v", selected)
			}
		})
	}
}

func TestCheckpointConstraintsSkipOriginalRequestSource(t *testing.T) {
	const original = "Inspect the parser before making changes."
	history := []message.Message{{Role: message.RoleUser, Content: original}}
	selected, err := resolveCheckpointConstraints(history, []string{"Inspect the parser"})
	if err != nil || len(selected) != 1 || selected[0].RequestSource != checkpointRequestSource(original) {
		t.Fatalf("selector did not resolve to the original request: selected=%+v err=%v", selected, err)
	}
	anchors := buildCompactionAnchors(compactionAnchors{}, original, checkpointConstraintEvidence(history, nil, selected))
	if len(anchors.Constraints) != 0 || anchors.OriginalRequest != message.CompactionAnchorLine(original, compactAnchorsRequestChars) {
		t.Fatalf("original request duplicated as a constraint: %+v", anchors)
	}
}

func TestCheckpointConstraintsFollowUserOrderAndDeduplicateSource(t *testing.T) {
	old := "never edit alpha.go"
	current := "edit alpha.go"
	history := []message.Message{
		{Role: message.RoleUser, Content: old},
		{Role: message.RoleUser, Content: current},
	}
	selected, err := resolveCheckpointConstraints(history, []string{current, old})
	if err != nil {
		t.Fatal(err)
	}
	anchors := buildCompactionAnchors(compactionAnchors{}, "", checkpointConstraintEvidence(history, nil, selected))
	if !slices.Equal(anchors.Constraints, []string{current}) {
		t.Fatalf("exact instruction or chronological ordering lost: %+v", anchors)
	}
	history[0].Content = "do not preserve the existing API"
	history[1].Content = "preserve the existing API"
	selected, err = resolveCheckpointConstraints(history, []string{history[0].Content})
	if err != nil {
		t.Fatal(err)
	}
	runtimeItem := buildStatedConstraintEvidence("runtime user message", history[1].Content)
	runtimeItem.Sequence = 10
	items := checkpointConstraintEvidence(history, []evidenceItem{runtimeItem}, selected)
	anchors = buildCompactionAnchors(compactionAnchors{}, "Inspect the parser.", items)
	if !slices.Equal(anchors.Constraints, []string{history[1].Content}) ||
		!slices.Equal(anchors.SupersededConstraints, []string{history[0].Content}) {
		t.Fatalf("model replay overrode newer user direction: %+v", anchors)
	}

	history = nil
	var runtime []evidenceItem
	var quotes []string
	for i := range compactAnchorsMaxConstraints {
		source := fmt.Sprintf("Please inspect module %02d; do not edit file%02d.go.", i, i)
		history = append(history, message.Message{Role: message.RoleUser, Content: source})
		item := buildUserCorrectionEvidence("runtime user message", source)
		item.Sequence = i
		runtime = append(runtime, item)
		if i < 8 {
			quotes = append(quotes, fmt.Sprintf("do not edit file%02d.go", i))
		}
	}
	selected, err = resolveCheckpointConstraints(history, quotes)
	if err != nil {
		t.Fatal(err)
	}
	before := buildCompactionAnchors(compactionAnchors{}, "Inspect the parser.", runtime)
	after := buildCompactionAnchors(compactionAnchors{}, "Inspect the parser.", checkpointConstraintEvidence(history, runtime, selected))
	if renderCompactionAnchors(before) != renderCompactionAnchors(after) || after.OmittedNote != "" {
		t.Fatalf("same-source selections displaced existing constraints:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestCheckpointConstraintsReactivationNeedsNewInstruction(t *testing.T) {
	const old = "do not preserve the existing API"
	const current = "preserve the existing API"
	previous := compactionAnchors{Constraints: []string{current}, SupersededConstraints: []string{old}}
	history := []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: withCompactionAnchors("", previous)}}
	if _, err := resolveCheckpointConstraints(history, []string{old}); err == nil {
		t.Fatal("retired instruction reactivated without a new user source")
	}
	selected, err := resolveCheckpointConstraints(history, []string{current})
	if err != nil || len(selected) != 0 {
		t.Fatalf("active restatement must be a no-op: selected=%+v err=%v", selected, err)
	}
	history = append(history, message.Message{Role: message.RoleUser, Content: old})
	selected, err = resolveCheckpointConstraints(history, []string{old})
	if err != nil {
		t.Fatal(err)
	}
	anchors := buildCompactionAnchors(previous, "", checkpointConstraintEvidence(history, nil, selected))
	if !slices.Equal(anchors.Constraints, []string{old}) || len(verifyAnchorsCoherence(anchors)) != 0 {
		t.Fatalf("new instruction failed to restore a coherent direction: %+v", anchors)
	}
	var items []evidenceItem
	for i, text := range []string{old, current, old} {
		item := buildStatedConstraintEvidence("runtime user message", text)
		item.Sequence = i
		items = append(items, item)
	}
	anchors = buildCompactionAnchors(compactionAnchors{}, "", items)
	if !slices.Equal(anchors.Constraints, []string{old}) || len(verifyAnchorsCoherence(anchors)) != 0 {
		t.Fatalf("A to B to A replay is incoherent: %+v", anchors)
	}
}

func TestCheckpointConstraintsReactivateOriginalRequest(t *testing.T) {
	const original = "do not preserve the existing API"
	const opposite = "preserve the existing API"
	for _, checkpointed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_window", true: "after_checkpoint"}[checkpointed], func(t *testing.T) {
			history := []message.Message{
				{Role: message.RoleUser, Content: original},
				{Role: message.RoleUser, Content: opposite},
			}
			previous := compactionAnchors{}
			if checkpointed {
				previous = buildCompactionAnchors(previous, original, []evidenceItem{
					buildStatedConstraintEvidence("runtime user message", opposite),
				})
				history = []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: withCompactionAnchors("", previous)}}
				if _, err := resolveCheckpointConstraints(history, []string{original}); err == nil {
					t.Fatal("a retired original request must require a new user source")
				}
				unchanged := buildCompactionAnchors(previous, original, nil)
				if !slices.Equal(unchanged.Constraints, []string{opposite}) ||
					!slices.Equal(unchanged.SupersededConstraints, []string{original}) {
					t.Fatalf("original request reactivated without evidence: %+v", unchanged)
				}
			}
			history = append(history, message.Message{Role: message.RoleUser, Content: original})
			a := newTestMainAgent(t, t.TempDir())
			a.resetRuntimeEvidenceFromMessages(history)
			items := a.evidenceItemsForCompaction(128000)
			// The same fold serves automatically detected constraints and
			// model-selected sources.
			runtimeAnchors := buildCompactionAnchors(previous, original, items)
			if len(runtimeAnchors.Constraints) != 0 || runtimeAnchors.OriginalRequest != original ||
				!slices.Equal(runtimeAnchors.SupersededConstraints, []string{opposite}) {
				t.Fatalf("runtime restatement lost: %+v", runtimeAnchors)
			}
			// Remove automatic detection of the repeated instruction so the
			// model selector alone must supply its new source.
			items = slices.DeleteFunc(items, func(item evidenceItem) bool {
				return item.RequestSource == checkpointRequestSource(original)
			})
			req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
				ActiveObjective: "Inspect the parser.", NextStep: "Read the parser.", Constraints: []string{original},
			}}
			bundle := modelDrivenBarrierSnapshot{
				snapshot: history, originalRequest: original, evidenceItems: items,
				sessionDir: t.TempDir(), workDir: t.TempDir(),
			}
			builder := a.newModelDrivenCheckpointBuilder(context.Background(), bundle, history, len(history), req)
			anchors := parseCompactionAnchors(message.CompactionAnchorsSection(builder.summaryText))
			if len(anchors.Constraints) != 0 || anchors.OriginalRequest != original ||
				!slices.Equal(anchors.SupersededConstraints, []string{opposite}) {
				t.Fatalf("selected restatement lost: %+v", anchors)
			}
			next := buildCompactionAnchors(anchors, original, nil)
			if len(next.Constraints) != 0 || next.OriginalRequest != original ||
				!slices.Equal(next.SupersededConstraints, []string{opposite}) {
				t.Fatalf("restored original did not carry forward without duplication: %+v", next)
			}
		})
	}
}

func TestCheckpointConstraintsOriginalRestatementWithoutLiveSource(t *testing.T) {
	for _, original := range []string{"preserve the existing API", strings.Repeat("x", compactAnchorsRequestChars)} {
		anchors := compactionAnchors{OriginalRequest: original}
		history := []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: withCompactionAnchors("", anchors)}}
		if selected, err := resolveCheckpointConstraints(history, []string{original}); err != nil || len(selected) != 0 {
			t.Fatalf("active original restatement should be a no-op: selected=%+v err=%v", selected, err)
		}
		anchors.SupersededConstraints = []string{message.CompactionAnchorLine(original, compactAnchorsConstraintChars)}
		history[0].Content = withCompactionAnchors("", anchors)
		if _, err := resolveCheckpointConstraints(history, []string{original}); err == nil {
			t.Fatal("retired original accepted without a new user source")
		}
	}
}

func TestCheckpointConstraintSelectionOrderAndOverflow(t *testing.T) {
	var history []message.Message
	var runtime []evidenceItem
	var quotes []string
	for i := range 20 {
		text := fmt.Sprintf("Use library%02d for module%02d.", i, i)
		history = append(history, message.Message{Role: message.RoleUser, Content: text})
		if i < 12 {
			item := buildStatedConstraintEvidence("runtime user message", text)
			item.Sequence = i
			runtime = append(runtime, item)
		} else {
			quotes = append(quotes, text)
		}
	}
	slices.Reverse(quotes)
	selected, err := resolveCheckpointConstraints(history, quotes)
	if err != nil {
		t.Fatal(err)
	}
	anchors := buildCompactionAnchors(compactionAnchors{}, "", checkpointConstraintEvidence(history, runtime, selected))
	var want []string
	for _, msg := range append(slices.Clone(history[:4]), history[12:]...) {
		want = append(want, msg.Content)
	}
	if !slices.Equal(anchors.Constraints, want) || !strings.Contains(anchors.OmittedNote, "8 earlier") {
		t.Fatalf("unexpected bound or order: %+v", anchors)
	}
}

func TestModelDrivenConstraintsCarrySourcesAcrossFallback(t *testing.T) {
	const original = "Inspect the parser."
	const source = "Use the standard library for this parser."
	history := []message.Message{{Role: message.RoleUser, Content: original}, {Role: message.RoleUser, Content: source}}
	bundle := modelDrivenBarrierSnapshot{snapshot: history, originalRequest: original, sessionDir: t.TempDir(), workDir: t.TempDir()}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: original, NextStep: "Read the parser.", Constraints: []string{"Use the standard library"},
	}}
	a := &MainAgent{}
	builder := a.newModelDrivenCheckpointBuilder(context.Background(), bundle, history, len(history), req)
	anchors := parseCompactionAnchors(message.CompactionAnchorsSection(builder.summaryText))
	if !slices.Equal(anchors.Constraints, []string{source}) {
		t.Fatalf("missing verified constraint: %+v", anchors)
	}
	key := checkpointConstraintSourceKey(source)
	if builder.requestSources[key] != checkpointRequestSource(source) {
		t.Fatalf("missing source: %v", builder.requestSources)
	}
	checkpoint := message.Message{Role: message.RoleUser, IsCompactionSummary: true,
		Content: builder.summaryText, CompactionSummaryMode: compactionSummaryModeModelDriven, CompactionRequestSources: builder.requestSources}
	projected := projectCheckpointRequests([]message.Message{checkpoint})
	if len(parseCompactionAnchors(message.CompactionAnchorsSection(projected[0].Content)).Constraints) != 0 ||
		!strings.Contains(checkpoint.Content, "- "+source) {
		t.Fatal("request folding must remove only the projected duplicate")
	}
	fallbackAnchors := buildCompactionAnchors(latestCompactionAnchors([]message.Message{checkpoint}), original, nil)
	fallback := message.Message{Role: message.RoleUser, IsCompactionSummary: true,
		Content: withCompactionAnchors("Continue the parser work.", fallbackAnchors)}
	fallback.CompactionRequestSources = buildCheckpointRequestSources(fallback.Content, []message.Message{checkpoint}, original, nil)
	history = []message.Message{fallback, {Role: message.RoleUser, Content: "Continue."}}
	req.Args.Constraints = nil
	bundle.snapshot = history
	next := a.newModelDrivenCheckpointBuilder(context.Background(), bundle, history, len(history), req)
	if !slices.Equal(latestCompactionAnchors([]message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: next.summaryText}}).Constraints, []string{source}) ||
		next.requestSources[key] != checkpointRequestSource(source) {
		t.Fatalf("constraint or source lost after fallback: %s", next.summaryText)
	}
}

func TestTryArmCheckpointRejectsUnverifiedConstraint(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	const callID = "checkpoint"
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "Inspect the parser."})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(callID, tools.NameCompactContext)}})
	raw, err := jsonx.Marshal(tools.CompactContextArgs{ActiveObjective: "Inspect the parser.", NextStep: "Read the parser.", Constraints: []string{"Use a new dependency."}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.tryArmModelDrivenCheckpoint(callID, string(raw)); err == nil || !strings.Contains(err.Error(), "constraints[0]") || a.pendingModelDriven != nil {
		t.Fatalf("unverified checkpoint armed: err=%v pending=%+v", err, a.pendingModelDriven)
	}
}
