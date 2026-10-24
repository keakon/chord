package tui

import (
	"context"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

func modelSelectKey(text string) tea.KeyPressMsg {
	if text == "" {
		return tea.KeyPressMsg(tea.Key{})
	}
	runes := []rune(text)
	return tea.KeyPressMsg(tea.Key{Text: text, Code: runes[0]})
}

func ctrlKeyPress(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: r, Mod: tea.ModCtrl})
}

func applyTestCmd(t *testing.T, m *Model, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	updated, next := m.Update(msg)
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model != m {
		*m = *model
	}
	return next
}

type targetedConversationAgent struct {
	sessionControlAgent
	messagesByTask map[string][]message.Message
	targetedCalls  []string
}

func (a *targetedConversationAgent) targetLabel(target agent.ConversationTarget) string {
	return target.AgentID + "/" + target.TaskID
}

func (a *targetedConversationAgent) GetMessagesForTarget(target agent.ConversationTarget) []message.Message {
	a.targetedCalls = append(a.targetedCalls, "read:"+a.targetLabel(target))
	return append([]message.Message(nil), a.messagesByTask[target.TaskID]...)
}

func (a *targetedConversationAgent) SendUserMessageToTarget(target agent.ConversationTarget, content string) {
	a.targetedCalls = append(a.targetedCalls, "send:"+a.targetLabel(target)+":"+content)
}

func (a *targetedConversationAgent) ContinueFromContextForTarget(target agent.ConversationTarget) {
	a.targetedCalls = append(a.targetedCalls, "continue:"+a.targetLabel(target))
}

type focusedMessagesAgent struct {
	sessionControlAgent
	focused         string
	messagesByFocus map[string][]message.Message
}

func (f *focusedMessagesAgent) GetMessages() []message.Message {
	msgs := f.messagesByFocus[f.focused]
	return append([]message.Message(nil), msgs...)
}

func (f *focusedMessagesAgent) SwitchFocus(agentID string) { f.focused = agentID }

func (f *focusedMessagesAgent) FocusedAgentID() string { return f.focused }
func (f *focusedMessagesAgent) FocusedAgentName() string {
	if f.focused == "" {
		return ""
	}
	for _, sub := range f.subAgents {
		if sub.InstanceID == f.focused {
			return sub.AgentDefName
		}
	}
	return f.focused
}
func (f *focusedMessagesAgent) StartupResumeStatus() (bool, string) {
	return false, ""
}

type sessionControlAgent struct {
	submittedInputs         []agent.SubmitInput
	events                  chan agent.AgentEvent
	messages                []message.Message
	messagesByFocus         map[string][]message.Message
	subAgents               []agent.SubAgentInfo
	poolNamesByFocus        map[string][]string
	mainModelPoolNames      []string
	currentPoolByFocus      map[string]string
	mainModelPool           string
	availableAgents         []string
	availableRoles          []string
	currentRole             string
	switchRoleErr           error
	contentRoot             string
	workDir                 string
	workDirID               string
	workDirGeneration       uint64
	focused                 string
	providerModelRef        string
	providerModelRefByFocus map[string]string
	runningModelRef         string
	runningModelRefByFocus  map[string]string
	runningVariant          string
	runningVariantByFocus   map[string]string
	tokenUsage              message.TokenUsage
	sidebarUsage            analytics.SessionStats
	usageStats              analytics.SessionStats
	walltimeStats           analytics.WalltimeStats
	contextCurrent          int
	contextLimit            int
	todos                   []tools.TodoItem
	sentMessages            []string
	confirmResolutions      []string
	contextMessages         []message.Message
	sentMultipart           [][]message.ContentPart
	queuedDraftIDs          []string
	queuedDrafts            [][]message.ContentPart
	removedDraftIDs         []string
	newSessionCalls         int
	resumeCalls             int
	resumeIDs               []string
	resumePending           bool
	startupResumeID         string
	forkMsgIndices          []int
	deleteSessionIDs        []string
	deleteSessionErr        error
	sessionSummary          *agent.SessionSummary
	cancelResult            bool
	cancelCalls             int
	continueCalls           int
	memoryEnabled           bool
	memoryDegraded          bool
	loopState               agent.LoopState
	loopTarget              string
	loopEnableCalls         int
	loopDisableCalls        int
	loopIteration           int
	loopMaxIterations       int
	loopMaxSet              bool
	canUseLoop              bool
	canUseLoopSet           bool
	serviceTierEnabled      bool
	serviceTier             config.ServiceTier
	effectiveServiceTier    config.ServiceTier
	supportedServiceTiers   []config.ServiceTier
	yoloEnabled             bool
	executePlanCalls        int
	executePlanPath         string
	executePlanAgent        string
	handoffResolutions      []handoffResolutionCall
}

