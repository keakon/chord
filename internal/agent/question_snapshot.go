package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxPendingQuestionBytes    = 1 << 20 // JSON-encoded items, shared by all pending questions
	maxQuestionSnapshotBytes   = 2 << 20
	maxQuestionSnapshotRecords = 32
	maxQuestionRequestedBytes  = 6 << 20
)

// QuestionSnapshotQuery pages closed questions in reverse creation order.
// Every page also includes all visible pending questions, independent of the cursor.
// BindingID binds continuation pages to the runtime that issued the first page.
type QuestionSnapshotQuery struct {
	BindingID  string
	AfterID    string
	RequestIDs []string
}

func (a *MainAgent) questionSnapshotPage(query QuestionSnapshotQuery) QuestionReceipt {
	r := &a.questions
	if query.BindingID != "" && query.BindingID != r.binding {
		return QuestionReceipt{Error: "question snapshot binding changed; restart pagination"}
	}
	if len(query.RequestIDs) > maxQuestionSnapshotRecords {
		return QuestionReceipt{Error: "question snapshot supports at most 32 requested IDs"}
	}
	start := len(r.recordOrder) - 1
	if query.AfterID != "" {
		if query.BindingID == "" {
			return QuestionReceipt{Error: "question snapshot continuation requires binding_id"}
		}
		index := slices.Index(r.recordOrder, query.AfterID)
		if index < 0 {
			return QuestionReceipt{Error: "question snapshot cursor not found"}
		}
		start = index - 1
	}
	var pending []QuestionSnapshot
	page := QuestionReceipt{Accepted: true, SessionID: filepath.Base(a.SessionDir()), BindingID: r.binding}
	for _, q := range r.records {
		if q.pending() && q.Visible {
			pending = append(pending, projectQuestionSnapshot(q, r))
		}
	}
	slices.SortFunc(pending, func(x, y QuestionSnapshot) int { return strings.Compare(x.ID, y.ID) })
	page.Questions = slices.Clone(pending)
	included := make(map[string]bool, len(pending)+len(query.RequestIDs))
	for _, q := range pending {
		included[q.ID] = true
	}
	requestedBytes := 0
	for _, id := range query.RequestIDs {
		q, ok := r.records[id]
		if !ok || included[id] || q.pending() {
			continue
		}
		projected := projectQuestionSnapshot(q, r)
		data, err := json.Marshal(projected)
		if err != nil || requestedBytes+len(data) > maxQuestionRequestedBytes {
			return QuestionReceipt{Error: "requested question snapshots exceed the byte budget; request fewer IDs"}
		}
		requestedBytes += len(data)
		included[id] = true
		page.Questions = append(page.Questions, projected)
	}

	bytes, count := 0, 0
	last := ""
	for i := start; i >= 0; i-- {
		q := r.records[r.recordOrder[i]]
		if !q.pending() && !included[q.ID] {
			projected := projectQuestionSnapshot(q, r)
			data, err := json.Marshal(projected)
			if err != nil {
				return QuestionReceipt{Error: "encode question snapshot: " + err.Error()}
			}
			if len(data) > maxQuestionSnapshotBytes {
				return QuestionReceipt{Error: "question snapshot exceeds the page byte budget"}
			}
			if count == maxQuestionSnapshotRecords || bytes+len(data) > maxQuestionSnapshotBytes {
				page.NextAfterID = last
				return page
			}
			page.Questions = append(page.Questions, projected)
			bytes += len(data)
			count++
		}
		last = q.ID
	}
	return page
}
