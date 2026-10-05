package main

import "github.com/keakon/chord/internal/agent"

func emitHeadlessInputRejected(requestID, detail string, state *headlessState, out *stdoutWriter) {
	if requestID != "" {
		// A consumed command owes a terminal reply even when application
		// cancellation has closed agent admission. Keep both ordering and queue
		// admission on the writer lifetime, bounded by the output drain budget.
		ctx, cancel := out.inputReplyContext()
		defer cancel()
		out.orderedWithContext(ctx, func() {
			env := &headlessEnvelope{Type: "input_result", Payload: agent.InputResultEvent{RequestID: requestID, Status: agent.InputRejected, Message: detail}}
			state.mu.Lock()
			state.stampHeadlessSeq(true, env)
			state.mu.Unlock()
			out.emitWithContext(ctx, env)
		})
	}
}