type handoffResolutionCall struct {
	RequestID  string
	Action     string
	AgentName  string
	DenyReason string
}

func (s *sessionControlAgent) Events() <-chan agent.AgentEvent { return s.events }
func (s *sessionControlAgent) SendUserMessage(content string) {
	s.sentMessages = append(s.sentMessages, content)
}
func (s *sessionControlAgent) SendUserMessageWithParts(parts []message.ContentPart) {
	cp := append([]message.ContentPart(nil), parts...)
	s.sentMultipart = append(s.sentMultipart, cp)
}
func (s *sessionControlAgent) AppendContextMessage(msg message.Message) {
	s.contextMessages = append(s.contextMessages, msg)
}

func (s *sessionControlAgent) CancelCurrentTurn() bool {
	s.cancelCalls++
	return s.cancelResult
}
func (s *sessionControlAgent) QueueSubAgentDraft(draftID string, parts []message.ContentPart) bool {
	s.queuedDraftIDs = append(s.queuedDraftIDs, draftID)
	cp := append([]message.ContentPart(nil), parts...)
	s.queuedDrafts = append(s.queuedDrafts, cp)
	return true
}
func (s *sessionControlAgent) UpdateSubAgentDraft(draftID string, parts []message.ContentPart) bool {
	return s.QueueSubAgentDraft(draftID, parts)
}
func (s *sessionControlAgent) RemoveSubAgentDraft(draftID string) bool {
	s.removedDraftIDs = append(s.removedDraftIDs, draftID)
	return true
}
func (s *sessionControlAgent) ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string) {
	s.confirmResolutions = append(s.confirmResolutions, action+" "+requestID)
}
func (s *sessionControlAgent) ApplyQuestionOperation(context.Context, agent.QuestionOperation) (agent.QuestionReceipt, error) {
	return agent.QuestionReceipt{Accepted: true}, nil
}

