package tools

import "testing"

// TestBuiltInSchemasAvoidTopLevelCombinators pins the provider-compatibility
// rule that moved the edit, artifact, and completion constraints into their
// argument descriptions: strict Anthropic endpoints reject a tool input_schema
// carrying anyOf, oneOf, allOf, or not at the top level, and that rejection
// fails the whole request. Nested combinators remain allowed.
func TestBuiltInSchemasAvoidTopLevelCombinators(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{NameEdit, EditTool{}.Parameters()},
		{NameSaveArtifact, SaveArtifactTool{}.Parameters()},
		{NameReadArtifact, ReadArtifactTool{}.Parameters()},
		{NameComplete, CompleteTool{}.Parameters()},
		{NameDone, NewDoneTool().Parameters()},
		{NameQuestion, NewQuestionTool(nil).Parameters()},
	} {
		for _, key := range []string{"anyOf", "oneOf", "allOf", "not"} {
			if value, ok := tc.params[key]; ok {
				t.Errorf("%s schema has top-level %q = %#v; strict Anthropic endpoints reject it", tc.name, key, value)
			}
		}
	}
}
