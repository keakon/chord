package main

import "github.com/keakon/chord/internal/agent"

func emitHeadlessInputRejected(requestID, detail string, state *headlessState, out *stdoutWriter) {
	if requestID != "" {
		out.ordered(func() {
			env := &headlessEnvelope{Type: "input_result", Payload: agent.InputResultEvent{RequestID: requestID, Status: agent.InputRejected, Message: detail}}
			state.mu.Lock()
			state.stampHeadlessSeq(true, env)
			state.mu.Unlock()
			out.emit(env)
		})
	}
}
