package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var hexDeviceIDRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestDeterministicUUIDv4IsStableAndShaped(t *testing.T) {
	first := deterministicUUIDv4([]byte("sample-session-seed"))
	if again := deterministicUUIDv4([]byte("sample-session-seed")); again != first {
		t.Fatalf("recomputing the seed gave %q, want the same %q", again, first)
	}
	if !uuidV4Re.MatchString(first) {
		t.Fatalf("uuid = %q, want a UUIDv4 shape (version 4, RFC variant bits)", first)
	}
	if other := deterministicUUIDv4([]byte("another-seed")); other == first {
		t.Fatal("two seeds produced the same UUID")
	}
}

func TestClaudeSessionIDDerivesFromSessionKey(t *testing.T) {
	key := "20260102150405123"
	sid := claudeSessionID(key)
	if !uuidV4Re.MatchString(sid) {
		t.Fatalf("session id = %q, want a UUIDv4 shape", sid)
	}
	if claudeSessionID(key) != sid {
		t.Fatal("the same session key produced a different id across calls")
	}
	if claudeSessionID("  "+key+"  ") != sid {
		t.Fatal("surrounding whitespace changed the derived id")
	}
	if sub := claudeSessionID(key + ":sub:coder-1"); sub == sid || !uuidV4Re.MatchString(sub) {
		t.Fatalf("SubAgent session id = %q, want a distinct UUIDv4 from the main one %q", sub, sid)
	}
	for _, missing := range []string{"", "   "} {
		if got := claudeSessionID(missing); got != "" {
			t.Fatalf("session id for a missing key = %q, want empty", got)
		}
	}
	// The Claude and Codex wire identities must not be slices of one digest:
	// both go on the wire, and shared bits would let one endpoint correlate them.
	if codex := deriveResponsesWireIdentity(key, 0, false).sessionID; codex == sid {
		t.Fatal("the Claude session id equals the Codex wire identity for the same key")
	}
}

func TestClaudeStainlessOSAndArchVocabulary(t *testing.T) {
	for goos, want := range map[string]string{
		"darwin": "MacOS", "linux": "Linux", "windows": "Windows", "sunos": "Unknown",
	} {
		if got := claudeStainlessOS(goos); got != want {
			t.Errorf("claudeStainlessOS(%q) = %q, want %q", goos, got, want)
		}
	}
	for goarch, want := range map[string]string{
		"amd64": "x64", "arm64": "arm64", "386": "x32", "arm": "arm", "riscv64": "unknown",
	} {
		if got := claudeStainlessArch(goarch); got != want {
			t.Errorf("claudeStainlessArch(%q) = %q, want %q", goarch, got, want)
		}
	}
}

func TestClaudeStainlessTimeoutSeconds(t *testing.T) {
	for elapsed, want := range map[time.Duration]int{
		0:                      claudeEmulationDefaultTimeoutSeconds,
		500 * time.Millisecond: claudeEmulationDefaultTimeoutSeconds,
		90 * time.Second:       90,
		600 * time.Second:      600,
		2 * time.Hour:          7200,
	} {
		if got := claudeStainlessTimeoutSeconds(elapsed); got != want {
			t.Errorf("claudeStainlessTimeoutSeconds(%v) = %d, want %d", elapsed, got, want)
		}
	}
}

func TestClaudeClientUserAgent(t *testing.T) {
	if got, want := claudeClientUserAgent("2.1.311"), "claude-cli/2.1.311 (external, cli)"; got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
}

// --- device id ---

func TestLoadClaudeDeviceIDPersistsOnceAndReuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_device_id")
	minted := strings.Repeat("ab", 32)
	mints := 0
	mint := func() string { mints++; return minted }

	if got := loadClaudeDeviceID(path, mint); got != minted {
		t.Fatalf("first load = %q, want the minted id", got)
	}
	if mints != 1 {
		t.Fatalf("mint called %d times, want once", mints)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("device id file not persisted: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("device id file mode = %v, want no group or other access", perm)
	}
	// The second load reads the persisted file and never mints again.
	if got := loadClaudeDeviceID(path, func() string { mints++; return "" }); got != minted {
		t.Fatalf("second load = %q, want the persisted id", got)
	}
	if mints != 1 {
		t.Fatalf("mint called %d times after reuse, want still once", mints)
	}
}

func TestLoadClaudeDeviceIDAdoptsConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_device_id")
	results := make([]string, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			results[i] = loadClaudeDeviceID(path, mintClaudeDeviceID)
		})
	}
	wg.Wait()
	if results[0] == "" || !hexDeviceIDRe.MatchString(results[0]) {
		t.Fatalf("device id = %q, want 64 hex characters", results[0])
	}
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Fatalf("concurrent loader %d got %q, want the winner's id %q", i, results[i], results[0])
		}
	}
}

func TestLoadClaudeDeviceIDWriteFailureKeepsUnpersistedID(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The parent "directory" is a file, so both MkdirAll and the create fail.
	path := filepath.Join(blocker, "claude_device_id")
	minted := strings.Repeat("cd", 32)
	if got := loadClaudeDeviceID(path, func() string { return minted }); got != minted {
		t.Fatalf("load = %q, want the shape-correct unpersisted id", got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("device id file exists, want nothing persisted under the blocked path")
	}
}

func TestLoadClaudeDeviceIDMintFailureReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_device_id")
	if got := loadClaudeDeviceID(path, func() string { return "" }); got != "" {
		t.Fatalf("load = %q, want empty so the caller falls back to the baseline metadata", got)
	}
}

// useTempClaudeDeviceID points the process-wide device id cache at a temp file
// before its first use, so tests never create the real ~/.config/chord file.
// Once the cache has initialized the override is a no-op, which is fine: every
// later test just needs a machine-level id, not a specific one.
func useTempClaudeDeviceID(t *testing.T) {
	t.Helper()
	original := claudeDeviceIDPathFn
	t.Cleanup(func() { claudeDeviceIDPathFn = original })
	dir := t.TempDir()
	claudeDeviceIDPathFn = func() (string, error) { return filepath.Join(dir, "claude_device_id"), nil }
	if id := claudeDeviceID(); id == "" || !hexDeviceIDRe.MatchString(id) {
		t.Fatalf("claude device id = %q, want a 64-hex value initialized from the temp path", id)
	}
}

// --- metadata payload ---

func TestAnthropicMetadataUserIDPayloadStates(t *testing.T) {
	useTempClaudeDeviceID(t)
	key := "20260102150405123"
	sessionID := claudeSessionID(key)

	emulated := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Emulation: &config.EmulationConfig{Claude: &config.ClaudeEmulationConfig{
			Version: "2.1.311", SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1",
		}},
		Emulate: config.EmulationClientClaude,
	}, nil)
	a := &AnthropicProvider{provider: emulated}
	got := a.anthropicMetadataUserIDPayload("", key)
	want := fmt.Sprintf(`{"device_id":"%s","account_uuid":"","session_id":"%s"}`, claudeDeviceID(), sessionID)
	if got != want {
		t.Fatalf("metadata.user_id = %s, want %s", got, want)
	}

	// A missing session key drops the session_id field instead of sending a
	// wrong-shaped payload.
	short := a.anthropicMetadataUserIDPayload("", "")
	wantShort := fmt.Sprintf(`{"device_id":"%s","account_uuid":""}`, claudeDeviceID())
	if short != wantShort {
		t.Fatalf("metadata.user_id without a session key = %s, want %s", short, wantShort)
	}

	// Without the emulation the baseline payload is unchanged.
	baselineProvider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	baseline := &AnthropicProvider{provider: baselineProvider}
	if got := baseline.anthropicMetadataUserIDPayload("", key); got != stableAnthropicMetadataUserIDPayload(baselineProvider) {
		t.Fatalf("baseline metadata.user_id = %s, want Chord's own stable payload", got)
	}

	// A CSPRNG failure skips the whole upgrade and returns the baseline payload.
	restore := claudeDeviceID
	claudeDeviceID = func() string { return "" }
	t.Cleanup(func() { claudeDeviceID = restore })
	if got := a.anthropicMetadataUserIDPayload("", key); got != stableAnthropicMetadataUserIDPayload(emulated) {
		t.Fatalf("metadata.user_id with a failed device id = %s, want the baseline payload", got)
	}
}

func TestClaudeEmulatedMetadataUserIDIsJSON(t *testing.T) {
	payload := claudeEmulatedMetadataUserID(strings.Repeat("ab", 32), "20260102150405123")
	var decoded struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if decoded.AccountUUID != "" || decoded.SessionID == "" || decoded.DeviceID == "" {
		t.Fatalf("decoded payload = %+v", decoded)
	}
}

