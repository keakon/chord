package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const (
	agentRequestSchemaVersion      = 1
	maxRetainedClosedAgentRequests = 256
)

type DurableAgentResponse struct {
	Message     string    `json:"message"`
	Kind        string    `json:"kind,omitempty"`
	ResponseID  string    `json:"response_id"`
	PersistedAt time.Time `json:"persisted_at"`
	DeliveredAt time.Time `json:"delivered_at"`
}

type DurableAgentRequest struct {
	Version          int                   `json:"version"`
	CorrelationID    string                `json:"correlation_id"`
	RequestMessageID string                `json:"request_message_id"`
	SourceTaskID     string                `json:"source_task_id"`
	SourceAttempt    uint64                `json:"source_attempt"`
	TargetTaskID     string                `json:"target_task_id,omitempty"`
	TargetAttempt    uint64                `json:"target_attempt,omitempty"`
	Message          string                `json:"message"`
	State            string                `json:"state"`
	CreatedAt        time.Time             `json:"created_at"`
	Response         *DurableAgentResponse `json:"response,omitempty"`
}

func cloneDurableAgentRequest(in *DurableAgentRequest) *DurableAgentRequest {
	if in == nil {
		return nil
	}
	out := *in
	if out.Response != nil {
		response := *out.Response
		out.Response = &response
	}
	return &out
}

func agentRequestsPath(sessionDir string) string {
	if strings.TrimSpace(sessionDir) == "" {
		return ""
	}
	return filepath.Join(sessionDir, "subagents", "agent-requests.json")
}

func loadAgentRequests(sessionDir string) (map[string]*DurableAgentRequest, error) {
	path := agentRequestsPath(sessionDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) || path == "" {
			return nil, nil
		}
		return nil, err
	}
	var records []*DurableAgentRequest
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	out := make(map[string]*DurableAgentRequest, len(records))
	for _, record := range records {
		record = cloneDurableAgentRequest(record)
		if record == nil || record.Version != agentRequestSchemaVersion || strings.TrimSpace(record.CorrelationID) == "" || strings.TrimSpace(record.SourceTaskID) == "" || record.SourceAttempt == 0 || strings.TrimSpace(record.RequestMessageID) == "" {
			return nil, fmt.Errorf("invalid durable agent request")
		}
		switch record.State {
		case "pending", "responded", "expired", "cancelled", "orphaned":
		default:
			return nil, fmt.Errorf("invalid agent request state %q", record.State)
		}
		if _, exists := out[record.CorrelationID]; exists {
			return nil, fmt.Errorf("duplicate agent request correlation_id %q", record.CorrelationID)
		}
		out[record.CorrelationID] = record
	}
	return out, nil
}

func persistAgentRequests(sessionDir string, records map[string]*DurableAgentRequest) error {
	path := agentRequestsPath(sessionDir)
	if path == "" {
		return nil
	}
	ordered := make([]*DurableAgentRequest, 0, len(records))
	for _, record := range records {
		ordered = append(ordered, cloneDurableAgentRequest(record))
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].CorrelationID < ordered[j].CorrelationID })
	return persistJSONAtomically(sessionDir, path, "agent-requests", ordered)
}

func retainAgentRequests(records map[string]*DurableAgentRequest) map[string]*DurableAgentRequest {
	retained := make(map[string]*DurableAgentRequest, len(records))
	closed := make([]*DurableAgentRequest, 0, len(records))
	for id, record := range records {
		record = cloneDurableAgentRequest(record)
		if record == nil {
			continue
		}
		if record.State == "pending" {
			retained[id] = record
			continue
		}
		closed = append(closed, record)
	}
	sort.Slice(closed, func(i, j int) bool {
		if !closed[i].CreatedAt.Equal(closed[j].CreatedAt) {
			return closed[i].CreatedAt.After(closed[j].CreatedAt)
		}
		return closed[i].CorrelationID > closed[j].CorrelationID
	})
	if len(closed) > maxRetainedClosedAgentRequests {
		closed = closed[:maxRetainedClosedAgentRequests]
	}
	for _, record := range closed {
		retained[record.CorrelationID] = record
	}
	return retained
}

func (a *MainAgent) persistAndPublishAgentRequests(records map[string]*DurableAgentRequest) error {
	records = retainAgentRequests(records)
	if err := persistAgentRequests(a.sessionDir, records); err != nil {
		return err
	}
	a.subs.mu.Lock()
	a.subs.agentRequests = records
	a.subs.mu.Unlock()
	return nil
}

