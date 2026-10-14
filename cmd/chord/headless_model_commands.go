package main

import (
	"strings"
)

func emitHeadlessModelsResponse(out *stdoutWriter, ok bool, message string, status string) {
	payload := map[string]any{"ok": ok}
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
		emitHeadlessModelsResponse(out, true, "", backend.ModelsStatusText())
	case "set_current_model_pool":
		pool := strings.TrimSpace(cmd.Pool)
		if pool == "" {
			emitHeadlessModelsResponse(out, false, "models set_current_model_pool requires pool", "")
			return
		}
		if err := backend.SetCurrentModelPool(pool); err != nil {
			emitHeadlessModelsResponse(out, false, err.Error(), "")
			return
		}
		emitHeadlessModelsResponse(out, true, "model pool set", backend.ModelsStatusText())
	default:
		emitHeadlessModelsResponse(out, false, "unsupported models action: "+cmd.Action, "")
	}
}
