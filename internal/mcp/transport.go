package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"
)

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 types
// ---------------------------------------------------------------------------

// JSONRPCRequest is a JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// JSONRPCNotification is a JSON-RPC 2.0 notification (no ID, no response expected).
type JSONRPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError is a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// ---------------------------------------------------------------------------
// Transport interface
// ---------------------------------------------------------------------------

// Transport sends a JSON-RPC request and returns the response.
type Transport interface {
	// Send sends a JSON-RPC request and waits for the corresponding response.
	Send(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error)
	// Notify sends a JSON-RPC notification (no response expected).
	Notify(ctx context.Context, notif JSONRPCNotification) error
	// Close cleans up transport resources (processes, connections, etc.).
	Close() error
}

// ---------------------------------------------------------------------------
// StdioTransport — launches an external process and communicates via
// stdin/stdout using newline-delimited JSON-RPC 2.0.
// ---------------------------------------------------------------------------

const (
	stdioKillGrace = 200 * time.Millisecond
)

// StdioTransport manages a child process for MCP communication over stdio.
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	mu      sync.Mutex
	pending map[int]chan JSONRPCResponse
	closed  bool

	// writeMu serialises all writes to stdin so that concurrent Send and
	// Notify calls cannot interleave bytes and corrupt JSON-RPC frames.
	writeMu sync.Mutex

	// done is closed when the reader goroutine exits.
	done chan struct{}
}

// NewStdioTransport starts the external command and returns a transport.
// The caller must call Close to terminate the child process.
func NewStdioTransport(ctx context.Context, command string, args []string, env []string) (*StdioTransport, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	configureStdioCommand(cmd)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		stdinPipe.Close()
		return nil, fmt.Errorf("mcp stdio: stdout pipe: %w", err)
	}
	// Discard stderr to avoid unbounded memory growth; output is not consumed.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		stdinPipe.Close()
		stdoutPipe.Close()
		return nil, fmt.Errorf("mcp stdio: start process %q: %w", command, err)
	}

	t := &StdioTransport{
		cmd:     cmd,
		stdin:   stdinPipe,
		stdout:  stdoutPipe,
		pending: make(map[int]chan JSONRPCResponse),
		done:    make(chan struct{}),
	}

	// Background reader: reads JSON-RPC responses from stdout and dispatches
	// them to the correct pending caller by ID.
	go t.readLoop()

	return t, nil
}

// readLoop reads newline-delimited JSON from the child's stdout and routes
// each response to the pending waiter identified by the response ID.
func (t *StdioTransport) readLoop() {
	defer close(t.done)
	scanner := bufio.NewScanner(t.stdout)
	// Allow up to 10 MB per line (some MCP servers return large results).
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			log.Debugf("mcp stdio: ignoring non-JSON line line=%v", string(line))
			continue
		}

		// Notifications from server (ID == 0 with no result) are logged and dropped.
		if resp.ID == 0 && resp.Result == nil && resp.Error == nil {
			log.Debugf("mcp stdio: received server notification line=%v", string(line))
			continue
		}

		t.mu.Lock()
		ch, ok := t.pending[resp.ID]
		if ok {
			delete(t.pending, resp.ID)
		}
		t.mu.Unlock()

		if ok {
			ch <- resp
		} else {
			log.Debugf("mcp stdio: no waiter for response id=%v", resp.ID)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Debugf("mcp stdio: reader stopped error=%v", err)
	}

	// Fail all pending requests.
	t.mu.Lock()
	for id, ch := range t.pending {
		ch <- JSONRPCResponse{
			ID: id,
			Error: &JSONRPCError{
				Code:    -32000,
				Message: "transport closed",
			},
		}
		delete(t.pending, id)
	}
	t.mu.Unlock()
}

// Send writes a JSON-RPC request to the process stdin and waits for the
// matching response from stdout. It respects context cancellation.
func (t *StdioTransport) Send(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return JSONRPCResponse{}, fmt.Errorf("mcp stdio: transport closed")
	}

	ch := make(chan JSONRPCResponse, 1)
	t.pending[req.ID] = ch
	t.mu.Unlock()

	// Marshal and write.
	data, err := json.Marshal(req)
	if err != nil {
		t.removePending(req.ID)
		return JSONRPCResponse{}, fmt.Errorf("mcp stdio: marshal request: %w", err)
	}
	data = append(data, '\n')

	t.writeMu.Lock()
	_, writeErr := t.stdin.Write(data)
	t.writeMu.Unlock()
	if writeErr != nil {
		t.removePending(req.ID)
		return JSONRPCResponse{}, fmt.Errorf("mcp stdio: write request: %w", writeErr)
	}

	// Wait for response or cancellation.
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		t.removePending(req.ID)
		return JSONRPCResponse{}, ctx.Err()
	case <-t.done:
		return JSONRPCResponse{}, fmt.Errorf("mcp stdio: transport closed while waiting for response")
	}
}

// Notify sends a JSON-RPC notification (fire-and-forget, no response).
func (t *StdioTransport) Notify(_ context.Context, notif JSONRPCNotification) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("mcp stdio: transport closed")
	}
	t.mu.Unlock()

	data, err := json.Marshal(notif)
	if err != nil {
		return fmt.Errorf("mcp stdio: marshal notification: %w", err)
	}
	data = append(data, '\n')

	t.writeMu.Lock()
	_, writeErr := t.stdin.Write(data)
	t.writeMu.Unlock()
	if writeErr != nil {
		return fmt.Errorf("mcp stdio: write notification: %w", writeErr)
	}
	return nil
}

