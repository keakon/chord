package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestImageEvictionRequestPayloadAndDurableHistory(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		select {
		case requests <- body:
		default:
			t.Error("unexpected additional provider request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg-1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Images received\"}]}]}}\n\n")
	}))
	defer server.Close()
	provider := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeResponses, APIURL: server.URL,
		Models: map[string]config.ModelConfig{"test-model": {
			Limit:      config.ModelLimit{Context: 32000, Output: 1024},
			Modalities: &config.ModelModalities{Input: []string{"text", "image"}},
		}},
	}, []string{"test-key"})
	impl, err := llm.NewResponsesProvider(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient(provider, impl, "test-model", 1024, "")
	defer client.Close()
	a := newImageReductionTestAgent(t)
	a.llmClient = client
	llm.SetBinaryPartResolver(a.resolveBinaryPart)
	t.Cleanup(func() { llm.SetBinaryPartResolver(nil) })
	a.projectConfig.Context.Reduction.StaleAgeTurns = 3
	session := t.TempDir()
	manager := recovery.NewRecoveryManager(session)
	t.Cleanup(func() { _ = manager.Close() })
	encodeImage := func(c color.RGBA) []byte {
		t.Helper()
		img := image.NewRGBA(image.Rect(0, 0, 4, 4))
		img.SetRGBA(0, 0, c)
		var data bytes.Buffer
		if err := png.Encode(&data, img); err != nil {
			t.Fatal(err)
		}
		return data.Bytes()
	}
	oldData := encodeImage(color.RGBA{R: 255, A: 255})
	freshData := encodeImage(color.RGBA{B: 255, A: 255})
	parts, err := manager.NormalizeAttachmentParts([]message.ContentPart{
		{Type: message.ContentPartImage, MimeType: "image/png", Data: oldData},
		{Type: message.ContentPartImage, MimeType: "image/png", Data: freshData},
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := imageEvictionMessages(1, toolResultParts("Old image.", parts[:1]))
	msgs[2].Content = "Old image."
	fresh := imageEvictionMessages(3, toolResultParts("Fresh image.", parts[1:]))
	fresh[1].ToolCalls[0].ID = "fresh-call"
	fresh[1].ToolCalls[0].Args = json.RawMessage(`{"path":"fresh.png"}`)
	fresh[2].ToolCallID = "fresh-call"
	fresh[2].Content = "Fresh image."
	msgs = append(msgs, fresh[1:]...)
	for _, msg := range msgs {
		if err = manager.PersistMessageDurable(identity.MainAgentID, msg); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(session, identity.MainSessionLogFilename)
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	setTestRequestBatch(a, nil, 4)
	prepared := a.prepareMessagesForLLM(msgs)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err = client.CompleteStream(ctx, prepared, nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-requests:
		if strings.Contains(string(body), base64.StdEncoding.EncodeToString(oldData)) {
			t.Fatal("the aged image leaked into the actual provider request")
		}
		if !strings.Contains(string(body), base64.StdEncoding.EncodeToString(freshData)) ||
			!strings.Contains(string(body), "Older image output omitted") ||
			!strings.Contains(string(body), parts[0].ImagePath) {
			t.Fatalf("request lost the fresh image or aged image recall marker: %s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the captured provider request")
	}
	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !hasImagePart(msgs[2].Parts) {
		t.Fatal("request projection rewrote durable image history")
	}
}
