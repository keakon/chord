package main

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/tools"
)

const headlessImageResult = "image_result"
const headlessImageChunkBytes = 384 << 10

// Backpressure applies before constructing the next base64 block. The writer
// owns the bounded queue; no image-sized slice of envelopes is retained here.
func emitHeadlessImageChunks(img imagegen.Image, reference, callID, requestID, agentID string, emit func(*headlessEnvelope) bool) bool {
	for offset := 0; offset < len(img.Data); offset += headlessImageChunkBytes {
		end := min(offset+headlessImageChunkBytes, len(img.Data))
		if !emit(&headlessEnvelope{Type: "image_artifact", Payload: map[string]any{
			"reference": reference, "call_id": callID, "request_id": requestID, "agent_id": agentID,
			"mime_type": img.MIME, "width": img.Width, "height": img.Height, "size_bytes": len(img.Data),
			"offset": offset, "last": end == len(img.Data), "data": base64.StdEncoding.EncodeToString(img.Data[offset:end]),
		}}) {
			return false
		}
	}
	return true
}

func headlessImageResultEnvelope(e agent.ToolResultEvent) *headlessEnvelope {
	result := e.Payload
	if result == "" {
		result = e.Result
	}
	return &headlessEnvelope{Type: headlessImageResult, Payload: map[string]any{"call_id": e.CallID, "agent_id": e.AgentID, "status": e.Status, "result": result}}
}

func emitHeadlessImageOriginals(ctx context.Context, e agent.ToolResultEvent, emit func(*headlessEnvelope) bool) {
	if e.Status != agent.ToolResultStatusSuccess {
		return
	}
	stopped := false
	err := tools.VisitGeneratedOriginals(ctx, e.Payload, e.Parts, func(ref tools.GeneratedImage, img imagegen.Image) error {
		if !emitHeadlessImageChunks(img, ref.Reference, e.CallID, "", e.AgentID, emit) {
			stopped = true
			return fmt.Errorf("image delivery stopped")
		}
		return nil
	})
	if err != nil && !stopped {
		env := headlessImageResultEnvelope(e)
		env.Payload.(map[string]any)["delivery_error"] = err.Error()
		emit(env)
	}
}

func handleHeadlessImageArtifact(cmd headlessCommand, backend headlessBackend, out *stdoutWriter) {
	sb, ok := backend.(headlessSessionBackend)
	if !ok {
		out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "Session artifacts are unavailable"}})
		return
	}
	img, err := tools.ReadGeneratedOriginal(out.sendContext(), sb.SessionDir(), cmd.Content)
	if err != nil {
		out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": fmt.Sprintf("Image artifact: %v", err)}})
		return
	}
	emitHeadlessImageChunks(img, cmd.Content, "", cmd.RequestID, "", func(env *headlessEnvelope) bool { return out.emit(env) })
}
