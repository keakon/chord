package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func requestViewFixture() message.Message {
	request := "Fix the parser.\n\n## Requirements\nKeep empty input valid."
	body := "## Current User Request\n" + modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: request}) +
		"\n\n## Progress\n- parser checked\n\n## Key Decisions\n- preserve empty input\n\n## Open Problems\n- add test\n\n" + typedStateSectionHeading + "\n" + renderTypedStateJSON(checkpointTypedState{Completed: []string{"parser checked"}, Decisions: []string{"preserve empty input"}, OpenIssues: []string{"add test"}}) + "\n" + typedStateOmittedNote +
		"\n\n" + retainedRecentMessagesHeading + "\nUser:\n> " + strings.ReplaceAll(request, "\n", "\n> ") + "\n\nUser:\n> Also keep whitespace.\n\n[Context Evidence]\nPreserve evidence."
	return message.Message{Role: message.RoleUser, Content: body, IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven}
}

func TestCheckpointRequestProjectionPreservesDurableCarryAndCorrections(t *testing.T) {
	original := requestViewFixture()
	msgs := []message.Message{original, {Role: message.RoleAssistant, Content: "working", ReasoningContent: "keep reasoning"}}
	before, _ := json.Marshal(msgs)
	projected := projectCheckpointRequests(msgs)
	after, _ := json.Marshal(msgs)
	if string(before) != string(after) {
		t.Fatal("projection mutated durable messages")
	}
	body := projected[0].Content
	for _, want := range []string{"## Current User Request", "Keep empty input valid.", "Also keep whitespace.", typedStateOmittedNote, "Preserve evidence."} {
		if !strings.Contains(body, want) {
			t.Fatalf("lost %q", want)
		}
	}
	if strings.Contains(body, typedStateSectionHeading) || strings.Count(body, "Keep empty input valid.") != 1 {
		t.Fatal("duplicate request or typed payload retained")
	}
	if !reflect.DeepEqual(projected[1], msgs[1]) {
		t.Fatal("assistant reasoning changed")
	}
	if !reflect.DeepEqual(projectCheckpointRequests(projected), projected) {
		t.Fatal("projection not idempotent")
	}
	// Simulate resume from durable JSON and a later compaction merge.
	var restored []message.Message
	if err := json.Unmarshal(before, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projectCheckpointRequests(restored), projected) {
		t.Fatal("resume changes request view")
	}
	req, _, _, _, broken := mergePriorTypedCheckpointState(&modelDrivenCheckpointRequest{}, restored[0].Content)
	if broken || len(req.Args.Decisions) != 1 || req.Args.Decisions[0] != "preserve empty input" {
		t.Fatal("durable carry lost")
	}
}

func TestCheckpointRequestProjectionConservativeCases(t *testing.T) {
	for _, kind := range []string{"ordinary", "malformed", "generic", "truncated", "different"} {
		t.Run(kind, func(t *testing.T) {
			msg := requestViewFixture()
			switch kind {
			case "ordinary":
				msg.IsCompactionSummary = false
			case "malformed":
				msg.Content = strings.Replace(msg.Content, `{"completed":`, `broken {"completed":`, 1)
			case "generic":
				msg.CompactionSummaryMode = message.CompactionSummaryModeTruncateOnly
			case "truncated":
				msg.Content = strings.Replace(msg.Content, "\n\nUser:\n> Also", "\n> [truncated]\n\nUser:\n> Also", 1)
			case "different":
				msg.Content = strings.Replace(msg.Content, "> Keep empty input valid.", "> Keep empty input invalid.", 1)
			}
			got := projectCheckpointRequests([]message.Message{msg})[0].Content
			if kind == "ordinary" && got != msg.Content {
				t.Fatal("ordinary user content changed")
			}
			if (kind == "malformed" || kind == "generic") && !strings.Contains(got, typedStateSectionHeading) {
				t.Fatal("unsafe machine state removed")
			}
			if (kind == "truncated" || kind == "different") && strings.Contains(got, "User: Same request") {
				t.Fatal("nonidentical request removed")
			}
		})
	}
}

func TestCheckpointProjectionBeforeReductionAndFallbackPreparation(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		a := newTestMainAgent(t, t.TempDir())
		if disabled {
			cfg := &config.Config{}
			if err := yaml.Unmarshal([]byte("context:\n  reduction: false\n"), cfg); err != nil {
				t.Fatal(err)
			}
			a.projectConfig = cfg
		}
		msgs := []message.Message{requestViewFixture()}
		first := a.prepareMessagesForLLM(msgs)
		again := a.prepareMessagesForLLM(msgs)
		fallback := a.prepareMessagesForLLMWithOptions(msgs, false)
		if strings.Contains(first[0].Content, typedStateSectionHeading) || first[0].Content != again[0].Content || first[0].Content != fallback[0].Content {
			t.Fatal("unstable request projection")
		}
		if !strings.Contains(msgs[0].Content, typedStateSectionHeading) {
			t.Fatal("durable checkpoint changed")
		}
	}
}

func TestCheckpointValidationReportsIndependentRepairsTogether(t *testing.T) {
	args := tools.CompactContextArgs{CheckpointKind: checkpointKindCommitted, ClaimKinds: map[string]string{"second": claimKindObserved, "first": claimKindObserved}}
	err := validateModelDrivenCheckpointKind(args)
	if err == nil {
		t.Fatal("invalid checkpoint accepted")
	}
	text := err.Error()
	for _, want := range []string{"stage_status=completed", "at least one evidence_refs", `claim_kinds "first"`, `claim_kinds "second"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Index(text, `"first"`) > strings.Index(text, `"second"`) {
		t.Fatal("errors not sorted")
	}
	for range 20 {
		if validateModelDrivenCheckpointKind(args).Error() != text {
			t.Fatal("unstable error order")
		}
	}
	explained := (&MainAgent{}).explainCheckpointRejection(err).Error()
	if strings.Count(explained, "no evidence ID is resolvable") != 1 {
		t.Fatal("evidence hint not attached exactly once")
	}
	args = tools.CompactContextArgs{ClaimKinds: map[string]string{}, ClaimEvidence: map[string][]string{}}
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		args.ClaimKinds[key] = claimKindObserved
	}
	if err := validateModelDrivenCheckpointKind(args); err == nil || !strings.Contains(err.Error(), "2 additional checkpoint validation errors omitted") {
		t.Fatalf("unbounded or accepted errors: %v", err)
	}
}
