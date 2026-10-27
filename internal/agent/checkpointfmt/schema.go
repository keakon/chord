package checkpointfmt

import "slices"

// Shared checkpoint section names used by renderers and readers.
const (
	ProgressHeading        = "## Progress"
	KeyDecisionsHeading    = "## Key Decisions"
	OpenProblemsHeading    = "## Open Problems"
	UserConstraintsHeading = "## User Constraints"
	// CurrentUserRequestHeading is the summary section whose body the
	// runtime resolves authoritatively, and the heading a continuation inherits
	// its latest request from.
	CurrentUserRequestHeading = "## Current User Request"
)

// Todo State subgroup labels. The exact text is load-bearing summary contract:
// the summarize prompt demands these label lines verbatim, the fallback
// renderer emits them, and the validator and restore readers match them, so
// the wording must change everywhere at once.
const (
	TodoSubgroupActive    = "Active/relevant to latest request"
	TodoSubgroupCompleted = "Completed/background"
	TodoSubgroupStale     = "Stale/superseded"
	// The label lines the summary contract requires under "## Todo State":
	// bullet prefix, label, colon, with the subgroup's items on the lines after.
	TodoSubgroupActiveLine    = "- " + TodoSubgroupActive + ":"
	TodoSubgroupCompletedLine = "- " + TodoSubgroupCompleted + ":"
	TodoSubgroupStaleLine     = "- " + TodoSubgroupStale + ":"
)

const compactSummaryMinChars = 160

var compactionRequiredHeadings = []string{
	CurrentUserRequestHeading,
	"## Active Objective",
	"## Background Goals",
	UserConstraintsHeading,
	ProgressHeading,
	KeyDecisionsHeading,
	"## Files and Evidence",
	"## Todo State",
	"## SubAgent State",
	OpenProblemsHeading,
	"## Next Step",
}

// RequiredHeadings returns an independently owned copy in canonical summary order.
// Mutating the result does not change validation or other callers' schemas.
func RequiredHeadings() []string { return slices.Clone(compactionRequiredHeadings) }