func nextAgentRequestSeq(records map[string]*DurableAgentRequest) uint64 {
	var maximum uint64
	for correlationID := range records {
		if !strings.HasPrefix(correlationID, "corr-") {
			continue
		}
		if value, err := strconv.ParseUint(strings.TrimPrefix(correlationID, "corr-"), 10, 64); err == nil && value > maximum {
			maximum = value
		}
	}
	return maximum
}

func (a *MainAgent) resetAgentRequests(records map[string]*DurableAgentRequest) {
	a.agentRequestPersistMu.Lock()
	defer a.agentRequestPersistMu.Unlock()
	a.subs.mu.Lock()
	a.subs.agentRequests = make(map[string]*DurableAgentRequest, len(records))
	for id, record := range records {
		a.subs.agentRequests[id] = cloneDurableAgentRequest(record)
	}
	a.subs.mu.Unlock()
	a.agentRequestSeq.Store(nextAgentRequestSeq(records))
}

// guardDegradedAgentRequestSeq guards correlation IDs: the corrupt
// agent-requests file may have held corr-N IDs the restored transcript and
// mailbox still reference, and aliasing them would route replies to the wrong
// asker.
func (a *MainAgent) guardDegradedAgentRequestSeq(degraded bool) {
	if a == nil {
		return
	}
	raiseDegradedSeqFloor(&a.agentRequestSeq, degraded)
}

func (a *MainAgent) createAgentRequest(sub *SubAgent, payload tools.AgentRequestPayload) (*DurableAgentRequest, error) {
	if sub == nil {
		return nil, fmt.Errorf("missing request source")
	}
	a.agentRequestPersistMu.Lock()
	defer a.agentRequestPersistMu.Unlock()
	a.subs.mu.RLock()
	rec := cloneDurableTaskRecord(a.subs.taskRecords[sub.taskID])
	records := make(map[string]*DurableAgentRequest, len(a.subs.agentRequests)+1)
	for id, request := range a.subs.agentRequests {
		records[id] = cloneDurableAgentRequest(request)
	}
	a.subs.mu.RUnlock()
	if rec == nil || rec.Attempt == 0 {
		return nil, fmt.Errorf("missing durable request source task")
	}
	var targetAttempt uint64
	if ownerTaskID := strings.TrimSpace(rec.OwnerTaskID); ownerTaskID != "" {
		owner := a.taskRecordByTaskID(ownerTaskID)
		if owner == nil || owner.Attempt == 0 || !isNonTerminalTaskState(owner.State) {
			return nil, fmt.Errorf("request owner task %s is not active", ownerTaskID)
		}
		targetAttempt = owner.Attempt
	}
	correlationID := fmt.Sprintf("corr-%d", a.agentRequestSeq.Add(1))
	requestMessageID := a.nextSubAgentMailboxMessageID(sub.instanceID)
	now := time.Now()
	request := &DurableAgentRequest{
		Version: agentRequestSchemaVersion, CorrelationID: correlationID, RequestMessageID: requestMessageID,
		SourceTaskID: rec.TaskID, SourceAttempt: rec.Attempt, TargetTaskID: rec.OwnerTaskID, TargetAttempt: targetAttempt,
		Message: strings.TrimSpace(payload.Reason),
		State:   "pending", CreatedAt: now,
	}
	records[correlationID] = request
	if err := a.persistAndPublishAgentRequests(records); err != nil {
		return nil, err
	}
	return cloneDurableAgentRequest(request), nil
}

func (a *MainAgent) peerRouteRecords(ctx context.Context, targetTaskID string) (delegationCaller, *DurableTaskRecord, *DurableTaskRecord, error) {
	caller, err := a.delegationCallerFromContext(ctx)
	if err != nil {
		return delegationCaller{}, nil, nil, err
	}
	if caller.IsMain || caller.TaskID == "" {
		return delegationCaller{}, nil, nil, fmt.Errorf("peer routing is only available to delegated tasks")
	}
	targetTaskID = strings.TrimSpace(targetTaskID)
	if targetTaskID == "" || targetTaskID == caller.TaskID {
		return delegationCaller{}, nil, nil, fmt.Errorf("target_task_id must identify another task")
	}
	source := a.taskRecordByTaskID(caller.TaskID)
	target := a.taskRecordByTaskID(targetTaskID)
	if source == nil || target == nil {
		return delegationCaller{}, nil, nil, fmt.Errorf("unknown peer task %q", targetTaskID)
	}
	if source.Attempt == 0 || target.Attempt == 0 || !isNonTerminalTaskState(source.State) || !isNonTerminalTaskState(target.State) {
		return delegationCaller{}, nil, nil, fmt.Errorf("peer routing requires current non-terminal tasks")
	}
	if strings.TrimSpace(source.OwnerAgentID) != strings.TrimSpace(target.OwnerAgentID) || strings.TrimSpace(source.OwnerTaskID) != strings.TrimSpace(target.OwnerTaskID) {
		return delegationCaller{}, nil, nil, fmt.Errorf("task %s is not a sibling with the same direct owner", targetTaskID)
	}
	return caller, source, target, nil
}

