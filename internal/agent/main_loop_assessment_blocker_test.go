package agent

import (
	"slices"
	"testing"
)

// The inference must only ever name a category the marker parser accepts. A
// category it invents reads as a plain detail when the model echoes it back in
// a `<blocked>category: reason</blocked>` marker, so the two lists have to stay
// one source.
func TestInferLoopBlockerCategoryOnlyNamesParseableCategories(t *testing.T) {
	reasons := []string{
		"",
		"missing credential for the upstream API",
		"permission denied writing the generated file",
		"the input sample was never captured",
		"workspace conflict: another checkout holds the lock",
		"needs a user decision on the layout",
		"something entirely unrelated happened",
	}
	for _, reason := range reasons {
		category := inferLoopBlockerCategory(reason)
		if !slices.Contains(loopBlockerCategories, category) {
			t.Fatalf("reason %q inferred category %q, which the marker parser does not accept", reason, category)
		}
		parsed, detail := parseLoopBlockedReason(category + ": the detail")
		if parsed != category || detail != "the detail" {
			t.Fatalf("category %q is not parseable when echoed back: parsed %q with detail %q", category, parsed, detail)
		}
	}
}

// An unrecognized category prefix is not a category: the whole marker text is
// the detail, and the category comes from the inference.
func TestParseLoopBlockedReasonIgnoresUnknownCategoryPrefix(t *testing.T) {
	category, detail := parseLoopBlockedReason("made_up_category: real detail")
	if category == "made_up_category" {
		t.Fatal("an unknown category prefix must not be accepted")
	}
	if !slices.Contains(loopBlockerCategories, category) {
		t.Fatalf("inferred category %q is not in the accepted list", category)
	}
	if detail != "made_up_category: real detail" {
		t.Fatalf("detail = %q, want the whole marker text", detail)
	}
}
