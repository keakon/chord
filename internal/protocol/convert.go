package protocol

import (
	"errors"
	"fmt"
	"time"

	"github.com/keakon/chord/internal/agent"
)

// FromAgentEvent converts an internal [agent.AgentEvent] into a protocol
// [Envelope] suitable for transmission to a connected client.
//
// The caller supplies the server-assigned sequence number (seq) which is
// embedded in the envelope for client-side gap detection after reconnection.
//
// Returns an error if the event type is unknown (future-proof: new event
// types added to the agent package without a corresponding conversion will
// surface as explicit errors rather than silent drops).
func FromAgentEvent(ev agent.AgentEvent, seq uint64) (*Envelope, error) {
	var (
		env *Envelope
		err error
	)

	switch e := ev.(type) {
	case agent.StreamTextEvent:
		env, err = NewEnvelope(TypeStreamText, StreamTextPayload{
			Text:    e.Text,
			AgentID: e.AgentID,
		})

	case agent.StreamThinkingEvent:
		env, err = NewEnvelope(TypeStreamThinking, StreamThinkingPayload{
			Text:    e.Text,
			AgentID: e.AgentID,
		})

	case agent.StreamThinkingDeltaEvent:
		env, err = NewEnvelope(TypeStreamThinkingDelta, StreamThinkingDeltaPayload{
			Text:    e.Text,
			AgentID: e.AgentID,
		})

	case agent.StreamRollbackEvent:
		env, err = NewEnvelope(TypeStreamRollback, StreamRollbackPayload{
			Reason:  e.Reason,
			AgentID: e.AgentID,
		})

	case agent.ThinkingStartedEvent:
		// TUI-only event: starts the thinking duration timer in embedded mode.
		// Not forwarded to C/S clients; clients start the timer on first delta.
		return nil, ErrTUIOnlyEvent

	case agent.PendingDraftConsumedEvent:
		// TUI-only event for local queued-draft bookkeeping.
		return nil, ErrTUIOnlyEvent

	case agent.ToolCallStartEvent:
		env, err = NewEnvelope(TypeToolCallStart, ToolCallStartPayload{
			CallID:   e.ID,
			Name:     e.Name,
			ArgsJSON: e.ArgsJSON,
			AgentID:  e.AgentID,
		})

	case agent.ToolCallUpdateEvent:
		env, err = NewEnvelope(TypeToolCallUpdate, ToolCallUpdatePayload{
			CallID:            e.ID,
			Name:              e.Name,
			ArgsJSON:          e.ArgsJSON,
			ArgsStreamingDone: e.ArgsStreamingDone,
			AgentID:           e.AgentID,
		})

	case agent.ToolCallExecutionEvent:
		env, err = NewEnvelope(TypeToolCallExecution, ToolCallExecutionPayload{
			CallID:   e.ID,
			Name:     e.Name,
			ArgsJSON: e.ArgsJSON,
			State:    string(e.State),
			AgentID:  e.AgentID,
		})

	case agent.ToolProgressEvent:
		// TUI-only event for local tool-card progress; remote parity can be added later.
		return nil, ErrTUIOnlyEvent

	case agent.ToolResultEvent:
		status := string(e.Status)
		env, err = NewEnvelope(TypeToolResult, ToolResultPayload{
			CallID:      e.CallID,
			Name:        e.Name,
			ArgsJSON:    e.ArgsJSON,
			Audit:       e.Audit.Clone(),
			Result:      e.Result,
			Status:      status,
			AgentID:     e.AgentID,
			Diff:        e.Diff,
			DiffAdded:   e.DiffAdded,
			DiffRemoved: e.DiffRemoved,
			FileCreated: e.FileCreated,
		})

	case agent.ErrorEvent:
		msg := ""
		if e.Err != nil {
			msg = e.Err.Error()
		}
		env, err = NewEnvelope(TypeError, ErrorPayload{
			Message: msg,
			AgentID: e.AgentID,
		})

	case agent.IdleEvent:
		env, err = NewEnvelope(TypeIdle, nil)

	case agent.HandoffEvent:
		env, err = NewEnvelope(TypePlanComplete, PlanCompletePayload{
			PlanPath: e.PlanPath,
		})

	case agent.InfoEvent:
		env, err = NewEnvelope(TypeInfo, InfoPayload{
			Message: e.Message,
			AgentID: e.AgentID,
		})

	case agent.ToastEvent:
		env, err = NewEnvelope(TypeToast, ToastPayload{
			Message: e.Message,
			Level:   e.Level,
			AgentID: e.AgentID,
		})

	case agent.SpawnFinishedEvent:
		env, err = NewEnvelope(TypeBackgroundObjectFinished, BackgroundObjectFinishedPayload{
			BackgroundID:  e.EffectiveID(),
			AgentID:       e.AgentID,
			Kind:          e.Kind,
			Status:        e.Status,
			Command:       e.Command,
			Description:   e.Description,
			MaxRuntimeSec: e.MaxRuntimeSec,
			Message:       e.Message,
		})

	case agent.AgentDoneEvent:
		env, err = NewEnvelope(TypeAgentDone, AgentDonePayload{
			AgentID: e.AgentID,
			TaskID:  e.TaskID,
			Summary: e.Summary,
		})

	case agent.AgentStatusEvent:
		env, err = NewEnvelope(TypeAgentStatus, AgentStatusPayload{
			AgentID: e.AgentID,
			Status:  e.Status,
			Message: e.Message,
		})

	case agent.AgentActivityEvent:
		env, err = NewEnvelope(TypeActivity, ActivityPayload{
			AgentID: e.AgentID,
			Type:    string(e.Type),
			Detail:  e.Detail,
		})

	case agent.RoleChangedEvent:
		env, err = NewEnvelope(TypeRoleChanged, RoleChangedPayload{
			Role: e.Role,
		})

	case agent.ModelSelectEvent:
		env, err = NewEnvelope(TypeModelSelectRequest, nil)

	case agent.RunningModelChangedEvent:
		env, err = NewEnvelope(TypeRunningModelChanged, RunningModelChangedPayload{
			AgentID:          e.AgentID,
			ProviderModelRef: e.ProviderModelRef,
			RunningModelRef:  e.RunningModelRef,
		})

	case agent.SessionSelectEvent:
		payload := SessionSelectRequestPayload{
			Sessions: make([]SessionSummaryPayload, 0, len(e.Sessions)),
		}
		for _, s := range e.Sessions {
			payload.Sessions = append(payload.Sessions, SessionSummaryPayload{
				ID:                                  s.ID,
				LastModTime:                         s.LastModTime,
				FirstUserMessage:                    s.FirstUserMessage,
				FirstUserMessageIsCompactionSummary: s.FirstUserMessageIsCompactionSummary,
				OriginalFirstUserMessage:            s.OriginalFirstUserMessage,
				OriginalFirstUserMessageIsCompactionSummary: s.OriginalFirstUserMessageIsCompactionSummary,
				ForkedFrom: s.ForkedFrom,
			})
		}
		env, err = NewEnvelope(TypeSessionSelectRequest, payload)

	case agent.SessionSwitchStartedEvent:
		env, err = NewEnvelope(TypeSessionSwitchStarted, SessionSwitchStartedPayload{
			Kind:      e.Kind,
			SessionID: e.SessionID,
		})

	case agent.SessionRestoredEvent:
		env, err = NewEnvelope(TypeSessionRestored, nil)

	case agent.ConfirmRequestEvent:
		env, err = NewEnvelope(TypeConfirmRequest, ConfirmRequestPayload{
			ToolName:       e.ToolName,
			ArgsJSON:       e.ArgsJSON,
			RequestID:      e.RequestID,
			TimeoutMS:      e.Timeout.Milliseconds(),
			NeedsApproval:  append([]string(nil), e.NeedsApproval...),
			AlreadyAllowed: append([]string(nil), e.AlreadyAllowed...),
		})

	case agent.QuestionRequestEvent:
		env, err = NewEnvelope(TypeQuestionRequest, QuestionRequestPayload{
			ToolName:      e.ToolName,
			Header:        e.Header,
			Question:      e.Question,
			Options:       e.Options,
			OptionDetails: e.OptionDetails,
			DefaultAnswer: e.DefaultAnswer,
			Multiple:      e.Multiple,
			RequestID:     e.RequestID,
			TimeoutMS:     e.Timeout.Milliseconds(),
		})

	case agent.TodosUpdatedEvent:
		env, err = NewEnvelope(TypeTodosUpdated, TodosUpdatedPayload{
			Todos: e.Todos,
		})

	default:
		return nil, fmt.Errorf("protocol: unhandled AgentEvent type %T", ev)
	}

	if err != nil {
		return nil, err
	}
	env.Seq = seq
	return env, nil
}