// --- request capture ---

// claudeEmulationProvider builds a Messages provider carrying the emulation
// block exactly as the loader would deliver it, pointed at apiURL.
func claudeEmulationProvider(emulation *config.EmulationConfig, apiURL string, streamTotalTimeout int) *ProviderConfig {
	return NewProviderConfig("sample", config.ProviderConfig{
		Type:               config.ProviderTypeMessages,
		APIURL:             apiURL,
		Emulation:          emulation,
		Emulate:            config.EmulationClientClaude,
		StreamTotalTimeout: streamTotalTimeout,
		Models:             map[string]config.ModelConfig{"test-model": {}},
	}, []string{"test-key"})
}

// captureClaudeEmulationRequest sends one request through the provider and
// returns the headers and raw body the test server received.
func captureClaudeEmulationRequest(t *testing.T, emulation *config.EmulationConfig, tuning RequestTuning, streamTotalTimeout int) (http.Header, []byte) {
	t.Helper()
	useTempClaudeDeviceID(t)
	var gotHeader http.Header
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		data, err := readRequestBody(r)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		gotBody = data
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			"event: message_start",
			`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude","usage":{"input_tokens":1,"output_tokens":0}}}`,
			"",
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			"",
			"event: content_block_delta",
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":0}`,
			"",
			"event: message_delta",
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			"",
			"event: message_stop",
			`data: {"type":"message_stop"}`,
			"",
		}, "\n"))
	}))
	t.Cleanup(server.Close)

	a := &AnthropicProvider{provider: claudeEmulationProvider(emulation, server.URL+"/v1/messages", streamTotalTimeout), client: server.Client()}
	if _, err := a.CompleteStream(
		context.Background(), "test-key", "test-model", "you are a helpful assistant",
		[]message.Message{{Role: message.RoleUser, Content: "hello"}},
		nil, 128, tuning,
		func(message.StreamDelta) {},
	); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	return gotHeader, gotBody
}