// Close terminates the child process and releases resources.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	// Close stdin to signal the child process.
	t.stdin.Close()

	// Terminate the child process group.
	terminateStdioCommand(t.cmd)

	// Wait for the reader goroutine to finish (bounded).
	select {
	case <-t.done:
	case <-time.After(stdioKillGrace):
		// Force kill.
		terminateStdioCommand(t.cmd)
		<-t.done
	}

	// Reap the child process.
	_ = t.cmd.Wait()
	return nil
}

func (t *StdioTransport) removePending(id int) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

// ---------------------------------------------------------------------------
// HTTPTransport — Streamable HTTP / JSON-over-HTTP (MCP remote servers).
//
// Supports:
//   - application/json single JSON-RPC response (simple HTTP MCP)
//   - text/event-stream SSE with "data: {jsonrpc...}" lines (Exa, Streamable HTTP)
//   - Mcp-Session-Id response header stored and sent on subsequent requests
// ---------------------------------------------------------------------------

const mcpSessionHeader = "Mcp-Session-Id"

// HTTPTransport communicates with an MCP server over HTTP.
type HTTPTransport struct {
	url    string
	client *http.Client

	mu        sync.Mutex
	sessionID string
}

// NewHTTPTransport creates an HTTP-based transport for the given URL.
func NewHTTPTransport(url string) *HTTPTransport {
	return &HTTPTransport{
		url: url,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (t *HTTPTransport) applySession(h http.Header) {
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()
	if sid != "" {
		h.Set(mcpSessionHeader, sid)
	}
}

func (t *HTTPTransport) rememberSession(h http.Header) {
	if sid := h.Get(mcpSessionHeader); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}
}

// readJSONRPCResponse reads a JSON-RPC response: either one JSON object or SSE data lines.
func readJSONRPCResponse(body io.Reader, contentType string, wantID int) (JSONRPCResponse, error) {
	ct := strings.ToLower(contentType)
	br := bufio.NewReader(body)
	if strings.Contains(ct, "text/event-stream") {
		return readSSERPCResponse(br, wantID)
	}
	for {
		b, err := br.Peek(1)
		if err != nil {
			if err == io.EOF {
				return JSONRPCResponse{}, fmt.Errorf("mcp http: empty response body")
			}
			return JSONRPCResponse{}, fmt.Errorf("mcp http: peek response: %w", err)
		}
		if b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r' {
			_, _ = br.ReadByte()
			continue
		}
		break
	}
	peek, _ := br.Peek(5)
	if len(peek) >= 5 && bytes.Equal(peek[:5], []byte("data:")) {
		return readSSERPCResponse(br, wantID)
	}
	var resp JSONRPCResponse
	if err := json.NewDecoder(br).Decode(&resp); err != nil {
		return JSONRPCResponse{}, fmt.Errorf("mcp http: decode response: %w", err)
	}
	if resp.ID != wantID && wantID != 0 {
		return JSONRPCResponse{}, fmt.Errorf("mcp http: response id %d, want %d", resp.ID, wantID)
	}
	return resp, nil
}

func readSSERPCResponse(body io.Reader, wantID int) (JSONRPCResponse, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var resp JSONRPCResponse
		if err := json.Unmarshal([]byte(payload), &resp); err != nil {
			continue
		}
		if resp.ID != wantID {
			continue
		}
		return resp, nil
	}
	if err := sc.Err(); err != nil {
		return JSONRPCResponse{}, fmt.Errorf("mcp http: read SSE: %w", err)
	}
	return JSONRPCResponse{}, fmt.Errorf("mcp http: no JSON-RPC response with id %d in SSE stream", wantID)
}

// Send posts a JSON-RPC request and reads the response.
func (t *HTTPTransport) Send(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return JSONRPCResponse{}, fmt.Errorf("mcp http: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(data))
	if err != nil {
		return JSONRPCResponse{}, fmt.Errorf("mcp http: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	t.applySession(httpReq.Header)

	httpResp, err := t.client.Do(httpReq)
	if err != nil {
		return JSONRPCResponse{}, fmt.Errorf("mcp http: send request: %w", err)
	}
	defer httpResp.Body.Close()

	t.rememberSession(httpResp.Header)

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		return JSONRPCResponse{}, fmt.Errorf("mcp http: server returned %d: %s", httpResp.StatusCode, string(body))
	}

	return readJSONRPCResponse(httpResp.Body, httpResp.Header.Get("Content-Type"), req.ID)
}

// Notify sends a JSON-RPC notification via HTTP POST (fire-and-forget).
func (t *HTTPTransport) Notify(ctx context.Context, notif JSONRPCNotification) error {
	data, err := json.Marshal(notif)
	if err != nil {
		return fmt.Errorf("mcp http: marshal notification: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("mcp http: create notification request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	t.applySession(httpReq.Header)

	httpResp, err := t.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("mcp http: send notification: %w", err)
	}
	defer httpResp.Body.Close()
	t.rememberSession(httpResp.Header)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 2048))
		return fmt.Errorf("mcp http: notification returned %d: %s", httpResp.StatusCode, string(body))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(httpResp.Body, 1<<20))
	return nil
}

// Close is a no-op for HTTP transport.
func (t *HTTPTransport) Close() error {
	return nil
}
