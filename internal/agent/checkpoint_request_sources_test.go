package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCheckpointRequestProjectionPreservesCollidingSources(t *testing.T) {
	prefix := strings.Repeat("Keep parser defaults stable. ", 25)
	suffix := strings.Repeat(" Preserve all existing output fields.", 15)
	older := prefix + " Never update the public API. " + suffix
	current := prefix + " Update the public API now. " + suffix
	if compactTextSnippet(older, compactUserConstraintExcerptChars) != compactTextSnippet(current, compactUserConstraintExcerptChars) {
		t.Fatal("fixture must collide after truncation")
	}
	msg := requestDedupFixture(current, older, older, "Earlier instruction")
	got := projectCheckpointRequests([]message.Message{msg})[0].Content
	if got != msg.Content {
		t.Fatal("folded an independently sourced excerpt into the current request")
	}
	oldItem := buildUserCorrectionEvidence("message 1", older)
	newItem := buildUserCorrectionEvidence("message 2", current)
	if oldItem.Key == newItem.Key || evidenceItemID(oldItem) == evidenceItemID(newItem) {
		t.Fatal("different full requests share an evidence identity")
	}
}

func TestCheckpointRequestProjectionPreservesEvidenceBoundaries(t *testing.T) {
	request := "Fix the parser and preserve the existing output."
	msg := requestDedupFixture(request, "Earlier objective", "Keep existing outputs", "Earlier instruction")
	msg.Content, _, _ = strings.Cut(msg.Content, message.CompactionEvidenceTag)
	tool := buildEvidenceItem(evidenceToolError, "Latest failing tool result", "Preserve the observed failure", "message 8 (tool result)", request)
	correction := buildUserCorrectionEvidence("message 9 (user)", request)
	msg.Content += renderEvidenceArtifactContent([]evidenceItem{tool, correction})
	msg.CompactionRequestSources = buildCheckpointRequestSources(msg.Content, nil, "Earlier objective", []evidenceItem{tool, correction})
	// Even supplied provenance cannot make a tool observation an instruction.
	msg.CompactionRequestSources[checkpointEvidenceSourcePrefix+evidenceItemID(tool)] = checkpointRequestSource(request)
	var block strings.Builder
	block.WriteString("Excerpt:\n")
	writeEvidenceExcerptBlock(&block, request)
	literal := strings.TrimSuffix(block.String(), "\n")
	msg.Content = strings.Replace(msg.Content, "\n\n## Progress", "\n\n## Active Objective\nInspect this literal example:\n"+literal+"\n\n## Progress", 1)
	got := projectCheckpointRequests([]message.Message{msg})[0].Content
	objective, _ := markdownSection(got, "## Active Objective")
	if !strings.Contains(objective, literal) {
		t.Fatal("rewrote literal text outside the evidence region")
	}
	toolRow, _, _ := strings.Cut(got[strings.Index(got, "Evidence ID: "+evidenceItemID(tool)):], "\n2.")
	if !strings.Contains(toolRow, literal) {
		t.Fatal("rewrote independent tool evidence")
	}
	if strings.Count(got, checkpointSameRequestPointer) != 1 {
		t.Fatal("did not fold the instruction row after the tool row")
	}
}

func TestCheckpointRequestProjectionLongAndInheritedSources(t *testing.T) {
	request := strings.Repeat("Preserve the parser output. ", 60)
	msg := requestDedupFixture(request, request, request, request)
	projected := projectCheckpointRequests([]message.Message{msg})[0].Content
	if strings.Contains(projected, compactAnchorsRequestLabel) || strings.Contains(projected, compactAnchorsConstraintsLabel) ||
		strings.Contains(projected, checkpointUserConstraintsHeading) || strings.Contains(projected, retainedRecentMessagesHeading) {
		t.Fatalf("kept duplicate instruction blocks:\n%s", projected)
	}
	if strings.Count(projected, "Preserve the parser output.") != 60 || strings.Count(projected, checkpointSameRequestPointer) != 1 {
		t.Fatal("did not keep one complete request and one evidence reference")
	}
	// Persist and restore source provenance before projecting an inherited
	// request. It must keep the same view without mutating the durable body.
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var restored message.Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.CompactionRequestSources, msg.CompactionRequestSources) {
		t.Fatal("source provenance was not restored")
	}
	restored.Content = strings.Replace(restored.Content, "- "+latestUserRequestLabel+": ", "- "+inheritedCheckpointLabel+": "+latestUserRequestLabel+": ", 1)
	inherited := projectCheckpointRequests([]message.Message{restored})[0].Content
	if strings.Count(inherited, "Preserve the parser output.") != 60 || strings.Count(inherited, checkpointSameRequestPointer) != 1 {
		t.Fatal("inherited complete request was repeated")
	}
}

