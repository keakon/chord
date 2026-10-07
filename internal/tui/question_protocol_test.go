package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func questionEventForTest(id, header, text string, labels, details []string, multiple bool, deadline time.Time, owner string) agent.QuestionStateEvent {
	item := tools.QuestionItem{Header: header, Question: text, Multiple: multiple}
	for i, label := range labels {
		opt := tools.QuestionOption{ID: label, Label: label}
		if i < len(details) {
			opt.Description = details[i]
		}
		item.Options = append(item.Options, opt)
	}
	return agent.QuestionStateEvent{Question: agent.QuestionSnapshot{ID: id, Item: item, Deadline: deadline, AgentID: owner, Visible: true, Version: 1}}
}

func (m *Model) receiveTestQuestion(d questionDialog) tea.Cmd {
	if d.requestID == "" {
		d.requestID = "test-question"
	}
	return m.handleQuestionState(agent.QuestionSnapshot{ID: d.requestID, AgentID: d.request.AgentID, Item: d.request.Item, Deadline: d.request.Deadline, Visible: true, Version: 1})
}