func TestClaudeEmulationWritesClaudeCodeHeadersAndMetadata(t *testing.T) {
	key := "20260102150405123"
	claude := &config.ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1"}
	header, raw := captureClaudeEmulationRequest(t, &config.EmulationConfig{Claude: claude}, RequestTuning{SessionKey: key}, 0)

	wantHeaders := map[string]string{
		headerAccept: headerValueApplicationJSON,
		headerAnthropicDangerousDirectBrowserAccess: "true",
		headerXStainlessLang:                        "js",
		headerXStainlessRuntime:                     "node",
		headerXStainlessOS:                          claudeStainlessOS(runtime.GOOS),
		headerXStainlessArch:                        claudeStainlessArch(runtime.GOARCH),
		headerXStainlessPackageVersion:              claude.SDKVersion,
		headerXStainlessRuntimeVersion:              claude.RuntimeVersion,
		headerXStainlessRetryCount:                  "0",
		headerXStainlessTimeout:                     "600",
		headerXClaudeCodeSessionID:                  claudeSessionID(key),
	}
	for name, want := range wantHeaders {
		if got := header.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if want := claudeClientUserAgent(claude.Version); header.Get(headerUserAgent) != want {
		t.Errorf("User-Agent = %q, want %q", header.Get(headerUserAgent), want)
	}
	// The emulation is additive: Chord's own messages-wire headers stay.
	if header.Get("anthropic-version") != "2023-06-01" || header.Get("x-app") != "cli" {
		t.Errorf("baseline headers = %v, want anthropic-version and x-app kept", header)
	}
	if !strings.Contains(header.Get("anthropic-beta"), "claude-code-20250219") {
		t.Errorf("anthropic-beta = %q, want the functional beta kept", header.Get("anthropic-beta"))
	}

	var body struct {
		Metadata *struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	want := fmt.Sprintf(`{"device_id":"%s","account_uuid":"","session_id":"%s"}`, claudeDeviceID(), claudeSessionID(key))
	if body.Metadata == nil || body.Metadata.UserID != want {
		t.Fatalf("metadata.user_id = %+v, want %s", body.Metadata, want)
	}
}

func TestClaudeEmulationOverridesVersionsAndReportsStreamTimeout(t *testing.T) {
	emulation := &config.EmulationConfig{Claude: &config.ClaudeEmulationConfig{Version: "2.1.99", SDKVersion: "9.9.9", RuntimeVersion: "v22.0.0"}}
	header, _ := captureClaudeEmulationRequest(t, emulation, RequestTuning{SessionKey: "20260102150405123"}, 90)

	if want := claudeClientUserAgent("2.1.99"); header.Get(headerUserAgent) != want {
		t.Fatalf("User-Agent = %q, want %q (payload version trims and overrides)", header.Get(headerUserAgent), want)
	}
	if got := header.Get(headerXStainlessPackageVersion); got != "9.9.9" {
		t.Fatalf("X-Stainless-Package-Version = %q, want the configured SDK version", got)
	}
	if got := header.Get(headerXStainlessRuntimeVersion); got != "v22.0.0" {
		t.Fatalf("X-Stainless-Runtime-Version = %q, want the configured runtime version", got)
	}
	if got := header.Get(headerXStainlessTimeout); got != "90" {
		t.Fatalf("X-Stainless-Timeout = %q, want the configured stream cap in seconds", got)
	}
}

func TestClaudeEmulationWithoutBlockKeepsBaselineRequest(t *testing.T) {
	header, raw := captureClaudeEmulationRequest(t, nil, RequestTuning{SessionKey: "20260102150405123"}, 0)

	for _, name := range []string{
		headerAccept, headerAnthropicDangerousDirectBrowserAccess, headerXClaudeCodeSessionID,
		headerXStainlessLang, headerXStainlessRuntime, headerXStainlessOS, headerXStainlessArch,
		headerXStainlessPackageVersion, headerXStainlessRuntimeVersion,
		headerXStainlessRetryCount, headerXStainlessTimeout,
	} {
		if got := header.Get(name); got != "" {
			t.Errorf("header %s = %q, want it absent without the opt-in", name, got)
		}
	}
	if ua := header.Get(headerUserAgent); !strings.HasPrefix(ua, "chord/") {
		t.Fatalf("User-Agent = %q, want Chord's own agent without the opt-in", ua)
	}
	var body struct {
		Metadata *struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body.Metadata == nil || !strings.HasPrefix(body.Metadata.UserID, `{"device_id":"chord_`) {
		t.Fatalf("metadata.user_id = %+v, want Chord's own baseline payload", body.Metadata)
	}
}

func TestClaudeEmulationWithoutSessionKeyOmitsSessionIdentity(t *testing.T) {
	payload := &config.EmulationConfig{Claude: &config.ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1"}}
	header, raw := captureClaudeEmulationRequest(t, payload, RequestTuning{}, 0)

	if got := header.Get(headerXClaudeCodeSessionID); got != "" {
		t.Fatalf("X-Claude-Code-Session-Id = %q, want it omitted without a session key", got)
	}
	var body struct {
		Metadata *struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body.Metadata == nil {
		t.Fatal("metadata missing, want the device-only payload")
	}
	if !strings.Contains(body.Metadata.UserID, claudeDeviceID()) || strings.Contains(body.Metadata.UserID, "session_id") {
		t.Fatalf("metadata.user_id = %s, want the device-only payload without a session_id", body.Metadata.UserID)
	}
}

func TestClaudeEmulationOnlyAppliesToMessagesProviders(t *testing.T) {
	emulation := &config.EmulationConfig{Claude: &config.ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1"}}
	for _, providerType := range []string{config.ProviderTypeChatCompletions, config.ProviderTypeResponses, config.ProviderTypeGenerateContent} {
		providerCfg := NewProviderConfig("sample", config.ProviderConfig{
			Type:      providerType,
			APIURL:    "https://example.invalid/v1",
			Emulation: emulation,
			Emulate:   config.EmulationClientClaude,
			Models:    map[string]config.ModelConfig{"test-model": {}},
		}, []string{"test-key"})
		if providerCfg.claudeEmulationActive("") {
			t.Errorf("provider type %q emulates the Claude Code identity", providerType)
		}
		if ua := providerCfg.claudeEmulationUserAgent(""); ua != "" {
			t.Errorf("provider type %q User-Agent = %q, want empty", providerType, ua)
		}
	}

	messagesCfg := claudeEmulationProvider(emulation, "https://example.invalid/v1/messages", 0)
	if !messagesCfg.claudeEmulationActive("") {
		t.Fatal("a Messages provider with the block must emulate the Claude Code identity")
	}
}

// The loader only propagates complete blocks, but the gate re-checks the
// payload itself, so an incomplete block can never act even if it reaches a
// provider another way.
func TestClaudeEmulationIsInertWhenIncomplete(t *testing.T) {
	for name, payload := range map[string]*config.EmulationConfig{
		"no payload":            nil,
		"empty block":           {Claude: &config.ClaudeEmulationConfig{}},
		"missing sdk version":   {Claude: &config.ClaudeEmulationConfig{Version: "2.1.311", RuntimeVersion: "v26.12.1"}},
		"missing runtime":       {Claude: &config.ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "0.128.0"}},
		"missing cli version":   {Claude: &config.ClaudeEmulationConfig{SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1"}},
		"whitespace sdk length": {Claude: &config.ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "  ", RuntimeVersion: "v26.12.1"}},
	} {
		providerCfg := NewProviderConfig("sample", config.ProviderConfig{
			Type:      config.ProviderTypeMessages,
			Emulation: payload,
			Emulate:   config.EmulationClientClaude,
			Models:    map[string]config.ModelConfig{"test-model": {}},
		}, []string{"test-key"})
		if providerCfg.claudeEmulationActive("") {
			t.Errorf("%s: an incomplete payload must not emulate the Claude Code identity", name)
		}
		if ua := providerCfg.claudeEmulationUserAgent(""); ua != "" {
			t.Errorf("%s: User-Agent = %q, want empty when the payload is incomplete", name, ua)
		}
	}
}

func TestClaudeEmulationUserAgentPriority(t *testing.T) {
	emulation := &config.EmulationConfig{Claude: &config.ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1"}}
	// Explicit provider user_agent wins over the derived agent.
	explicit := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages, UserAgent: "my-agent/1.2.3", Emulation: emulation,
		Emulate: config.EmulationClientClaude, Models: map[string]config.ModelConfig{"test-model": {}},
	}, nil)
	header := http.Header{}
	setProviderLLMUserAgent(header, explicit, "")
	if got := header.Get(headerUserAgent); got != "my-agent/1.2.3" {
		t.Fatalf("User-Agent = %q, want the explicit provider override", got)
	}

	// A complete block derives the Claude Code agent over the default Chord one.
	derived := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages, Emulation: emulation,
		Emulate: config.EmulationClientClaude, Models: map[string]config.ModelConfig{"test-model": {}},
	}, nil)
	header = http.Header{}
	setProviderLLMUserAgent(header, derived, "")
	if got, want := header.Get(headerUserAgent), "claude-cli/2.1.311 (external, cli)"; got != want {
		t.Fatalf("User-Agent = %q, want the derived %q", got, want)
	}

	// Without the block the default Chord agent stands.
	plain := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{"test-model": {}}}, nil)
	header = http.Header{}
	setProviderLLMUserAgent(header, plain, "")
	if ua := header.Get(headerUserAgent); !strings.HasPrefix(ua, "chord/") {
		t.Fatalf("User-Agent = %q, want Chord's own agent without the opt-in", ua)
	}
}

func TestDumpRequestHeadersIncludesClaudeEmulationHeaders(t *testing.T) {
	headers := http.Header{}
	values := map[string]string{
		headerAccept: headerValueApplicationJSON,
		headerAnthropicDangerousDirectBrowserAccess: "true",
		headerXStainlessLang:                        "js",
		headerXStainlessRuntime:                     "node",
		headerXStainlessOS:                          "MacOS",
		headerXStainlessArch:                        "arm64",
		headerXStainlessPackageVersion:              "0.128.0",
		headerXStainlessRuntimeVersion:              "v26.12.1",
		headerXStainlessRetryCount:                  "0",
		headerXStainlessTimeout:                     "600",
		headerXClaudeCodeSessionID:                  "0f0e0d0c-0b0a-4009-8008-070605040302",
	}
	for name, value := range values {
		headers.Set(name, value)
	}
	headers.Set("X-Unknown-Header", "must-not-persist")
	dumped := dumpRequestHeaders(headers)
	for name, want := range values {
		if got := dumped.Get(name); got != want {
			t.Errorf("dumped %s = %q, want %q", name, got, want)
		}
	}
	if _, ok := dumped["X-Unknown-Header"]; ok {
		t.Error("unknown header was persisted")
	}
}