func (a *MainAgent) NotifyPeerMessage(ctx context.Context, peer tools.AgentPeerNoticeRequest) (tools.TaskHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	peer.TargetTaskID = strings.TrimSpace(peer.TargetTaskID)
	peer.Message = strings.TrimSpace(peer.Message)
	peer.Kind = strings.TrimSpace(peer.Kind)
	if a.shuttingDown.Load() || a.admissionPaused.Load() {
		return tools.TaskHandle{}, fmt.Errorf("cannot notify peer during shutdown or session transition")
	}
	admissionEpoch := a.admissionEpoch.Load()
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	if a.shuttingDown.Load() || a.admissionPaused.Load() || a.admissionEpoch.Load() != admissionEpoch {
		return tools.TaskHandle{}, fmt.Errorf("peer notification invalidated by session or lifecycle change")
	}
	if err := ctx.Err(); err != nil {
		return tools.TaskHandle{}, err
	}
	caller, source, target, err := a.peerRouteRecords(ctx, peer.TargetTaskID)
	if err != nil {
		return tools.TaskHandle{}, err
	}
	targetSub := a.subAgentByTaskID(target.TaskID)
	if targetSub == nil {
		return tools.TaskHandle{}, fmt.Errorf("peer target task %s has no live worker", target.TaskID)
	}
	metadata := &message.MailboxMetadata{
		MessageID: a.nextSubAgentMailboxMessageID(targetSub.instanceID),
		TaskID:    target.TaskID, SourceTaskID: source.TaskID, SourceAttempt: source.Attempt,
		TargetTaskID: target.TaskID, TargetAttempt: target.Attempt, MessageType: string(AgentMessageTypeNotice),
	}
	status, statusMessage, err := a.deliverPeerMessageToSubAgent(targetSub, peer.Message, peer.Kind, metadata)
	if err != nil {
		return tools.TaskHandle{}, err
	}
	a.emitPeerNotifyAudit(caller, source, target, peer)
	return tools.TaskHandle{Status: status, TaskID: target.TaskID, AgentID: targetSub.instanceID, Message: statusMessage}, nil
}

func (a *MainAgent) emitPeerNotifyAudit(caller delegationCaller, source, target *DurableTaskRecord, peer tools.AgentPeerNoticeRequest) {
	a.emitToTUI(AgentNotifyEvent{
		AgentID: caller.AgentID, TaskID: source.TaskID, ParentAgentID: controlPlaneAgentID(source.OwnerAgentID), ParentTaskID: source.OwnerTaskID,
		TargetAgentID: target.LatestInstanceID, TargetTaskID: target.TaskID, Kind: peer.Kind, Message: peer.Message,
	})
}

// snapshotAgentRequests deep-clones the durable agent-request map under the
// subs read lock. Callers holding agentRequestPersistMu may mutate entries of
// the returned map freely before persisting it.
func (a *MainAgent) snapshotAgentRequests() map[string]*DurableAgentRequest {
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	records := make(map[string]*DurableAgentRequest, len(a.subs.agentRequests))
	for id, item := range a.subs.agentRequests {
		records[id] = cloneDurableAgentRequest(item)
	}
	return records
}

