package checkpointfmt

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func validCompactionSummaryForTest(history string) string {
	return fmt.Sprintf(
		"## Current User Request\n- continue current task\n\n## Active Objective\n- continue current task\n\n## Background Goals\n- none\n\n## User Constraints\n- none\n\n## Progress\n- progress recorded\n\n## Key Decisions\n- decisions captured\n\n## Files and Evidence\n- Archived history: %s\n- src/current_task.go\n\n## Todo State\n- Active/relevant to latest request: (none)\n- Completed/background: (none)\n- Stale/superseded: (none)\n\n## SubAgent State\n- none active\n\n## Open Problems\n- none\n\n## Next Step\n- Inspect src/current_task.go and continue the current task.",
		history,
	)
}

func TestValidateCompactionSummaryRejectsWeakOneLiner(t *testing.T) {
	if err := ValidateSummary("Keep improving extraction quality."); err == nil {
		t.Fatal("expected weak one-line summary to be rejected")
	}
}

func TestValidateCompactionSummaryRejectsThinkingTags(t *testing.T) {
	summary := "<think>provider failed to split reasoning</think>\n\n" + validCompactionSummaryForTest("history-1.md")
	if err := ValidateSummary(summary); err == nil {
		t.Fatal("expected summary with thinking tags to be rejected")
	}
}

func TestValidateCompactionSummaryRequiresHeadingLinesInOrder(t *testing.T) {
	if err := ValidateSummary(validCompactionSummaryForTest("history-1.md")); err != nil {
		t.Fatalf("validCompactionSummaryForTest rejected: %v", err)
	}
	outOfOrder := strings.Replace(
		validCompactionSummaryForTest("history-1.md"),
		"## Active Objective\n- continue current task\n\n## Background Goals",
		"## Background Goals\n- none\n\n## Active Objective\n- continue current task",
		1,
	)
	if err := ValidateSummary(outOfOrder); err == nil {
		t.Fatal("expected out-of-order headings to be rejected")
	}
	quotedOnly := strings.Replace(validCompactionSummaryForTest("history-1.md"), "## Active Objective", "> ## Active Objective", 1)
	if err := ValidateSummary(quotedOnly); err == nil {
		t.Fatal("expected non-heading section marker to be rejected")
	}
}

func TestValidateCompactionSummaryRejectsVagueNextStep(t *testing.T) {
	summary := strings.Replace(
		validCompactionSummaryForTest("history-1.md"),
		"- Inspect src/current_task.go and continue the current task.",
		"- continue",
		1,
	)
	if err := ValidateSummary(summary); err == nil {
		t.Fatal("expected vague next step to be rejected")
	}
}

func TestValidateCompactionSummaryRejectsCompletedTodoAsActive(t *testing.T) {
	summary := strings.Replace(
		validCompactionSummaryForTest("history-1.md"),
		"- Active/relevant to latest request: (none)\n- Completed/background: (none)\n- Stale/superseded: (none)",
		"- Active/relevant to latest request: finish export docs\n- Completed/background: finish export docs\n- Stale/superseded: (none)",
		1,
	)
	if err := ValidateSummary(summary); err == nil {
		t.Fatal("expected completed todo duplicated as active to be rejected")
	}
}

func TestValidateCompactionSummaryRejectsStaleTodoAsActive(t *testing.T) {
	summary := strings.Replace(
		validCompactionSummaryForTest("history-1.md"),
		"- Active/relevant to latest request: (none)\n- Completed/background: (none)\n- Stale/superseded: (none)",
		"- Active/relevant to latest request: debug old search path\n- Completed/background: (none)\n- Stale/superseded: debug old search path",
		1,
	)
	if err := ValidateSummary(summary); err == nil {
		t.Fatal("expected stale todo duplicated as active to be rejected")
	}
}

func TestValidateCompactionSummaryRejectsOutOfOrderSections(t *testing.T) {
	summary := strings.Replace(validCompactionSummaryForTest("history-1.md"),
		"## Open Problems\n- none\n\n## Next Step\n- Inspect src/current_task.go and continue the current task.",
		"## Next Step\n- Inspect src/current_task.go and continue the current task.\n\n## Open Problems\n- none", 1)
	err := ValidateSummary(summary)
	if err == nil {
		t.Fatal("expected a summary with swapped sections to be rejected")
	}
	if err.Error() != "compaction summary sections out of order" {
		t.Fatalf("validateCompactionSummary() = %v, want out-of-order rejection", err)
	}
	if missing, ok := errors.AsType[*MissingSectionsError](err); ok {
		t.Fatalf("out-of-order rejection reported missing headings: %v", missing.Missing)
	}
}

func TestValidateCompactionSummaryRejectsLegacySections(t *testing.T) {
	summary := `## Goal
- Continue work with enough detail to pass the minimum summary length requirement for validation.

## User Constraints
- Keep changes focused and do not accept older compaction section names.

## Progress
- Legacy summaries are intentionally rejected after upgrading section names.

## Key Decisions
- Require the active-objective heading set instead of the previous goal heading set.

## Files and Evidence
- history-1.md
- internal/agent/compaction.go

## Todo State
- Existing todo state is preserved from the legacy summary.

## SubAgent State
- none

## Open Problems
- none

## Next Step
- Continue from the legacy checkpoint safely.`
	if err := ValidateSummary(summary); err == nil {
		t.Fatal("expected legacy summary to be rejected")
	}
}

func TestRequiredHeadingsCannotChangeValidation(t *testing.T) {
	headings := RequiredHeadings()
	headings[0] = "## Replaced Section"
	if got := RequiredHeadings()[0]; got != CurrentUserRequestHeading {
		t.Fatalf("caller changed canonical first heading to %q", got)
	}
	if err := ValidateSummary(validCompactionSummaryForTest("history-1.md")); err != nil {
		t.Fatalf("schema returned to a caller changed validation: %v", err)
	}
}

func TestTodoSubgroupIDsStopsAtNestedHeading(t *testing.T) {
	section := `
- Stale/superseded:
  - [in_progress] old: old task
  - [pending] also old: another task
### Runtime TODO snapshot
  - [in_progress] t1: runtime task
`
	got := TodoSubgroupIDs(section)
	want := []string{"old", "also old"}
	if !slices.Equal(got, want) {
		t.Fatalf("TodoSubgroupIDs = %#v, want %#v", got, want)
	}
}

func TestTodoSubgroupIDsIgnoresUnclassifiedEntries(t *testing.T) {
	for _, section := range []string{
		"- Stale/superseded:\n  - (none)\n",
		"- Stale/superseded:\n  - an old task with no stable ID\n",
	} {
		if got := TodoSubgroupIDs(section); len(got) != 0 {
			t.Fatalf("TodoSubgroupIDs(%q) = %#v, want nil", section, got)
		}
	}
}
