package llm

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/jsonx"
	"github.com/keakon/chord/internal/message"
)

// LLMDump captures a complete LLM request/response cycle for debugging.
// Each dump is written as a JSON file to the configured dump directory.
type LLMDump struct {
	Timestamp      string          `json:"timestamp"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	RequestBody    json.RawMessage `json:"request_body"`
	RequestHeaders http.Header     `json:"request_headers,omitempty"`
	HTTPStatus     int             `json:"http_status,omitzero"`
	HTTPHeaders    http.Header     `json:"http_headers,omitempty"`
	HTTPBody       string          `json:"http_body,omitempty"`
	SSEChunks      []string        `json:"sse_chunks"`
	Response       *DumpResponse   `json:"response,omitzero"`
	Recovery       *DumpRecovery   `json:"recovery,omitzero"`
	Error          string          `json:"error,omitempty"`
	DurationMS     int64           `json:"duration_ms"`
}

// DumpRecovery captures structured recovery metadata useful for diagnosing
// oversize recovery, compaction recovery, and other control-flow decisions.
type DumpRecovery struct {
	Reason string `json:"reason,omitempty"`
	Stage  string `json:"stage,omitempty"`
	Action string `json:"action,omitempty"`
	Code   string `json:"code,omitempty"`
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
	CacheReadTokens  int `json:"cache_read_tokens,omitzero"`
	CacheWriteTokens int `json:"cache_write_tokens,omitzero"`
	ReasoningTokens  int `json:"reasoning_tokens,omitzero"`
}

// DumpWriter writes LLM dump files to a directory.
type DumpWriter struct {
	dir  string
	seq  uint64
	dirs map[string]*dumpDirState
	mu   sync.Mutex // serialise directory updates/creation and sequence allocation
}

// dumpDirState tracks the sequence high-water mark for one dump directory.
// The high-water mark includes writes that reserved a sequence but have not
// published yet, so switching directories cannot rewind the allocator below
// an in-flight write and reuse its filename.
type dumpDirState struct {
	high    uint64
	pending uint64
}

// NewDumpWriter creates a DumpWriter that writes JSON files to dir.
// The directory is created lazily on the first Write call.
func NewDumpWriter(dir string) *DumpWriter {
	w := &DumpWriter{dirs: make(map[string]*dumpDirState)}
	w.SetDir(dir)
	return w
}

// SetDir updates the target dump directory for subsequent writes.
func (w *DumpWriter) SetDir(dir string) {
	if w == nil {
		return
	}
	dir = strings.TrimSpace(dir)
	w.mu.Lock()
	oldDir := w.dir
	w.dir = dir
	next := nextDumpSequenceForDir(dir)
	if state := w.dirs[dir]; state != nil && state.high >= next {
		next = state.high + 1
	}
	w.seq = next - 1
	if oldDir != dir {
		if state := w.dirs[oldDir]; state != nil && state.pending == 0 {
			delete(w.dirs, oldDir)
		}
	}
	w.mu.Unlock()
}

func nextDumpSequenceForDir(dir string) uint64 {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return 1
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}
	var maxSeq uint64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		prefix, _, ok := strings.Cut(strings.TrimSpace(entry.Name()), "_")
		if !ok || prefix == "" {
			continue
		}
		seq, err := strconv.ParseUint(prefix, 10, 64)
		if err != nil {
			continue
		}
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq + 1
}

// reserveSeq creates the current dump directory if needed and reserves the
// next sequence for it. The caller must call releaseSeq when the write ends.
func (w *DumpWriter) reserveSeq() (string, uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	dir := w.dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, fmt.Errorf("create dump dir: %w", err)
	}
	if w.dirs == nil {
		w.dirs = make(map[string]*dumpDirState)
	}
	state := w.dirs[dir]
	if state == nil {
		state = &dumpDirState{}
		w.dirs[dir] = state
	}
	w.seq++
	seq := w.seq
	if seq > state.high {
		state.high = seq
	}
	state.pending++
	return dir, seq, nil
}

func (w *DumpWriter) releaseSeq(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := w.dirs[dir]
	if state == nil {
		return
	}
	if state.pending > 0 {
		state.pending--
	}
	if state.pending == 0 && dir != w.dir {
		delete(w.dirs, dir)
	}
}

// Write persists a dump to a JSON file.
// File naming: {seq}_{timestamp}_{sanitized-provider}_{sanitized-model}.json
//
// The dump is encoded field by field so a large request body or chunk list is
// never duplicated as one encoded document. RequestBody is written as-is when
// it is already valid JSON; asynchronous callers must copy it before starting
// the write because Write does not retain or mutate the bytes.
func (w *DumpWriter) Write(dump *LLMDump) error {
	if dump == nil {
		return fmt.Errorf("dump is nil")
	}
	requestBody, err := normalizeDumpRequestBody(dump.RequestBody)
	if err != nil {
		return fmt.Errorf("normalize dump request body: %w", err)
	}
	requestHeaders := dumpRequestHeaders(dump.RequestHeaders)

	dir, seq, err := w.reserveSeq()
	if err != nil {
		return err
	}
	defer w.releaseSeq(dir)

	ts := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf(
		"%04d_%s_%s_%s.json",
		seq,
		ts,
		sanitizeDumpNamePart(dump.Provider),
		sanitizeDumpNamePart(dump.Model),
	)
	path := filepath.Join(dir, filename)

	// Publish by rename so a failed or interrupted write never leaves a partial
	// .json file that looks complete.
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create dump temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	bw := bufio.NewWriterSize(tmp, 64<<10)
	size, err := writeDumpJSON(bw, dump, requestBody, requestHeaders)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write dump: %w", err)
	}
	if err := bw.Flush(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("flush dump: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close dump: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish dump file: %w", err)
	}

	log.Debugf("LLM dump written path=%v size=%v provider=%v model=%v", path, size, dump.Provider, dump.Model)
	return nil
}

type dumpCountingWriter struct {
	w io.Writer
	n int64
}

func (w *dumpCountingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}

func (w *dumpCountingWriter) WriteString(s string) (int, error) {
	n, err := io.WriteString(w.w, s)
	w.n += int64(n)
	return n, err
}

type dumpJSONWriter struct {
	w          io.Writer
	err        error
	wroteField bool
}

func (w *dumpJSONWriter) writeString(s string) {
	if w.err != nil {
		return
	}
	_, w.err = io.WriteString(w.w, s)
}

func (w *dumpJSONWriter) writeBytes(p []byte) {
	if w.err != nil {
		return
	}
	_, w.err = w.w.Write(p)
}

func (w *dumpJSONWriter) beginField(name string) {
	if w.err != nil {
		return
	}
	if w.wroteField {
		w.writeString(",")
	} else {
		w.wroteField = true
	}
	w.writeString("\n  ")
	nameJSON, err := jsonx.Marshal(name)
	if err != nil {
		w.err = err
		return
	}
	w.writeBytes(nameJSON)
	w.writeString(": ")
}

func (w *dumpJSONWriter) field(name string, value any) {
	if w.err != nil {
		return
	}
	w.beginField(name)
	if w.err != nil {
		return
	}
	data, err := jsonx.Marshal(value)
	if err != nil {
		w.err = err
		return
	}
	w.writeBytes(data)
}

func (w *dumpJSONWriter) rawField(name string, value []byte) {
	if w.err != nil {
		return
	}
	w.beginField(name)
	if w.err != nil {
		return
	}
	if len(value) == 0 {
		w.writeString("null")
		return
	}
	w.writeBytes(value)
}

func (w *dumpJSONWriter) stringArrayField(name string, values []string) {
	if w.err != nil {
		return
	}
	w.beginField(name)
	if w.err != nil {
		return
	}
	if values == nil {
		w.writeString("null")
		return
	}
	w.writeString("[")
	for i, value := range values {
		if w.err != nil {
			return
		}
		if i > 0 {
			w.writeString(",")
		}
		w.writeString("\n    ")
		// jsonx.Marshal emits no trailing newline, so the array layout stays intact.
		data, err := jsonx.Marshal(value)
		if err != nil {
			w.err = err
			return
		}
		w.writeBytes(data)
	}
	if len(values) > 0 {
		w.writeString("\n  ")
	}
	w.writeString("]")
}

func writeDumpJSON(out io.Writer, dump *LLMDump, requestBody json.RawMessage, requestHeaders http.Header) (int64, error) {
	counter := &dumpCountingWriter{w: out}
	w := &dumpJSONWriter{w: counter}
	w.writeString("{")
	w.field("timestamp", dump.Timestamp)
	w.field("provider", dump.Provider)
	w.field("model", dump.Model)
	w.rawField("request_body", requestBody)
	if len(requestHeaders) > 0 {
		w.field("request_headers", requestHeaders)
	}
	if dump.HTTPStatus != 0 {
		w.field("http_status", dump.HTTPStatus)
	}
	if len(dump.HTTPHeaders) > 0 {
		w.field("http_headers", dump.HTTPHeaders)
	}
	if dump.HTTPBody != "" {
		w.field("http_body", dump.HTTPBody)
	}
	w.stringArrayField("sse_chunks", dump.SSEChunks)
	if dump.Response != nil {
		w.field("response", dump.Response)
	}
	if dump.Recovery != nil {
		w.field("recovery", dump.Recovery)
	}
	if dump.Error != "" {
		w.field("error", dump.Error)
	}
	w.field("duration_ms", dump.DurationMS)
	w.writeString("\n}\n")
	return counter.n, w.err
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
	// Valid JSON is returned without copying; the dump writer only reads it.
	if json.Valid(body) {
		return body, nil
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

func dumpHTTPResponseMetadata(resp *http.Response) (int, http.Header) {
	if resp == nil {
		return 0, nil
	}
	return resp.StatusCode, resp.Header.Clone()
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
type providerDumpWriterSetter interface {
	SetDumpWriter(*DumpWriter)
}

func SetProviderDumpWriter(p Provider, w *DumpWriter) {
	if impl, ok := p.(providerDumpWriterSetter); ok {
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
