package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/jsonx"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestToolImagePersistenceAndRecall(t *testing.T) {
	for _, speculative := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "speculative"}[speculative], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "shot.png")
			var data bytes.Buffer
			if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			a := newImageReductionTestAgent(t)
			session := filepath.Join(root, "session")
			pipeline := toolExecutionPipeline{registry: a.tools, sessionDir: session}
			args, err := jsonx.Marshal(map[string]string{"path": path})
			if err != nil {
				t.Fatal(err)
			}
			call := message.ToolCall{ID: "image-call", Name: tools.NameViewImage, Args: args}
			var result ToolExecutionResult
			if speculative {
				result, err = pipeline.executeSpeculative(t.Context(), call)
			} else {
				result, err = pipeline.execute(t.Context(), call, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Images) != 1 || result.Images[0].ImagePath == "" || len(result.Images[0].Data) != 0 {
				t.Fatalf("tool result must have a session-owned path before commit: %+v", result.Images)
			}
			saved := result.Images[0].ImagePath
			originalBytes, err := os.ReadFile(saved)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(originalBytes, data.Bytes()) {
				t.Fatal("session archive changed the image bytes")
			}
			// The source is transient; recall must use the immutable session copy.
			if err = os.WriteFile(path, []byte("replaced"), 0o600); err != nil {
				t.Fatal(err)
			}
			parts := toolResultParts(result.Result, result.Images)
			msgs := imageEvictionMessages(1, parts)
			msgs[1].ToolCalls[0] = call
			msgs[2].Content = result.Result
			msgs[2].ToolCallID = call.ID
			a.ctxMgr.Append(msgs[2])
			manager := recovery.NewRecoveryManager(session)
			t.Cleanup(func() { _ = manager.Close() })
			if err = manager.PersistMessageDurable(identity.MainAgentID, msgs[2]); err != nil {
				t.Fatal(err)
			}
			record, err := manager.PersistedRecord(msgs[2])
			if err != nil {
				t.Fatal(err)
			}
			var restored message.Message
			if err = json.Unmarshal(record, &restored); err != nil {
				t.Fatal(err)
			}
			if !message.ContentPartsEqual(restored.Parts, a.ctxMgr.Snapshot()[0].Parts) {
				t.Fatal("durable and active history disagree on the image archive")
			}
			setTestRequestBatch(a, nil, 3)
			prepared := a.prepareMessagesForLLM(msgs)
			marker := prepared[2].Parts[1].Text
			if !strings.Contains(marker, saved) || !strings.Contains(marker, tools.NameViewImage) {
				t.Fatalf("active session lost its recall route: %s", marker)
			}
			b := newImageReductionTestAgent(t)
			restoredMsgs := append([]message.Message(nil), msgs...)
			restoredMsgs[2] = restored
			setTestRequestBatch(b, nil, 3)
			if restoredMarker := b.prepareMessagesForLLM(restoredMsgs)[2].Parts[1].Text; restoredMarker != marker {
				t.Fatalf("restore changed the image marker: %s != %s", restoredMarker, marker)
			}
			recalled := &tools.ImageCollector{}
			args, err = jsonx.Marshal(map[string]string{"path": saved})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tools.NewViewImageTool(a).Execute(tools.WithImageSink(t.Context(), recalled), args)
			if err != nil {
				t.Fatalf("saved image could not be recalled: %v", err)
			}
			if got := recalled.Drain(); len(got) != 1 || !bytes.Equal(got[0].Data, originalBytes) {
				t.Fatal("recall did not restore the archived pixels")
			}
		})
	}
}

type inlineImageResultTool struct {
	err error
}

func (inlineImageResultTool) Name() string        { return "sample_image" }
func (inlineImageResultTool) Description() string { return "Return an image." }
func (inlineImageResultTool) IsReadOnly() bool    { return true }
func (inlineImageResultTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (tool inlineImageResultTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	sink, _ := tools.ImageSinkFromContext(ctx)
	sink.AddImage(message.ContentPart{Type: message.ContentPartImage, MimeType: "image/png", Data: []byte("image bytes")})
	return "Image result.", tool.err
}

func TestToolImageArchiveKeepsDataOnFailure(t *testing.T) {
	for name, toolErr := range map[string]error{"success": nil, "error": errors.New("sample failure"), "cancelled": context.Canceled} {
		t.Run(name, func(t *testing.T) {
			for _, hasSession := range []bool{false, true} {
				registry := tools.NewRegistry()
				registry.Register(inlineImageResultTool{err: toolErr})
				session := ""
				if hasSession {
					session = t.TempDir()
					if err := os.WriteFile(filepath.Join(session, "images"), []byte("blocked"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				pipeline := toolExecutionPipeline{registry: registry, sessionDir: session}
				result, err := pipeline.execute(t.Context(), message.ToolCall{ID: "image-call", Name: "sample_image", Args: json.RawMessage(`{}`)}, false)
				if !errors.Is(err, toolErr) {
					t.Fatalf("archive error changed the tool outcome: %v", err)
				}
				if len(result.Images) != 1 || string(result.Images[0].Data) != "image bytes" || result.Images[0].ImagePath != "" {
					t.Fatalf("inline image data lost on archive failure: %+v", result.Images)
				}
			}
		})
	}
}
