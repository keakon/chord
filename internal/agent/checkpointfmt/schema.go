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
