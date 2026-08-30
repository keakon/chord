package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestLooksLikeStatedConstraint(t *testing.T) {
	positive := []string{
		"保持现有 API 行为不变，继续修复 bug",
		"对外输出格式保持稳定，只调整内部实现",
		"保持向后兼容",
		"keep the existing API behavior unchanged",
		"the output format must stay unchanged",
		"maintain backward compatibility with v1 callers",
	}
	for _, text := range positive {
		if !looksLikeStatedConstraint(text) {
			t.Errorf("stated constraint not recognized: %q", text)
		}
	}
	negative := []string{
		"继续修复编译错误",
		"跑一下测试看看结果",
		"这个文件读一下",
		"summarize the current progress",
		"please run the tests",
	}
	for _, text := range negative {
		if looksLikeStatedConstraint(text) {
			t.Errorf("plain request miscast as stated constraint: %q", text)
		}
	}
	// An imperative correction wins: it is already captured by the stronger
	// user-correction kind and must not double-fire.
	if looksLikeStatedConstraint("不要改这个文件") {
		t.Error("imperative correction should not also classify as a stated constraint")
	}
}

func TestCollectEvidenceItemsCapturesStatedConstraints(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleUser, Content: "修一下编译错误"},
		{Role: message.RoleTool, Content: "done", ToolStatus: string(ToolResultStatusSuccess)},
		{Role: message.RoleUser, Content: "保持现有 API 行为不变，继续"},
	}
	items := collectEvidenceItems(messages)
	var found *evidenceItem
	for i := range items {
		if items[i].Kind == evidenceStatedConstraint {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("stated constraint not captured as evidence: %+v", items)
	}
	if !strings.Contains(found.Source, "message 3 (user)") {
		t.Fatalf("stated constraint missing source ref, got %q", found.Source)
	}
	if !strings.Contains(found.Excerpt, "保持现有 API 行为不变") {
		t.Fatalf("stated constraint excerpt lost the constraint text: %q", found.Excerpt)
	}
	if evidencePriority(found.Kind) <= evidencePriority(evidenceUserRequest) {
		t.Fatalf("stated constraint priority %d must outrank plain requests (%d)", evidencePriority(found.Kind), evidencePriority(evidenceUserRequest))
	}
}

// A Done rejection must outrank the latest plain user request as the latest
// request anchor; the plain request still earns a candidate, but the rejection
// is never pushed out by it.
func TestDoneRejectionOutranksLatestPlainRequest(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleUser, Content: "继续修"},
		{Role: message.RoleTool, Content: "Done rejected: 保持现有行为不变，重新做"},
	}
	items := collectEvidenceItems(messages)
	var rejection, request *evidenceItem
	for i := range items {
		switch items[i].Kind {
		case evidenceDoneRejected:
			rejection = &items[i]
		case evidenceUserRequest:
			request = &items[i]
		}
	}
	if rejection == nil {
		t.Fatalf("Done rejection not captured: %+v", items)
	}
	if request == nil {
		t.Fatalf("plain user request not captured alongside the rejection: %+v", items)
	}
	if evidencePriority(rejection.Kind) != evidencePriority(evidenceUserCorrection) {
		t.Fatalf("Done rejection must be treated as equal-weight user feedback (priority %d), got %d", evidencePriority(evidenceUserCorrection), evidencePriority(rejection.Kind))
	}
	// Latest-request anchoring: through the real selection path (dedup, sort,
	// budget), the rejection must lead and the plain request must still be
	// present behind it.
	selected := evidenceItemsFromCandidates(items, 100000)
	if len(selected) == 0 || selected[0].Kind != evidenceDoneRejected {
		t.Fatalf("Done rejection should lead the selected evidence, got %+v", selected)
	}
	seenRequest := false
	for _, item := range selected {
		if item.Kind == evidenceUserRequest {
			seenRequest = true
		}
	}
	if !seenRequest {
		t.Fatalf("plain user request dropped from selection: %+v", selected)
	}
}
