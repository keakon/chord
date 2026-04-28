package agent

import "github.com/keakon/chord/internal/message"

func (a *MainAgent) SetLSPStatusFunc(serverList func() []LSPServerDisplay) {
	a.lspServerListFn = serverList
}

func (a *MainAgent) SetLSPSessionFuncs(reset func(), load func([]message.Message)) {
	a.lspSessionResetFn = reset
	a.lspSessionLoadFn = load
}

func (a *MainAgent) SetMCPStatusFunc(serverList func() []MCPServerDisplay) {
	a.mcpServerListFn = serverList
}

func (a *MainAgent) NotifyEnvStatusUpdated() { a.emitToTUI(EnvStatusUpdateEvent{}) }

func (a *MainAgent) LSPServerList() []LSPServerDisplay {
	if a.lspServerListFn == nil {
		return nil
	}
	return a.lspServerListFn()
}

func (a *MainAgent) MCPServerList() []MCPServerDisplay {
	if a.mcpServerListFn == nil {
		return nil
	}
	return a.mcpServerListFn()
}
