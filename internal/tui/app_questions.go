package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

type questionOperationMsg struct {
	id         string
	generation uint64
	operation  string
	receipt    agent.QuestionReceipt
	err        error
}

func (m *Model) questionOperation(op agent.QuestionOperation) tea.Cmd {
	backend := m.agent
	generation := m.questionGeneration
	if backend == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		receipt, err := backend.ApplyQuestionOperation(ctx, op)
		return questionOperationMsg{id: op.QuestionID, generation: generation, operation: op.Operation, receipt: receipt, err: err}
	}
}
func questionOperationID(id, op string) string {
	return fmt.Sprintf("tui:%s:%s:%d", id, op, time.Now().UnixNano())
}
func (m *Model) handleQuestionState(q agent.QuestionSnapshot) tea.Cmd {
	if m.questionRecords == nil {
		m.questionRecords = map[string]agent.QuestionSnapshot{}
		m.questionShown = map[string]bool{}
	}
	if m.questionShown == nil {
		m.questionShown = map[string]bool{}
	}
	if old, ok := m.questionRecords[q.ID]; ok && old.Version >= q.Version {
		return nil
	}
	m.questionRecords[q.ID] = q
	if (!q.Visible && q.Outcome == "") || q.Outcome != "" {
		m.removeQueuedQuestion(q.ID)
		return m.handleQuestionResolved(q.ID)
	}
	if m.question.requestID == q.ID && m.question.request != nil {
		oldDeadline := m.question.deadline
		req := questionRequestFromSnapshot(q)
		m.question.request = &req
		m.question.deadline = q.Deadline
		m.question.interacted = q.Timer == agent.QuestionTimerCancelled
		m.question.renderCacheText = ""
		if oldDeadline.IsZero() && !q.Deadline.IsZero() {
			return questionTimeoutTick(m.questionGeneration)
		}
		return m.confirmQuestionPresentation()
	}
	if !m.questionShown[q.ID] {
		m.questionShown[q.ID] = true
		return m.enqueueDialog(pendingDialog{questionID: q.ID, arrivedAt: time.Now()})
	}
	return nil
}

func questionRequestFromSnapshot(q agent.QuestionSnapshot) QuestionRequest {
	return QuestionRequest{Item: q.Item, Deadline: q.Deadline, AgentID: q.AgentID}
}

// Automatic presentation is not user interaction and must not cancel a timer.
func (m *Model) presentQuestionByID(id string, prev Mode) tea.Cmd {
	q, ok := m.questionRecords[id]
	if !ok {
		return nil
	}
	cmd := m.presentQuestionRequest(questionDialog{request: questionRequestFromSnapshot(q), requestID: id}, prev)
	if q.Outcome != "" {
		return cmd
	}
	return tea.Batch(cmd, m.confirmQuestionPresentation())
}

func (m *Model) beginQuestionInteraction() tea.Cmd {
	if m.question.request == nil || m.question.interacted || m.question.interacting {
		return nil
	}
	q, ok := m.questionRecords[m.question.requestID]
	if !ok || q.Outcome != "" || q.Timer == agent.QuestionTimerDisabled || q.Timer == agent.QuestionTimerCancelled {
		return nil
	}
	m.question.interacting = true
	m.question.renderCacheText = ""
	return m.questionOperation(agent.QuestionOperation{Operation: agent.QuestionOpInteract, OperationID: "tui:interact:" + q.ID, QuestionID: q.ID})
}

func (m *Model) removeQueuedQuestion(id string) {
	m.pendingDialogs = slices.DeleteFunc(m.pendingDialogs, func(d pendingDialog) bool { return d.questionID == id })
}

