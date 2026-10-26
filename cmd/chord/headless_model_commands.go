package main

import (
	"strings"
)

func emitHeadlessModelsResponse(out *stdoutWriter, requestID string, ok bool, message string, status string) {
	payload := map[string]any{"ok": ok}
	if requestID != "" {
		payload["request_id"] = requestID
	}
	if message != "" {
		payload["message"] = message
	}
	if status != "" {
		payload["status"] = status
	}
	out.emit(headlessEnvelope{Type: "models_response", Payload: payload})
}

func handleHeadlessModelsCommand(cmd headlessCommand, backend headlessModelsBackend, out *stdoutWriter) {
	switch strings.TrimSpace(cmd.Action) {
	case "", "status":
		emitHeadlessModelsResponse(out, cmd.RequestID, true, "", backend.ModelsStatusText())
	case "set_current_model_pool":
		pool := strings.TrimSpace(cmd.Pool)
		if pool == "" {
			emitHeadlessModelsResponse(out, cmd.RequestID, false, "models set_current_model_pool requires pool", "")
			return
		}
		if err := backend.SetCurrentModelPool(pool); err != nil {
			emitHeadlessModelsResponse(out, cmd.RequestID, false, err.Error(), "")
			return
		}
		emitHeadlessModelsResponse(out, cmd.RequestID, true, "model pool set", backend.ModelsStatusText())
	default:
		emitHeadlessModelsResponse(out, cmd.RequestID, false, "unsupported models action: "+cmd.Action, "")
	}
}