func (s *sessionControlAgent) ProviderModelRef() string {
	if s.providerModelRefByFocus != nil {
		if ref, ok := s.providerModelRefByFocus[s.focused]; ok {
			return ref
		}
	}
	return s.providerModelRef
}
func (s *sessionControlAgent) RunningModelRef() string {
	if s.runningModelRefByFocus != nil {
		if ref, ok := s.runningModelRefByFocus[s.focused]; ok {
			return ref
		}
	}
	if s.runningModelRef != "" {
		return s.runningModelRef
	}
	return s.ProviderModelRef()
}
func (s *sessionControlAgent) RunningVariant() string {
	if s.runningVariantByFocus != nil {
		if variant, ok := s.runningVariantByFocus[s.focused]; ok {
			return variant
		}
	}
	return s.runningVariant
}
func (s *sessionControlAgent) CurrentPoolName() string {
	key := s.focused
	if key != "" {
		for _, sub := range s.subAgents {
			if sub.InstanceID == s.focused {
				key = sub.AgentDefName
				break
			}
		}
	}
	if s.currentPoolByFocus != nil {
		if pool, ok := s.currentPoolByFocus[key]; ok {
			return pool
		}
	}
	return ""
}
func (s *sessionControlAgent) PoolNames() []string {
	key := s.focused
	if key != "" {
		for _, sub := range s.subAgents {
			if sub.InstanceID == s.focused {
				key = sub.AgentDefName
				break
			}
		}
	}
	if s.poolNamesByFocus != nil {
		if names, ok := s.poolNamesByFocus[key]; ok {
			return append([]string(nil), names...)
		}
	}
	return nil
}
func (s *sessionControlAgent) MainModelPoolName() string {
	if s.mainModelPool != "" {
		return s.mainModelPool
	}
	if s.currentPoolByFocus != nil {
		if pool, ok := s.currentPoolByFocus[""]; ok {
			return pool
		}
	}
	return ""
}
func (s *sessionControlAgent) MainModelPoolNames() []string {
	if len(s.mainModelPoolNames) > 0 {
		return append([]string(nil), s.mainModelPoolNames...)
	}
	if s.poolNamesByFocus != nil {
		if names, ok := s.poolNamesByFocus[""]; ok {
			return append([]string(nil), names...)
		}
	}
	return nil
}
func (s *sessionControlAgent) AgentOverridePoolName(agentName string) (string, bool) {
	if s.currentPoolByFocus != nil {
		if pool, ok := s.currentPoolByFocus[agentName]; ok {
			return pool, true
		}
	}
	return "", false
}
func (s *sessionControlAgent) SetCurrentModelPool(string) error       { return nil }
func (s *sessionControlAgent) SetAgentModelPool(string, string) error { return nil }
func (s *sessionControlAgent) GetSubAgents() []agent.SubAgentInfo {
	return append([]agent.SubAgentInfo(nil), s.subAgents...)
}
func (s *sessionControlAgent) GetMessages() []message.Message {
	if s.messagesByFocus != nil {
		if msgs, ok := s.messagesByFocus[s.focused]; ok {
			return append([]message.Message(nil), msgs...)
		}
	}
	return append([]message.Message(nil), s.messages...)
}
func (s *sessionControlAgent) SwitchFocus(agentID string) { s.focused = agentID }
func (s *sessionControlAgent) FocusedAgentID() string     { return s.focused }
func (s *sessionControlAgent) FocusedAgentName() string {
	if s.focused == "" {
		return ""
	}
	if s.subAgents != nil {
		for _, sub := range s.subAgents {
			if sub.InstanceID == s.focused {
				return sub.AgentDefName
			}
		}
	}
	return s.focused
}
func (s *sessionControlAgent) StartupResumeStatus() (bool, string) {
	return s.resumePending, s.startupResumeID
}
func (s *sessionControlAgent) ContinueFromContext()                  { s.continueCalls++ }
func (s *sessionControlAgent) GetTokenUsage() message.TokenUsage     { return s.tokenUsage }
func (s *sessionControlAgent) GetUsageStats() analytics.SessionStats { return s.usageStats }
func (s *sessionControlAgent) GetSidebarUsageStats() analytics.SessionStats {
	return s.sidebarUsage
}
func (s *sessionControlAgent) GetSidebarWalltimeStats() analytics.WalltimeStats {
	return s.walltimeStats
}
func (s *sessionControlAgent) GetContextStats() (current, limit int) {
	return s.contextCurrent, s.contextLimit
}
func (s *sessionControlAgent) GetContextUsageState() ctxmgr.ContextUsageState {
	if s.contextCurrent > 0 {
		return ctxmgr.ContextUsageObserved
	}
	return ctxmgr.ContextUsageUnknown
}
func (s *sessionControlAgent) ContextPressureLinesForModelRef(string) (float64, float64) {
	return 0, 0
}
func (s *sessionControlAgent) GetContextMessageCount() int { return 0 }
func (s *sessionControlAgent) GetContextBytes() int        { return 0 }
func (s *sessionControlAgent) GetContextReductionStats() agent.ContextReductionStats {
	return agent.ContextReductionStats{}
}
func (s *sessionControlAgent) KeyStats() (available, total int)                          { return 0, 0 }
func (s *sessionControlAgent) CurrentRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot { return nil }
func (s *sessionControlAgent) ProxyInUseForRef(ref string) bool                          { return false }
func (s *sessionControlAgent) ContentRoot() string                                       { return s.contentRoot }
func (s *sessionControlAgent) WorkDir() string {
	if s.workDir != "" {
		return s.workDir
	}
	return s.contentRoot
}
func (s *sessionControlAgent) WorkDirSnapshot() agent.WorkDirSnapshot {
	return agent.WorkDirSnapshot{Path: s.WorkDir(), WorktreeID: s.workDirID, Generation: s.workDirGeneration}
}
func (s *sessionControlAgent) CurrentRole() string               { return s.currentRole }
func (s *sessionControlAgent) LoopKeepsMainBusy() bool           { return false }
func (s *sessionControlAgent) CurrentLoopState() agent.LoopState { return s.loopState }
func (s *sessionControlAgent) MemoryEnabled() bool               { return s.memoryEnabled }
func (s *sessionControlAgent) MemoryDegraded() bool              { return s.memoryDegraded }
func (s *sessionControlAgent) CurrentLoopTarget() string         { return s.loopTarget }
func (s *sessionControlAgent) CurrentLoopIteration() int {
	if s.loopIteration == 0 {
		return 1
	}
	return s.loopIteration
}
func (s *sessionControlAgent) CurrentLoopMaxIterations() int {
	if s.loopMaxSet {
		return 0
	}
	if s.loopMaxIterations == 0 {
		return 10
	}
	return s.loopMaxIterations
}
func (s *sessionControlAgent) EnableLoopMode(target string) {
	s.loopEnableCalls++
	s.loopTarget = target
}
func (s *sessionControlAgent) DisableLoopMode() { s.loopDisableCalls++ }
func (s *sessionControlAgent) ServiceTier() config.ServiceTier {
	if s == nil {
		return config.ServiceTierStandard
	}
	if s.serviceTier != "" {
		return s.serviceTier
	}
	if s.serviceTierEnabled {
		return config.ServiceTierFast
	}
	return config.ServiceTierStandard
}
func (s *sessionControlAgent) EffectiveServiceTier() config.ServiceTier {
	if s == nil {
		return config.ServiceTierStandard
	}
	if s.effectiveServiceTier != "" {
		return s.effectiveServiceTier
	}
	return s.ServiceTier()
}
func (s *sessionControlAgent) SupportedServiceTiers() []config.ServiceTier {
	if s == nil || len(s.supportedServiceTiers) == 0 {
		return []config.ServiceTier{config.ServiceTierStandard, config.ServiceTierFast, config.ServiceTierSlow}
	}
	return append([]config.ServiceTier(nil), s.supportedServiceTiers...)
}
func (s *sessionControlAgent) YoloEnabled() bool {
	return s != nil && s.yoloEnabled
}
func (s *sessionControlAgent) CanUseLoopMode() bool {
	if s.canUseLoopSet {
		return s.canUseLoop
	}
	return true
}
func (s *sessionControlAgent) ListSessionSummaries() ([]agent.SessionSummary, error) {
	return nil, nil
}
func (s *sessionControlAgent) GetSessionSummary() *agent.SessionSummary { return s.sessionSummary }
func (s *sessionControlAgent) DeleteSession(sessionID string) error {
	s.deleteSessionIDs = append(s.deleteSessionIDs, sessionID)
	return s.deleteSessionErr
}
func (s *sessionControlAgent) ExportSession(format, path string) {}
func (s *sessionControlAgent) ResumeSession()                    { s.resumeCalls++ }
func (s *sessionControlAgent) ResumeSessionID(sessionID string) {
	s.resumeIDs = append(s.resumeIDs, sessionID)
}
func (s *sessionControlAgent) NewSession() { s.newSessionCalls++ }
func (s *sessionControlAgent) ForkSession(msgIndex int) {
	s.forkMsgIndices = append(s.forkMsgIndices, msgIndex)
}
func (s *sessionControlAgent) ExecutePlan(planPath, agentName string) {
	s.executePlanCalls++
	s.executePlanPath = planPath
	s.executePlanAgent = agentName
}
func (s *sessionControlAgent) ResolveHandoff(requestID, action, agentName, denyReason string) {
	s.handoffResolutions = append(s.handoffResolutions, handoffResolutionCall{
		RequestID: requestID, Action: action, AgentName: agentName, DenyReason: denyReason,
	})
}
func (s *sessionControlAgent) AvailableAgents() []string {
	return append([]string(nil), s.availableAgents...)
}
func (s *sessionControlAgent) SwitchRole(role string) error {
	if s.switchRoleErr != nil {
		return s.switchRoleErr
	}
	s.currentRole = role
	return nil
}
func (s *sessionControlAgent) AvailableRoles() []string {
	return append([]string(nil), s.availableRoles...)
}
func (s *sessionControlAgent) InvokedSkills() []*skill.Meta { return nil }
func (s *sessionControlAgent) GetTodos() []tools.TodoItem {
	return append([]tools.TodoItem(nil), s.todos...)
}

func (s *sessionControlAgent) IsCompactionRunning() bool { return false }
func (s *sessionControlAgent) CancelCompaction() bool    { return false }
