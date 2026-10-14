package theme

import "testing"

func TestBuildOwnsIndependentStyleSnapshot(t *testing.T) {
	original := Default()
	first := Build(original)
	before := first.AssistantCardStyle.Render("sample")
	changed := original
	changed.AssistantCardBg = "17"
	changed.SelectedBg = "18"
	changed.SearchMatchFg = "19"
	second := Build(changed)
	if first == second || first.Palette != original || second.Palette != changed {
		t.Fatal("building a palette changed a previously published snapshot")
	}
	if first.AssistantCardStyle.Render("sample") != before {
		t.Fatal("building new styles mutated the original styles")
	}
	if second.QuestionSelectedStyle.Render("sample") != second.SelectedStyle.Render("sample") {
		t.Fatal("question alias does not use the same snapshot")
	}
	local := first.AssistantCardStyle.Padding(9)
	if local.GetPaddingTop() == first.AssistantCardStyle.GetPaddingTop() {
		t.Fatal("local presentation change mutated the shared style")
	}
}