// ErrTUIOnlyEvent is returned by FromAgentEvent for events that are only
// meaningful in embedded (non-C/S) mode and should not be forwarded to clients.
var ErrTUIOnlyEvent = errors.New("protocol: TUI-only event, not forwarded to clients")

// ErrNotAgentEvent is returned by ToAgentEvent when the envelope type does not
// represent an agent event (e.g. snapshot).
var ErrNotAgentEvent = errors.New("protocol: envelope is not an agent event")

// ToAgentEvent converts a protocol [Envelope] from the server into an
// [agent.AgentEvent] for the TUI. Returns ErrNotAgentEvent for envelope types
// that are not agent events (e.g. TypeSnapshot).
// The client should use those envelope types for connection state only.
func ToAgentEvent(env *Envelope) (agent.AgentEvent, error) {
	if env == nil {
		return nil, fmt.Errorf("protocol: nil envelope")
	}
	switch env.Type {
	case TypeStreamText:
		p, err := ParsePayload[StreamTextPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.StreamTextEvent{Text: p.Text, AgentID: p.AgentID}, nil
	case TypeStreamThinking:
		p, err := ParsePayload[StreamThinkingPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.StreamThinkingEvent{Text: p.Text, AgentID: p.AgentID}, nil
	case TypeStreamThinkingDelta:
		p, err := ParsePayload[StreamThinkingDeltaPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.StreamThinkingDeltaEvent{Text: p.Text, AgentID: p.AgentID}, nil
	case TypeStreamRollback:
		p, err := ParsePayload[StreamRollbackPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.StreamRollbackEvent{Reason: p.Reason, AgentID: p.AgentID}, nil
	case TypeToolCallStart:
		p, err := ParsePayload[ToolCallStartPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.ToolCallStartEvent{ID: p.CallID, Name: p.Name, ArgsJSON: p.ArgsJSON, AgentID: p.AgentID}, nil
	case TypeToolCallUpdate:
		p, err := ParsePayload[ToolCallUpdatePayload](env)
		if err != nil {
			return nil, err
		}
		return agent.ToolCallUpdateEvent{ID: p.CallID, Name: p.Name, ArgsJSON: p.ArgsJSON, ArgsStreamingDone: p.ArgsStreamingDone, AgentID: p.AgentID}, nil
	case TypeToolCallExecution:
		p, err := ParsePayload[ToolCallExecutionPayload](env)
		if err != nil {
			return nil, err
		}
		state := agent.ToolCallExecutionStateQueued
		switch p.State {
		case string(agent.ToolCallExecutionStateRunning):
			state = agent.ToolCallExecutionStateRunning
		case string(agent.ToolCallExecutionStateQueued):
			state = agent.ToolCallExecutionStateQueued
		}
		return agent.ToolCallExecutionEvent{ID: p.CallID, Name: p.Name, ArgsJSON: p.ArgsJSON, State: state, AgentID: p.AgentID}, nil
	case TypeToolResult:
		p, err := ParsePayload[ToolResultPayload](env)
		if err != nil {
			return nil, err
		}
		status := agent.ToolResultStatusSuccess
		switch p.Status {
		case string(agent.ToolResultStatusCancelled):
			status = agent.ToolResultStatusCancelled
		case string(agent.ToolResultStatusError):
			status = agent.ToolResultStatusError
		case string(agent.ToolResultStatusSuccess):
			status = agent.ToolResultStatusSuccess
		}
		return agent.ToolResultEvent{
			CallID:      p.CallID,
			Name:        p.Name,
			ArgsJSON:    p.ArgsJSON,
			Audit:       p.Audit.Clone(),
			Result:      p.Result,
			Status:      status,
			AgentID:     p.AgentID,
			Diff:        p.Diff,
			DiffAdded:   p.DiffAdded,
			DiffRemoved: p.DiffRemoved,
			FileCreated: p.FileCreated,
		}, nil
	case TypeError:
		p, err := ParsePayload[ErrorPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.ErrorEvent{Err: fmt.Errorf("%s", p.Message), AgentID: p.AgentID}, nil
	case TypeIdle:
		return agent.IdleEvent{}, nil
	case TypePlanComplete:
		p, err := ParsePayload[PlanCompletePayload](env)
		if err != nil {
			return nil, err
		}
		return agent.HandoffEvent{PlanPath: p.PlanPath}, nil
	case TypeInfo:
		p, err := ParsePayload[InfoPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.InfoEvent{Message: p.Message, AgentID: p.AgentID}, nil
	case TypeToast:
		p, err := ParsePayload[ToastPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.ToastEvent{Message: p.Message, Level: p.Level, AgentID: p.AgentID}, nil
	case TypeBackgroundObjectFinished:
		p, err := ParsePayload[BackgroundObjectFinishedPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.SpawnFinishedEvent{BackgroundID: p.EffectiveID(), AgentID: p.AgentID, Kind: p.Kind, Status: p.Status, Command: p.Command, Description: p.Description, MaxRuntimeSec: p.MaxRuntimeSec, Message: p.Message}, nil
	case TypeAgentDone:
		p, err := ParsePayload[AgentDonePayload](env)
		if err != nil {
			return nil, err
		}
		return agent.AgentDoneEvent{AgentID: p.AgentID, TaskID: p.TaskID, Summary: p.Summary}, nil
	case TypeAgentStatus:
		p, err := ParsePayload[AgentStatusPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.AgentStatusEvent{AgentID: p.AgentID, Status: p.Status, Message: p.Message}, nil
	case TypeActivity:
		p, err := ParsePayload[ActivityPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.AgentActivityEvent{AgentID: p.AgentID, Type: agent.ActivityType(p.Type), Detail: p.Detail}, nil
	case TypeRoleChanged:
		p, err := ParsePayload[RoleChangedPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.RoleChangedEvent{Role: p.Role}, nil
	case TypeModelSelectRequest:
		return agent.ModelSelectEvent{}, nil
	case TypeRunningModelChanged:
		p, err := ParsePayload[RunningModelChangedPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.RunningModelChangedEvent{AgentID: p.AgentID, ProviderModelRef: p.ProviderModelRef, RunningModelRef: p.RunningModelRef}, nil
	case TypeSessionSelectRequest:
		p, err := ParsePayload[SessionSelectRequestPayload](env)
		if err != nil || p.Sessions == nil {
			return agent.SessionSelectEvent{Sessions: nil}, nil
		}
		list := make([]agent.SessionSummary, 0, len(p.Sessions))
		for _, s := range p.Sessions {
			list = append(list, agent.SessionSummary{
				ID:                                  s.ID,
				LastModTime:                         s.LastModTime,
				FirstUserMessage:                    s.FirstUserMessage,
				FirstUserMessageIsCompactionSummary: s.FirstUserMessageIsCompactionSummary,
				OriginalFirstUserMessage:            s.OriginalFirstUserMessage,
				OriginalFirstUserMessageIsCompactionSummary: s.OriginalFirstUserMessageIsCompactionSummary,
				ForkedFrom: s.ForkedFrom,
			})
		}
		return agent.SessionSelectEvent{Sessions: list}, nil
	case TypeSessionSwitchStarted:
		p, err := ParsePayload[SessionSwitchStartedPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.SessionSwitchStartedEvent{Kind: p.Kind, SessionID: p.SessionID}, nil
	case TypeSessionRestored:
		return agent.SessionRestoredEvent{}, nil
	case TypeConfirmRequest:
		p, err := ParsePayload[ConfirmRequestPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.ConfirmRequestEvent{
			ToolName:       p.ToolName,
			ArgsJSON:       p.ArgsJSON,
			RequestID:      p.RequestID,
			Timeout:        time.Duration(p.TimeoutMS) * time.Millisecond,
			NeedsApproval:  append([]string(nil), p.NeedsApproval...),
			AlreadyAllowed: append([]string(nil), p.AlreadyAllowed...),
		}, nil
	case TypeQuestionRequest:
		p, err := ParsePayload[QuestionRequestPayload](env)
		if err != nil {
			return nil, err
		}
		return agent.QuestionRequestEvent{
			ToolName:      p.ToolName,
			Header:        p.Header,
			Question:      p.Question,
			Options:       p.Options,
			OptionDetails: p.OptionDetails,
			DefaultAnswer: p.DefaultAnswer,
			Multiple:      p.Multiple,
			RequestID:     p.RequestID,
			Timeout:       time.Duration(p.TimeoutMS) * time.Millisecond,
		}, nil
	case TypeSnapshot, TypeTodosUpdated:
		// These types are handled directly by the client (readLoop special cases)
		// and must NOT go through the generic ToAgentEvent path.
		return nil, ErrNotAgentEvent
	default:
		return nil, fmt.Errorf("protocol: unknown envelope type %q", env.Type)
	}
}