func (m *Model) handleQuestionOperation(msg questionOperationMsg) tea.Cmd {
	// Receipts can arrive after a terminal event or session switch.
	if msg.generation != m.questionGeneration || m.question.request == nil || m.question.requestID != msg.id {
		return nil
	}
	if msg.err != nil || !msg.receipt.Accepted {
		reason := msg.receipt.Error
		if msg.err != nil {
			reason = msg.err.Error()
		}
		if m.question.requestID == msg.id {
			m.question.submitting = false
			m.question.interacting = false
		}
		return m.enqueueToast("Question: "+reason, "warn")
	}
	if m.question.requestID == msg.id {
		switch msg.operation {
		case agent.QuestionOpInteract:
			m.question.interacted = true
			m.question.interacting = false
			m.question.deadline = time.Time{}
			m.question.renderCacheText = ""
		case agent.QuestionOpAnswer, agent.QuestionOpDecline, agent.QuestionOpWithdraw:
			return m.handleQuestionResolved(msg.id)
		}
	}
	return nil
}
func (m *Model) restoreQuestionProjection(msgs []message.Message) {
	records := map[string]agent.QuestionSnapshot{}
	for _, msg := range msgs {
		if len(msg.Question) == 0 {
			continue
		}
		var fact agent.QuestionFact
		if json.Unmarshal(msg.Question, &fact) != nil {
			continue
		}
		for _, q := range fact.Updates {
			if old, ok := records[q.ID]; !ok || q.Version > old.Version {
				records[q.ID] = q
			}
		}
	}
	for id, q := range m.questionRecords {
		if loaded, ok := records[id]; !ok || q.Version > loaded.Version {
			records[id] = q
		}
	}
	m.questionRecords = records
	if m.questionShown == nil {
		m.questionShown = map[string]bool{}
	}
	var visible []agent.QuestionSnapshot
	for _, q := range records {
		if q.Visible && q.Outcome == "" && !m.questionShown[q.ID] {
			visible = append(visible, q)
		}
	}
	slices.SortFunc(visible, func(a, b agent.QuestionSnapshot) int {
		if a.TurnID != b.TurnID {
			if a.TurnID < b.TurnID {
				return -1
			}
			return 1
		}
		if a.BatchID == b.BatchID {
			return a.Index - b.Index
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, q := range visible {
		m.questionShown[q.ID] = true
		if m.question.requestID != q.ID {
			m.pendingDialogs = append(m.pendingDialogs, pendingDialog{questionID: q.ID, arrivedAt: time.Now()})
		}
	}
}

func questionTranscriptBlock(msg message.Message, id int) *Block {
	if len(msg.Question) == 0 {
		return nil
	}
	var fact agent.QuestionFact
	if json.Unmarshal(msg.Question, &fact) != nil || fact.Result == nil {
		return nil
	}
	result := fact.Result
	if msg.Kind == message.KindQuestionState {
		return nil
	}
	body := result.Header + "\n" + strings.Join(result.Selected, "\n")
	if msg.Kind == message.KindQuestionResult {
		title := "Question · " + result.Outcome
		if result.Outcome == "defaulted" {
			title = "Question · Default adopted by system"
		}
		return &Block{ID: id, Type: BlockStatus, StatusTitle: title, Content: body}
	}
	return &Block{ID: id, Type: BlockUser, Content: body, Collapsed: true}
}

// A timer can start only when the answer UI and its default are visible.
func (m *Model) confirmQuestionPresentation() tea.Cmd {
	if m.mode != ModeQuestion || m.question.request == nil || m.question.interacted || m.question.interacting {
		return nil
	}
	q := m.questionRecords[m.question.requestID]
	if q.Timer != agent.QuestionTimerAwaiting && q.Timer != agent.QuestionTimerSuspended {
		return nil
	}
	rendered := ansi.Strip(m.renderQuestionDialog())
	if q.Item.DefaultOptionID != "" {
		for _, o := range q.Item.Options {
			if o.ID == q.Item.DefaultOptionID && !strings.Contains(rendered, "Default: "+sanitizeToolDisplayText(o.Label)) {
				return nil
			}
		}
	}
	if m.question.visibleBodyHeight <= 0 {
		return nil
	}
	return m.questionOperation(agent.QuestionOperation{Operation: agent.QuestionOpPresented, OperationID: fmt.Sprintf("tui:presented:%s:%d", q.ID, q.Version), QuestionID: q.ID, Version: q.Version, BindingID: q.BindingID, SupportsInteraction: true})
}
