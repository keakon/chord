package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func TestQuestionSnapshotPagesPreservePendingAndRevisions(t *testing.T) {
	a := newQuestionTestAgent(t)
	for i := range 75 {
		id := fmt.Sprintf("closed-%03d", i)
		q := QuestionSnapshot{ID: id, Item: questionItems("Choice")[0], Visible: true, Outcome: tools.QuestionOutcomeAnswered}
		a.applyQuestionFact(QuestionFact{TransactionID: id, Updates: []QuestionSnapshot{q}}, QuestionReceipt{Accepted: true})
	}
	pending := createTestQuestions(t, a, questionItems("Current choice"), false).Result.QuestionIDs[0]
	revised := tools.QuestionAnswer{QuestionID: "closed-001", ResultID: "revision", Selected: []string{"Updated choice"}}
	a.applyQuestionFact(QuestionFact{Operation: QuestionOpRevise, TransactionID: "revision", Result: &revised}, QuestionReceipt{Accepted: true})
	seen := map[string]bool{}
	query := QuestionSnapshotQuery{}
	for {
		page := a.questionSnapshotPage(query)
		if !page.Accepted || page.Error != "" {
			t.Fatalf("page: %+v", page)
		}
		if len(page.Questions) == 0 || page.Questions[0].ID != pending || !page.Questions[0].pending() {
			t.Fatal("page discarded pending state")
		}
		if len(page.Questions) > maxQuestionSnapshotRecords+1 {
			t.Fatal("unbounded page")
		}
		for _, q := range page.Questions {
			if q.ID == pending {
				continue
			}
			if seen[q.ID] {
				t.Fatalf("duplicate historical record %s", q.ID)
			}
			seen[q.ID] = true
			if q.ID == revised.QuestionID && (q.Revision == nil || q.Revision.ResultID != revised.ResultID) {
				t.Fatal("page lost latest revision")
			}
		}
		if page.NextAfterID == "" {
			break
		}
		query = QuestionSnapshotQuery{BindingID: page.BindingID, AfterID: page.NextAfterID}
	}
	if len(seen) != 75 || len(a.questions.records) != 76 || len(a.questions.transactions) != 77 {
		t.Fatal("paging lost durable facts")
	}
	requested := a.questionSnapshotPage(QuestionSnapshotQuery{RequestIDs: []string{"closed-001", "closed-001"}})
	found := 0
	for _, q := range requested.Questions {
		if q.ID == "closed-001" {
			found++
		}
	}
	if found != 1 {
		t.Fatal("requested historical decision was lost or duplicated")
	}
	if page := a.questionSnapshotPage(QuestionSnapshotQuery{BindingID: "different", AfterID: "closed-001"}); page.Error == "" {
		t.Fatal("accepted cursor from a different runtime")
	}
	if page := a.questionSnapshotPage(QuestionSnapshotQuery{AfterID: "closed-001"}); page.Error == "" {
		t.Fatal("accepted an unbound cursor")
	}
}

func TestQuestionSnapshotUsesEncodedByteBudget(t *testing.T) {
	a := newQuestionTestAgent(t)
	item := questionItems("Choice")[0]
	item.Question = strings.Repeat("\x01", tools.MaxQuestionTextBytes)
	for i := range tools.MaxQuestionOptions {
		item.Options = append(item.Options, tools.QuestionOption{ID: fmt.Sprintf("option-%d", i), Label: "Option", Description: strings.Repeat("\x01", tools.MaxQuestionTextBytes)})
	}
	item.Options = item.Options[2:]
	for i := range 40 {
		id := fmt.Sprintf("closed-%03d", i)
		a.applyQuestionFact(QuestionFact{TransactionID: id, Updates: []QuestionSnapshot{{ID: id, Item: item, Visible: true, Outcome: tools.QuestionOutcomeAnswered}}}, QuestionReceipt{Accepted: true})
	}
	page := a.questionSnapshotPage(QuestionSnapshotQuery{})
	data, err := json.Marshal(page.Questions)
	if err != nil || len(data) > maxQuestionSnapshotBytes+maxQuestionSnapshotRecords+2 || page.NextAfterID == "" {
		t.Fatalf("unbounded encoded page: %d %v", len(data), err)
	}
	if len(page.Questions) >= maxQuestionSnapshotRecords {
		t.Fatal("count cap hid encoded-byte overflow")
	}
}

func TestPendingQuestionTextBudgetReopensAfterResolution(t *testing.T) {
	a := newQuestionTestAgent(t)
	item := questionItems("Choice")[0]
	item.Question = strings.Repeat("\x01", tools.MaxQuestionTextBytes)
	item.Options[0].Description = strings.Repeat("\x01", tools.MaxQuestionTextBytes)
	var ids []string
	for range tools.MaxPendingQuestions {
		receipt := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpCreate, OperationID: makeRequestID()}, args: tools.QuestionArgs{Questions: []tools.QuestionItem{item}, Wait: new(false)}})
		if !receipt.Accepted {
			if len(ids) == 0 || len(ids) == tools.MaxPendingQuestions || !strings.Contains(receipt.Error, "text budget") {
				t.Fatalf("unexpected rejection: %+v", receipt)
			}
			break
		}
		ids = append(ids, receipt.Result.QuestionIDs[0])
	}
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: ids[0], Answers: []string{"yes"}}, time.Time{})
	if receipt := createTestQuestions(t, a, []tools.QuestionItem{item}, false); !receipt.Accepted {
		t.Fatal("resolved question did not release the active text budget")
	}
}
