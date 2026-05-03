package llm

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// LLMDump captures a complete LLM request/response cycle for debugging.
// Each dump is written as a JSON file to the configured dump directory.
type LLMDump struct {
	Timestamp   string          `json:"timestamp"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	RequestBody json.RawMessage `json:"request_body"`
	SSEChunks   []string        `json:"sse_chunks"`
	Response    *DumpResponse   `json:"response,omitempty"`
	Error       string          `json:"error,omitempty"`
	DurationMS  int64           `json:"duration_ms"`
}

// DumpResponse captures the parsed LLM response.
type DumpResponse struct {
	Content    string         `json:"content"`
	ToolCalls  []DumpToolCall `json:"tool_calls,omitempty"`
	StopReason string         `json:"stop_reason"`
	Usage      DumpTokenUsage `json:"usage"`
}

// DumpToolCall captures a single tool call in the dump.
type DumpToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// DumpTokenUsage captures token usage in the dump (input, output, cache, reasoning).
type DumpTokenUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

// DumpWriter writes LLM dump files to a directory.
type DumpWriter struct {
	dir string
	seq atomic.Uint64
	mu  sync.Mutex // serialise directory updates/creation
}

// NewDumpWriter creates a DumpWriter that writes JSON files to dir.
// The directory is created lazily on the first Write call.
func NewDumpWriter(dir string) *DumpWriter {
	return &DumpWriter{dir: dir}
}

// SetDir updates the target dump directory for subsequent writes.
func (w *DumpWriter) SetDir(dir string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.dir = dir
	w.mu.Unlock()
}

// Write persists a dump to a JSON file.
// File naming: {seq}_{timestamp}_{sanitized-provider}_{sanitized-model}.json
func (w *DumpWriter) Write(dump *LLMDump) error {
	if dump == nil {
		return fmt.Errorf("dump is nil")
	}
	requestBody, err := normalizeDumpRequestBody(dump.RequestBody)
	if err != nil {
		return fmt.Errorf("normalize dump request body: %w", err)
	}
	dumpToWrite := *dump
	dumpToWrite.RequestBody = requestBody

	w.mu.Lock()
	dir := w.dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		w.mu.Unlock()
		return fmt.Errorf("create dump dir: %w", err)
	}
	w.mu.Unlock()

	seq := w.seq.Add(1)
	ts := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf(
		"%04d_%s_%s_%s.json",
		seq,
		ts,
		sanitizeDumpNamePart(dump.Provider),
		sanitizeDumpNamePart(dump.Model),
	)
	path := filepath.Join(dir, filename)

	data, err := json.MarshalIndent(&dumpToWrite, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dump: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write dump file: %w", err)
	}

	log.Debugf("LLM dump written path=%v size=%v provider=%v model=%v", path, len(data), dump.Provider, dump.Model)
	return nil
}

func sanitizeDumpNamePart(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_', r == '@':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "._-")
	if out == "" {
		return "unknown"
	}
	return out
}

func normalizeDumpRequestBody(body json.RawMessage) (json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	if json.Valid(body) {
		return append(json.RawMessage(nil), body...), nil
	}
	decoded, err := tryGunzip(body)
	if err != nil {
		return nil, fmt.Errorf("decode gzip request body: %w", err)
	}
	if !json.Valid(decoded) {
		return nil, fmt.Errorf("request body is not valid json")
	}
	return json.RawMessage(decoded), nil
}

func tryGunzip(body []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create gzip reader: %w", err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read gzip body: %w", err)
	}
	if err := reader.Close(); err != nil {
		return nil, fmt.Errorf("close gzip reader: %w", err)
	}
	return decoded, nil
}

// DumpResponseFromResponse converts a message.Response to a DumpResponse.
func DumpResponseFromResponse(resp *message.Response) *DumpResponse {
	if resp == nil {
		return nil
	}
	var dumpUsage DumpTokenUsage
	if resp.Usage != nil {
		dumpUsage = DumpTokenUsage{
			InputTokens:      resp.Usage.InputTokens,
			OutputTokens:     resp.Usage.OutputTokens,
			CacheReadTokens:  resp.Usage.CacheReadTokens,
			CacheWriteTokens: resp.Usage.CacheWriteTokens,
			ReasoningTokens:  resp.Usage.ReasoningTokens,
		}
	}
	dr := &DumpResponse{
		Content:    resp.Content,
		StopReason: resp.StopReason,
		Usage:      dumpUsage,
	}
	for _, tc := range resp.ToolCalls {
		dr.ToolCalls = append(dr.ToolCalls, DumpToolCall{
			ID:   tc.ID,
			Name: tc.Name,
			Args: tc.Args,
		})
	}
	return dr
}

// SetProviderDumpWriter attaches a DumpWriter to a Provider implementation.
// It silently does nothing if the provider doesn't support dumping (future-proof).
func SetProviderDumpWriter(p Provider, w *DumpWriter) {
	switch impl := p.(type) {
	case *OpenAIProvider:
		impl.SetDumpWriter(w)
	case *AnthropicProvider:
		impl.SetDumpWriter(w)
	case *ResponsesProvider:
		impl.SetDumpWriter(w)
	}
}

// SSECollector collects SSE chunks during streaming for dump purposes.
type SSECollector struct {
	chunks []string
	mu     sync.Mutex
}

// NewSSECollector creates a new SSE chunk collector.
func NewSSECollector() *SSECollector {
	return &SSECollector{
		chunks: make([]string, 0, 64),
	}
}

// Add records a raw SSE data line.
func (c *SSECollector) Add(line string) {
	c.mu.Lock()
	c.chunks = append(c.chunks, line)
	c.mu.Unlock()
}

// Chunks returns all collected SSE data lines.
func (c *SSECollector) Chunks() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.chunks))
	copy(out, c.chunks)
	return out
}
