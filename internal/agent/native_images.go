package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) configureNativeImagePolicy(policy *llm.NativeToolPolicy, turn *Turn, registry *tools.Registry, journal recovery.NativeRequestJournal, callerID string) {
	sessionDir := journal.SessionDir
	if turn != nil {
		tool, _ := registry.Get(tools.NameGenerateImage)
		local, available := tool.(*tools.GenerateImageTool)
		available = available && local.IsAvailable()
		policy.ImageTimeout = 300 * time.Second
		if available {
			policy.ImageTimeout = local.Backend.Timeout()
		}
		var requestID string
		policy.DisableImage = turn.nativeImageFallback.Load()
		begin := policy.Begin
		policy.Begin = func(ctx context.Context, record llm.NativeRequestRecord) (string, error) {
			if record.Authorization.Tool == tools.NameGenerateImage {
				turn.nativeImageDeadline.CompareAndSwap(0, time.Now().Add(policy.ImageTimeout).UnixNano())
			}
			id, err := begin(ctx, record)
			if err == nil && record.Authorization.Tool == tools.NameGenerateImage {
				requestID = id
			}
			return id, err
		}
		policy.ImageFallback = func() bool {
			if !available || requestID == "" || turn.nativeImageFallback.Load() {
				return false
			}
			callerCtx := tools.WithAgentID(turn.Ctx, callerID)
			callerCtx = tools.WithSessionDir(callerCtx, sessionDir)
			callerCtx = tools.WithTurnID(callerCtx, turn.ID)
			if local.Backend.Check(callerCtx, imagegen.Request{}) != nil {
				return false
			}
			deadline := turn.nativeImageDeadline.Load()
			if deadline <= time.Now().UnixNano() {
				return false
			}
			marker := filepath.Join(sessionDir, "native-image-fallback", fmt.Sprintf("%x.json", sha256.Sum256([]byte(requestID))))
			data, _ := json.Marshal(struct {
				RequestID string `json:"request_id"`
				AgentID   string `json:"agent_id"`
				Deadline  int64  `json:"deadline_unix_nano"`
				TurnID    uint64 `json:"turn_id"`
			}{requestID, journal.AgentID, deadline, journal.TurnID})
			if err := privatefs.EnsureDir(sessionDir, filepath.Dir(marker)); err != nil {
				return false
			}
			if err := privatefs.WriteFileSynced(sessionDir, marker, data); err != nil {
				return false
			}
			if err := privatefs.SyncDir(filepath.Dir(marker)); err != nil {
				return false
			}
			return turn.nativeImageFallback.CompareAndSwap(false, true)
		}
	}
	policy.ProjectResponse = func(ctx context.Context, resp *message.Response) error {
		return saveNativeImages(ctx, sessionDir, resp)
	}
	policy.ImageOriginal = func(ctx context.Context, part message.ContentPart) ([]byte, error) {
		img, err := tools.ReadGeneratedImagePart(ctx, sessionDir, part)
		return img.Data, err
	}
}

// Final images are saved before the native response journal can publish success.
// Canonical receipts keep replay metadata and local original refs, without bytes.
func saveNativeImages(ctx context.Context, sessionDir string, resp *message.Response) error {
	if resp == nil || resp.Hosted == nil {
		return nil
	}
	// Raw server image bytes never enter canonical receipts, even when local
	// validation or saving fails. Keep item identities for reconciliation.
	for i, raw := range resp.Hosted.Items {
		var item map[string]json.RawMessage
		var kind string
		if json.Unmarshal(raw, &item) == nil && json.Unmarshal(item["type"], &kind) == nil && kind == message.HostedCallKindImageGeneration {
			delete(item, "result")
			resp.Hosted.Items[i], _ = json.Marshal(item)
		}
	}
	defer func() {
		for i := range resp.Hosted.Calls {
			call := &resp.Hosted.Calls[i]
			if call.Kind == message.HostedCallKindImageGeneration && len(call.Parts) == 0 {
				call.Result = nil
			}
		}
	}()
	count := 0
	for i := range resp.Hosted.Calls {
		call := &resp.Hosted.Calls[i]
		if call.Kind != message.HostedCallKindImageGeneration || call.Error != "" || len(call.Result) == 0 || len(call.Parts) > 0 {
			continue
		}
		count++
		if count > imagegen.MaxImages {
			return fmt.Errorf("native image response exceeds image limit; do not regenerate")
		}
		var item struct {
			ID            string `json:"id"`
			Result        string `json:"result"`
			RevisedPrompt string `json:"revised_prompt"`
		}
		if err := json.Unmarshal(call.Result, &item); err != nil {
			return fmt.Errorf("decode native image result: %w", err)
		}
		if item.ID == "" || item.ID != call.ID || len(item.Result) == 0 || len(item.Result) > base64.StdEncoding.EncodedLen(imagegen.MaxImageBytes) {
			return fmt.Errorf("native image result is missing or exceeds the limit; do not regenerate")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(item.Result)
		if err != nil {
			return fmt.Errorf("decode native image bytes: %w", err)
		}
		img, err := imagegen.ValidateImage(ctx, data, "")
		if err != nil {
			return err
		}
		ref, err := tools.SaveImageArtifact(ctx, sessionDir, img)
		if err != nil {
			return fmt.Errorf("image generated; save native image original could not complete; do not regenerate: %w", err)
		}
		generated := tools.GeneratedImage{ArtifactRef: ref, Reference: tools.ImageArtifactPrefix + ref.RelPath, Width: img.Width, Height: img.Height, RevisedPrompt: item.RevisedPrompt}
		summary := tools.ImageGenerationSummary{OperationID: item.ID, State: imagegen.StateSaved, Images: []tools.GeneratedImage{generated}, RequestID: resp.ProviderResponseID, BillingState: "unknown"}
		encoded, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		if err := imagegen.RetryDelivery(ctx, "persist-native-image-result", func() error {
			_, _, err := tools.SaveImmutableResult(sessionDir, "image_generation", encoded)
			return err
		}); err != nil {
			return fmt.Errorf("image saved; persist native image summary could not complete; do not regenerate: %w", err)
		}
		call.Result = encoded
		call.Parts = []message.ContentPart{{Type: message.ContentPartImage, MimeType: img.MIME, ImagePath: filepath.Join(sessionDir, filepath.FromSlash(ref.RelPath)), FileName: filepath.Base(ref.RelPath), DataBytes: ref.SizeBytes, ArtifactID: ref.ID}}
	}
	return nil
}
