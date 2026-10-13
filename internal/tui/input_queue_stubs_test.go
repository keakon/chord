package tui

import "github.com/keakon/chord/internal/agent"

func (*sessionControlAgent) InputDeliveryTarget() agent.InputTarget {
	return agent.InputTarget{RuntimeID: "test-runtime", SessionID: "test-session", OwnerTaskID: "main"}
}
func (*sessionControlAgent) InputQueueSnapshot() *agent.InputQueueSnapshot { return nil }
func (s *sessionControlAgent) SubmitUserInput(input agent.SubmitInput) bool {
	s.submittedInputs = append(s.submittedInputs, input)
	return s.QueueSubAgentDraft(input.DraftID, input.Parts)
}
func (*sessionControlAgent) UpdateQueuedInput(agent.UpdateInput) bool   { return true }
func (*sessionControlAgent) DeleteQueuedInput(agent.DeleteInput) bool   { return true }
func (*sessionControlAgent) ControlInputQueue(agent.QueueControl) bool  { return true }
func (loopBusyAgentStub) InputDeliveryTarget() agent.InputTarget        { return agent.InputTarget{} }
func (loopBusyAgentStub) InputQueueSnapshot() *agent.InputQueueSnapshot { return nil }
func (loopBusyAgentStub) SubmitUserInput(agent.SubmitInput) bool        { return false }
func (loopBusyAgentStub) UpdateQueuedInput(agent.UpdateInput) bool      { return false }
func (loopBusyAgentStub) DeleteQueuedInput(agent.DeleteInput) bool      { return false }
func (loopBusyAgentStub) ControlInputQueue(agent.QueueControl) bool     { return false }