func (a *MainAgent) NotifySubAgentMessage(ctx context.Context, response tools.AgentResponseRequest) (tools.TaskHandle, error) {
	caller, err := a.delegationCallerFromContext(ctx)
	if err != nil {
		return tools.TaskHandle{}, err
	}
	correlationID := strings.TrimSpace(response.CorrelationID)
	response.TargetTaskID = strings.TrimSpace(response.TargetTaskID)
	response.Message = strings.TrimSpace(response.Message)
	response.Kind = strings.TrimSpace(response.Kind)
	if correlationID == "" || response.TargetTaskID == "" || response.Message == "" {
		return tools.TaskHandle{}, fmt.Errorf("correlation_id, target_task_id, and message are required")
	}
	if a.admissionPaused.Load() {
		return tools.TaskHandle{}, fmt.Errorf("agent response delivery is unavailable during session change")
	}
	admissionEpoch := a.admissionEpoch.Load()
	a.agentRequestPersistMu.Lock()
	if a.admissionPaused.Load() || a.admissionEpoch.Load() != admissionEpoch {
		a.agentRequestPersistMu.Unlock()
		return tools.TaskHandle{}, fmt.Errorf("agent response delivery invalidated by session change")
	}
	records := a.snapshotAgentRequests()
	request := records[correlationID]
	if request == nil || request.SourceTaskID != response.TargetTaskID {
		a.agentRequestPersistMu.Unlock()
		return tools.TaskHandle{}, fmt.Errorf("unknown pending request %q for task %s", correlationID, response.TargetTaskID)
	}
	if request.TargetTaskID != caller.TaskID || (request.TargetTaskID == "" && !caller.IsMain) {
		a.agentRequestPersistMu.Unlock()
		return tools.TaskHandle{}, fmt.Errorf("request %s is not owned by caller", correlationID)
	}
	if request.TargetTaskID != "" {
		owner := a.taskRecordByTaskID(request.TargetTaskID)
		if owner == nil || owner.Attempt != request.TargetAttempt || !isNonTerminalTaskState(owner.State) {
			if err := a.persistAgentRequestStateLocked(records, request, "orphaned"); err != nil {
				a.agentRequestPersistMu.Unlock()
				return tools.TaskHandle{}, err
			}
			a.agentRequestPersistMu.Unlock()
			return tools.TaskHandle{}, fmt.Errorf("request %s owner attempt is no longer current", correlationID)
		}
	}
	if request.State != "pending" {
		if request.State == "responded" && agentResponseMatches(request.Response, response) && a.agentResponseDeliveryRecorded(request) {
			a.agentRequestPersistMu.Unlock()
			return tools.TaskHandle{Status: "delivered", TaskID: request.SourceTaskID, Message: "response already recorded"}, nil
		}
		if request.State != "responded" || !agentResponseMatches(request.Response, response) {
			a.agentRequestPersistMu.Unlock()
			return tools.TaskHandle{}, fmt.Errorf("request %s is %s", correlationID, request.State)
		}
	}
	if request.Response != nil && !agentResponseMatches(request.Response, response) {
		a.agentRequestPersistMu.Unlock()
		return tools.TaskHandle{}, fmt.Errorf("request %s already has a different durable response", correlationID)
	}
	current := a.taskRecordByTaskID(request.SourceTaskID)
	if current == nil || current.Attempt != request.SourceAttempt {
		if err := a.persistAgentRequestStateLocked(records, request, "orphaned"); err != nil {
			a.agentRequestPersistMu.Unlock()
			return tools.TaskHandle{}, err
		}
		a.agentRequestPersistMu.Unlock()
		return tools.TaskHandle{}, fmt.Errorf("request %s source attempt is no longer current", correlationID)
	}
	if !isNonTerminalTaskState(current.State) {
		if err := a.persistAgentRequestStateLocked(records, request, "cancelled"); err != nil {
			a.agentRequestPersistMu.Unlock()
			return tools.TaskHandle{}, err
		}
		a.agentRequestPersistMu.Unlock()
		return tools.TaskHandle{}, fmt.Errorf("request %s source task is terminal", correlationID)
	}
	if request.Response == nil {
		request.Response = &DurableAgentResponse{
			Message: response.Message, Kind: response.Kind, ResponseID: "response-" + correlationID, PersistedAt: time.Now(),
		}
		records[correlationID] = request
		if err := a.persistAndPublishAgentRequests(records); err != nil {
			a.agentRequestPersistMu.Unlock()
			return tools.TaskHandle{}, err
		}
	}
	a.agentRequestPersistMu.Unlock()

	record, err := a.canCallerControlTask(caller.AgentID, caller.TaskID, request.SourceTaskID)
	if err != nil {
		return tools.TaskHandle{}, err
	}
	sub := a.subAgentByTaskID(request.SourceTaskID)
	rehydrated := false
	previousAgentID := ""
	if sub == nil {
		if !record.allowsRehydrate(taskResumeByTargetedNotify) {
			return tools.TaskHandle{}, fmt.Errorf("request source task %s cannot be rehydrated", request.SourceTaskID)
		}
		sub, previousAgentID, rehydrated, err = a.getOrRehydrateTask(record)
		if err != nil {
			return tools.TaskHandle{}, err
		}
	}
	metadata := &message.MailboxMetadata{
		MessageID: request.Response.ResponseID, TaskID: request.SourceTaskID, Kind: string(SubAgentMailboxKindDecisionRequired),
		LifecycleKind: string(SubAgentMailboxKindDecisionRequired), MessageType: string(AgentMessageTypeResponse), Subtype: "decision",
		SourceTaskID: caller.TaskID, SourceAttempt: request.TargetAttempt, TargetTaskID: request.SourceTaskID, TargetAttempt: request.SourceAttempt,
		CorrelationID: correlationID, InReplyTo: request.RequestMessageID,
	}
	status, statusMessage, err := a.deliverMessageToSubAgentWithMetadata(sub, response.Message, response.Kind, false, metadata)
	if err != nil {
		if rehydrated {
			a.closeSubAgent(sub.instanceID)
		}
		return tools.TaskHandle{}, err
	}
	handle := tools.TaskHandle{Status: status, TaskID: sub.taskID, AgentID: sub.instanceID, Message: statusMessage}
	if rehydrated {
		handle.Status = "rehydrated"
		handle.PreviousAgentID = previousAgentID
		handle.Rehydrated = true
	}
	a.agentRequestPersistMu.Lock()
	if a.admissionPaused.Load() || a.admissionEpoch.Load() != admissionEpoch {
		a.agentRequestPersistMu.Unlock()
		return tools.TaskHandle{}, fmt.Errorf("agent response delivery invalidated by session change")
	}
	a.subs.mu.RLock()
	currentRequest := cloneDurableAgentRequest(a.subs.agentRequests[correlationID])
	a.subs.mu.RUnlock()
	if currentRequest == nil || currentRequest.Response == nil {
		a.agentRequestPersistMu.Unlock()
		return tools.TaskHandle{}, fmt.Errorf("agent response delivery invalidated by request reset")
	}
	currentRequest.State = "responded"
	currentRequest.Response.DeliveredAt = time.Now()
	records = a.snapshotAgentRequests()
	records[correlationID] = currentRequest
	err = a.persistAndPublishAgentRequests(records)
	a.agentRequestPersistMu.Unlock()
	if err != nil {
		return tools.TaskHandle{}, err
	}
	return handle, nil
}