func TestCheckpointRequestSourcesCarryAndAmbiguity(t *testing.T) {
	prefix := strings.Repeat("Preserve parser defaults. ", 25)
	suffix := strings.Repeat(" Keep output fields.", 25)
	older := prefix + " Do not update the API. " + suffix
	current := prefix + " Update the API. " + suffix
	prior := requestDedupFixture(older, older, older, older)
	oldItem := buildUserCorrectionEvidence("message 1", older)
	newItem := buildUserCorrectionEvidence("message 2", current)
	body := requestDedupFixture(current, older, older, "Earlier instruction").Content
	sources := buildCheckpointRequestSources(body, []message.Message{prior}, older, []evidenceItem{newItem})
	anchors := parseCompactionAnchors(message.CompactionAnchorsSection(body))
	if sources[checkpointOriginalRequestSource] != checkpointRequestSource(older) ||
		sources[checkpointConstraintSourceKey(anchors.Constraints[0])] != checkpointRequestSource(older) {
		t.Fatal("a colliding new request changed carried anchor provenance")
	}
	ambiguous := buildCheckpointRequestSources(body, nil, older, []evidenceItem{oldItem, newItem})
	if ambiguous[checkpointConstraintSourceKey(anchors.Constraints[0])] != "" ||
		ambiguous[checkpointUserConstraintSourceKey(strings.ReplaceAll(oldItem.Excerpt, "\n", " "))] != "" {
		t.Fatal("ambiguous instruction bullet acquired a source")
	}
	prior.CompactionRequestSources = nil
	unknown := buildCheckpointRequestSources(body, []message.Message{prior}, older, []evidenceItem{newItem})
	if unknown[checkpointOriginalRequestSource] != "" || unknown[checkpointConstraintSourceKey(anchors.Constraints[0])] != "" {
		t.Fatal("inferred full provenance from carried truncated text")
	}
}

func TestCheckpointRequestProjectionShortRequestDoesNotGrow(t *testing.T) {
	for _, request := range []string{"Go", "Fix parser.", "Fix the parser and preserve the existing output."} {
		msg := requestDedupFixture(request, request, request, request)
		got := projectCheckpointRequests([]message.Message{msg})[0].Content
		if len(got) >= len(msg.Content) {
			t.Fatalf("projection grew for %q: %d >= %d", request, len(got), len(msg.Content))
		}
		if strings.Contains(got, checkpointUserConstraintsHeading) || strings.Contains(got, retainedRecentMessagesHeading) {
			t.Fatalf("kept a redundant instruction section:\n%s", got)
		}
	}
}

func TestCheckpointRequestProjectionRetainedPrefixAndRepeatedBlocks(t *testing.T) {
	request := "Preserve the parser output."
	msg := requestDedupFixture(request, "Earlier objective", "Keep other constraints", request)
	quote := retainedUserLabel + ":\n> " + request
	msg.Content = strings.Replace(msg.Content, quote, quote+" More requirements follow.\n\n"+quote+"\n\n"+quote, 1)
	got := projectCheckpointRequests([]message.Message{msg})[0].Content
	if !strings.Contains(got, "> "+request+" More requirements follow.") ||
		strings.Contains(got, quote+"\n\n") {
		t.Fatal("lost the longer instruction or kept a later exact duplicate")
	}
}

func TestCheckpointRequestSourcesRuntimeBuilders(t *testing.T) {
	request := "Do not change the existing output contract."
	history := []message.Message{{Role: message.RoleUser, Content: request}}
	items := collectEvidenceItems(history)
	if len(items) != 1 || items[0].RequestSource != checkpointRequestSource(request) {
		t.Fatal("history evidence lacks full request provenance")
	}
	a := newTestMainAgent(t, t.TempDir())
	a.recordEvidenceFromMessage(history[0])
	runtime := a.evidence.snapshot()
	if len(runtime) != 1 || runtime[0].RequestSource != items[0].RequestSource || runtime[0].Key != items[0].Key {
		t.Fatal("runtime and history evidence disagree about the source")
	}
	a.sessionDir = t.TempDir()
	bundle := modelDrivenBarrierSnapshot{
		snapshot: history, sessionDir: a.sessionDir, originalRequest: request,
		evidenceItems: items, retainRecentTokens: 1000,
	}
	builder := a.newModelDrivenCheckpointBuilder(t.Context(), bundle, history, len(history),
		&modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "Inspect parser", NextStep: "Run parser checks"}})
	content, _ := builder.render("")
	msg := message.Message{
		Role: message.RoleUser, Content: content, IsCompactionSummary: true,
		CompactionSummaryMode:    compactionSummaryModeModelDriven,
		CompactionRequestSources: builder.requestSources,
	}
	projected := projectCheckpointRequests([]message.Message{msg})[0].Content
	if strings.Count(projected, request) != 1 ||
		strings.Contains(projected, checkpointUserConstraintsHeading) ||
		strings.Contains(projected, retainedRecentMessagesHeading) {
		t.Fatalf("runtime builder produced repeated instruction blocks:\n%s", projected)
	}
}
