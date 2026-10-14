package llm

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// convertMessagesToResponses is the test-default constructor most conversion
// assertions use: no item ids, function-shape replay.
func convertMessagesToResponses(systemPrompt string, msgs []message.Message) []responsesInputItem {
	return convertMessagesToResponsesWithItemIDs(systemPrompt, msgs, false, false, false)
}

// buildSSEStream returns an io.Reader that yields SSE lines (event: + data:).
// Each pair is one event; dataLines use official format (full JSON with type, output_index, etc.).
func buildSSEStream(dataLines []string) *bytes.Reader {
	var b strings.Builder
	for _, d := range dataLines {
		// Infer event type from data for standard SSE (event line then data line).
		var ev struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(d), &ev)
		eventType := ev.Type
		if eventType == "" {
			eventType = "unknown"
		}
		b.WriteString("event: ")
		b.WriteString(eventType)
		b.WriteString("\ndata: ")
		b.WriteString(d)
		b.WriteString("\n\n")
	}
	return bytes.NewReader([]byte(b.String()))
}

type errorAfterReader struct {
	payload []byte
	err     error
	done    bool
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.payload), nil
}

type blockingAfterPayloadReader struct {
	payload []byte
	done    bool
}

func (r *blockingAfterPayloadReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.payload), nil
	}
	select {}
}

type chunkTimeoutAfterPayloadReader struct {
	payload []byte
	done    bool
}

func (r *chunkTimeoutAfterPayloadReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.payload), nil
	}
	return 0, &ChunkTimeoutError{d: TerminalDrainChunkTimeout}
}

// recordingPhaser records the timeout transitions a parser requests so tests
// can assert the terminal drain was armed.
type recordingPhaser struct {
	lastChunk time.Duration
	lastDrain time.Duration
	drainSet  bool
}

func (p *recordingPhaser) SetChunkTimeout(d time.Duration) { p.lastChunk = d }
func (p *recordingPhaser) SetTerminalDrainTimeout(d time.Duration) {
	p.lastDrain = d
	p.drainSet = true
}

func TestResponsesRequestSignatureIgnoresInputButTracksNonInputFields(t *testing.T) {
	reqA := &responsesRequest{
		Model:          "gpt-5",
		Tools:          []responsesTool{{Type: "function", Name: "Read"}},
		Stream:         true,
		Input:          []responsesInputItem{{Type: "message", Role: "user", Content: "one"}},
		PromptCacheKey: "sid",
	}
	reqB := &responsesRequest{
		Model:          "gpt-5",
		Tools:          []responsesTool{{Type: "function", Name: "Read"}},
		Stream:         true,
		Input:          []responsesInputItem{{Type: "message", Role: "user", Content: "two"}},
		PromptCacheKey: "sid",
	}
	if responsesRequestSignature(reqA) != responsesRequestSignature(reqB) {
		t.Fatal("signature should ignore input differences")
	}
	reqB.ClientMetadata = map[string]string{
		responsesClientMetadataTurnID:       "turn-2",
		responsesClientMetadataTurnMetadata: `{"turn_id":"turn-2","turn_started_at_unix_ms":2}`,
	}
	if responsesRequestSignature(reqA) != responsesRequestSignature(reqB) {
		t.Fatal("signature should ignore request-scoped client metadata differences")
	}
	reqB.Tools = []responsesTool{{Type: "function", Name: "Shell"}}
	if responsesRequestSignature(reqA) == responsesRequestSignature(reqB) {
		t.Fatal("signature should change when non-input fields change")
	}
}

func TestResponsesInputSignature_PrefixConsistency(t *testing.T) {
	items := []responsesInputItem{
		{Type: "message", Role: "user", Content: "hello"},
		{Type: "message", Role: "assistant", Content: "world"},
		{Type: "message", Role: "user", Content: "more"},
	}
	pfxSig := responsesInputPrefixSignature(items, 2)
	fullSig := responsesInputSignature(items[:2])
	if pfxSig != fullSig {
		t.Errorf("prefix sig = %q, full sig = %q; must match", pfxSig, fullSig)
	}
	pfx1 := responsesInputPrefixSignature(items, 1)
	if pfx1 == pfxSig {
		t.Errorf("prefix sig for len=1 must differ from len=2")
	}
	modified := make([]responsesInputItem, len(items))
	copy(modified, items)
	modified[0].Content = "different"
	if responsesInputPrefixSignature(modified, 2) == pfxSig {
		t.Errorf("modified prefix sig should differ from original")
	}
}

func TestOpenAIProvider_PersistsResponsesProvider(t *testing.T) {
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Preset: config.ProviderPresetCodex,
		APIURL: "https://example.com/v1/responses",
	}, nil)
	o, err := NewOpenAIProvider(providerCfg, "")
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if o.responsesProvider == nil {
		t.Fatal("OpenAIProvider.responsesProvider should be set in constructor")
	}
	rp1 := o.responsesProvider
	rp2 := o.responsesProvider
	if rp1 != rp2 {
		t.Error("responsesProvider must be the same instance")
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