func agentResponseMatches(existing *DurableAgentResponse, response tools.AgentResponseRequest) bool {
	if existing == nil {
		return false
	}
	return existing.Message == strings.TrimSpace(response.Message) && existing.Kind == strings.TrimSpace(response.Kind)
}

func (a *MainAgent) agentResponseDeliveryRecorded(request *DurableAgentRequest) bool {
	if a == nil || request == nil || request.Response == nil {
		return false
	}
	responseID := strings.TrimSpace(request.Response.ResponseID)
	if responseID == "" {
		return false
	}
	if sub := a.subAgentByTaskID(request.SourceTaskID); sub != nil && sub.hasAcceptedMailbox(responseID) {
		return true
	}
	record := a.taskRecordByTaskID(request.SourceTaskID)
	if record == nil || a.recovery == nil {
		return false
	}
	messages, err := loadTaskHistoryMessages(a.recovery, record, loadToolActivityStarted(a.recovery))
	if err != nil {
		log.Warnf("failed to inspect durable agent response delivery response_id=%v task_id=%v error=%v", responseID, request.SourceTaskID, err)
		return false
	}
	for _, msg := range messages {
		if msg.Mailbox != nil && msg.Mailbox.MessageType == string(AgentMessageTypeResponse) && strings.TrimSpace(msg.Mailbox.MessageID) == responseID {
			return true
		}
	}
	return false
}

// persistAgentRequestStateLocked commits a request lifecycle transition before
// publishing it in memory. The caller must hold agentRequestPersistMu.
func (a *MainAgent) persistAgentRequestStateLocked(records map[string]*DurableAgentRequest, request *DurableAgentRequest, state string) error {
	request.State = state
	records[request.CorrelationID] = request
	return a.persistAndPublishAgentRequests(records)
}
