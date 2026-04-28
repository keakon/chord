package agent

import (
	"context"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type emitToolProgressFn func(AgentEvent)

type toolProgressReporter struct {
	callID  string
	name    string
	agentID string
	emit    emitToolProgressFn
}

func (r toolProgressReporter) ReportToolProgress(progress tools.ToolProgressSnapshot) {
	if r.emit == nil || r.callID == "" || r.name == "" {
		return
	}
	r.emit(ToolProgressEvent{
		CallID:  r.callID,
		Name:    r.name,
		AgentID: r.agentID,
		Progress: ToolProgressSnapshot{
			Label:   progress.Label,
			Current: progress.Current,
			Total:   progress.Total,
			Text:    progress.Text,
		},
	})
}

func buildToolExecContext(
	ctx context.Context,
	tc message.ToolCall,
	agentID string,
	taskID string,
	sessionDir string,
	eventSender tools.EventSender,
	emit emitToolProgressFn,
) context.Context {
	agentCtx := tools.WithAgentID(ctx, agentID)
	agentCtx = tools.WithTaskID(agentCtx, taskID)
	agentCtx = tools.WithEventSender(agentCtx, eventSender)
	agentCtx = tools.WithSessionDir(agentCtx, sessionDir)
	agentCtx = tools.WithToolProgressReporter(agentCtx, toolProgressReporter{
		callID:  tc.ID,
		name:    tc.Name,
		agentID: agentID,
		emit:    emit,
	})
	return agentCtx
}
