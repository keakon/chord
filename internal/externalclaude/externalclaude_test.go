package externalclaude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/toolname"
)

func TestBuildArgsBase(t *testing.T) {
	args, err := BuildArgs(LaunchOptions{SessionID: "11111111-2222-4333-8444-555555555555"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{
		{"-p", "--output-format", "stream-json", "--verbose", "--input-format", "stream-json"},
		{"--session-id", "11111111-2222-4333-8444-555555555555"},
		{"--permission-prompt-tool", "stdio"}, {"--permission-mode", "default"},
		{"--safe-mode", "--restricted", "--setting-sources", "", "--strict-mcp-config"},
		{"--settings", managedSettings()},
		{"--mcp-config", `{"mcpServers":{}}`},
	} {
		if !containsSequence(args, want) {
			t.Fatalf("args %v missing %v", args, want)
		}
	}
}

func TestBuildArgsResumeAndPersistent(t *testing.T) {
	args, _ := BuildArgs(LaunchOptions{
		SessionID: "11111111-2222-4333-8444-555555555555",
		Resume:    true,
	})
	for _, want := range [][]string{
		{"--input-format", "stream-json"},
		{"--resume", "11111111-2222-4333-8444-555555555555"},
	} {
		if !containsSequence(args, want) {
			t.Fatalf("BuildArgs() = %v, missing %v", args, want)
		}
	}
	if containsSequence(args, []string{"--session-id"}) {
		t.Fatalf("resume args must not carry --session-id: %v", args)
	}
}

func TestBuildArgsDerivedAndUserOverride(t *testing.T) {
	args, err := BuildArgs(LaunchOptions{
		DisallowedTools: []string{"Edit", "Write"},
		LaunchArgs:      []string{"--model", "sample/test-model"},
	})
	for _, want := range [][]string{
		{"--disallowedTools", "Edit,Write"},
		{"--model", "sample/test-model"},
	} {
		if !containsSequence(args, want) {
			t.Fatalf("BuildArgs() = %v, missing %v", args, want)
		}
	}
	if !containsSequence(args, []string{"--model", "sample/test-model"}) {
		t.Fatalf("user launch args must be appended: %v", args)
	}
	if args[len(args)-2] != "--model" || args[len(args)-1] != "sample/test-model" {
		t.Fatalf("user launch args must come last: %v", args)
	}
	if err != nil {
		t.Fatal(err)
	}

	args, err = BuildArgs(LaunchOptions{
		DisallowedTools: []string{"Edit"},
		LaunchArgs:      []string{"--disallowed-tools=Bash"},
	})
	if err != nil || !containsSequence(args, []string{"--disallowedTools", "Bash,Edit"}) {
		t.Fatalf("derived prohibitions lost: args=%v err=%v", args, err)
	}
}

// An inline value must not smuggle another option into the argv: BuildArgs
// appends the value right after its flag, so a leading dash would sit in the
// argv where the CLI may read it as a flag of its own.
func TestBuildArgsRejectsInlineOptionValues(t *testing.T) {
	for _, launchArgs := range [][]string{
		{"--model=--dangerously-skip-permissions"},
		{"--effort=--safe-mode"},
		{"--max-turns=--add-dir"},
		{"--disallowedTools=-X"},
	} {
		if _, err := BuildArgs(LaunchOptions{LaunchArgs: launchArgs}); err == nil {
			t.Fatalf("BuildArgs(%v) accepted a value that starts with a dash", launchArgs)
		}
	}
	if _, err := BuildArgs(LaunchOptions{LaunchArgs: []string{"--model=sample/test-model", "--max-turns=3"}}); err != nil {
		t.Fatalf("valid inline values rejected: %v", err)
	}
}

func TestValidSessionID(t *testing.T) {
	for _, valid := range []string{
		"11111111-2222-4333-8444-555555555555",
		"AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE",
	} {
		if !ValidSessionID(valid) {
			t.Fatalf("ValidSessionID(%q) = false", valid)
		}
	}
	for _, invalid := range []string{
		"",
		"not-a-uuid",
		"11111111222243338444555555555555",
		"11111111-2222-4333-8444-55555555555g",
		"11111111-2222-4333-8444-5555555555555",
	} {
		if ValidSessionID(invalid) {
			t.Fatalf("ValidSessionID(%q) = true", invalid)
		}
	}
}

func TestNewSessionID(t *testing.T) {
	var random [16]byte
	for i := range random {
		random[i] = byte(i)
	}
	id := NewSessionID(random)
	if !ValidSessionID(id) {
		t.Fatalf("NewSessionID() = %q, not a valid UUID", id)
	}
	if id[14] != '4' {
		t.Fatalf("NewSessionID() = %q, want version 4", id)
	}
	if !strings.ContainsRune("89ab", rune(id[19])) {
		t.Fatalf("NewSessionID() = %q, want RFC 4122 variant", id)
	}
}

func TestDecodeRecordedShapes(t *testing.T) {
	initLine := `{"type":"system","subtype":"init","session_id":"efe26821-06f5-49b3-968e-34e763b3de3f","model":"claude-opus-5","permissionMode":"default","claude_code_version":"2.1.303","tools":["Read","Bash"]}`
	event, err := Decode([]byte(initLine))
	if err != nil {
		t.Fatalf("Decode(init) error = %v", err)
	}
	if event.Type != TypeSystem || event.Subtype != "init" || event.ClaudeCodeVersion != "2.1.303" {
		t.Fatalf("Decode(init) = %+v", event)
	}
	if len(event.Tools) != 2 || event.Tools[0] != "Read" {
		t.Fatalf("Decode(init) tools = %v", event.Tools)
	}

	assistantLine := `{"type":"assistant","message":{"id":"msg-1","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"toolu-1","name":"Read","input":{"file_path":"/tmp/a"}}]}}`
	event, err = Decode([]byte(assistantLine))
	if err != nil {
		t.Fatalf("Decode(assistant) error = %v", err)
	}
	if got := event.AssistantText(); got != "hi" {
		t.Fatalf("AssistantText() = %q, want hi", got)
	}
	uses := event.ToolUses()
	if len(uses) != 1 || uses[0].Name != "Read" || !strings.Contains(string(uses[0].Input), "file_path") {
		t.Fatalf("ToolUses() = %+v", uses)
	}

	// Observed on 2.1.303: an authentication failure reports subtype "success"
	// with is_error true, so classification must not trust the subtype.
	resultLine := `{"type":"result","subtype":"success","is_error":true,"result":"Not logged in","session_id":"efe26821-06f5-49b3-968e-34e763b3de3f","terminal_reason":"api_error","num_turns":1}`
	event, err = Decode([]byte(resultLine))
	if err != nil {
		t.Fatalf("Decode(result) error = %v", err)
	}
	if !event.Failed() || event.Succeeded() {
		t.Fatalf("result classification = failed:%v succeeded:%v, want failed", event.Failed(), event.Succeeded())
	}
	if event.TerminalReason != "api_error" {
		t.Fatalf("TerminalReason = %q", event.TerminalReason)
	}

	streamLine := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}}`
	event, err = Decode([]byte(streamLine))
	if err != nil {
		t.Fatalf("Decode(stream event) error = %v", err)
	}
	if event.Type != TypeStreamEvent || event.StreamEvent == nil ||
		event.StreamEvent.Type != "content_block_delta" ||
		event.StreamEvent.Delta == nil ||
		event.StreamEvent.Delta.Type != "text_delta" ||
		event.StreamEvent.Delta.Text != "partial" {
		t.Fatalf("Decode(stream event) = %+v", event)
	}

	// A UI system event reuses "event" for a string and arrives before the
	// initialize acknowledgment. It must decode instead of being rejected as
	// an invalid protocol line.
	invalidateLine := `{"type":"system","subtype":"ui_invalidate","event":"ui.render","uuid":"8a6f3c2e-1d4b-4e9a-9c7f-2b5d8e1a3f60","session_id":"efe26821-06f5-49b3-968e-34e763b3de3f"}`
	event, err = Decode([]byte(invalidateLine))
	if err != nil {
		t.Fatalf("Decode(ui_invalidate) error = %v", err)
	}
	if event.Type != TypeSystem || event.Subtype != "ui_invalidate" || event.StreamEvent != nil {
		t.Fatalf("Decode(ui_invalidate) = %+v", event)
	}

	if _, err := Decode([]byte(`{"type":"assistant","event":`)); err == nil {
		t.Fatal("Decode(truncated) error = nil, want an error")
	}
}

func TestDecodeRejectsDamagedInitToolCatalogue(t *testing.T) {
	for _, line := range []string{
		`{"type":"system","subtype":"init","tools":"Read"}`,
		`{"type":"system","subtype":"init","tools":[1,{"name":"Read"}]}`,
	} {
		if _, err := Decode([]byte(line)); err == nil {
			t.Fatalf("Decode(%s) error = nil, want a rejected tool catalogue", line)
		}
	}
	for _, line := range []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"system","subtype":"init","tools":null}`,
		`{"type":"system","subtype":"init","tools":[]}`,
	} {
		event, err := Decode([]byte(line))
		if err != nil {
			t.Fatalf("Decode(%s) error = %v", line, err)
		}
		if len(event.Tools) != 0 {
			t.Fatalf("Decode(%s) tools = %v, want none", line, event.Tools)
		}
	}
}

func TestDecodeToleratesNonActionableDrift(t *testing.T) {
	for _, test := range []struct {
		name    string
		line    string
		typ     string
		subtype string
	}{
		{
			name:    "system field drift",
			line:    `{"type":"system","subtype":"ui_toast","message":"dismiss","session_id":"efe26821-06f5-49b3-968e-34e763b3de3f"}`,
			typ:     TypeSystem,
			subtype: "ui_toast",
		},
		{
			name: "unknown frame",
			line: `{"type":"future_event","tools":"Read"}`,
			typ:  "future_event",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			event, err := Decode([]byte(test.line))
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if event.Type != test.typ || event.Subtype != test.subtype {
				t.Fatalf("Decode() = %+v", event)
			}
		})
	}
}

func TestDecodeRejectsActionableDrift(t *testing.T) {
	for _, line := range []string{
		`{"type":"assistant","message":"not an object"}`,
		`{"type":"user","message":"not an object"}`,
		`{"type":"result","usage":"not an object"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":"not an object"}}`,
		`{"type":"control_request","request":"not an object"}`,
		`{"type":"control_response","response":"not an object"}`,
	} {
		if _, err := Decode([]byte(line)); err == nil {
			t.Fatalf("Decode(%s) error = nil, want an error", line)
		}
	}
	for _, line := range []string{`[]`, `{}`, `"text"`} {
		if _, err := Decode([]byte(line)); err == nil {
			t.Fatalf("Decode(%s) error = nil, want an error", line)
		}
	}
}

func TestControlFrames(t *testing.T) {
	allow := AllowPermission("req-1", json.RawMessage(`{"path":"/tmp/a"}`))
	data, err := json.Marshal(allow)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	frame, err := func() (Frame, error) {
		var decoded Frame
		err := json.Unmarshal(data, &decoded)
		return decoded, err
	}()
	if err != nil {
		t.Fatalf("decode frame error = %v", err)
	}
	if frame.Type != TypeControlResponse || frame.Response == nil || frame.Response.RequestID != "req-1" {
		t.Fatalf("frame = %+v", frame)
	}
	var decision PermissionDecision
	if err := json.Unmarshal(frame.Response.Response, &decision); err != nil {
		t.Fatalf("Unmarshal(decision) error = %v", err)
	}
	if decision.Behavior != PermissionBehaviorAllow || !strings.Contains(string(decision.UpdatedInput), "/tmp/a") {
		t.Fatalf("decision = %+v", decision)
	}

	deny := DenyPermission("req-2", "denied")
	if deny.Response == nil || deny.Response.Subtype != ControlResponseSuccess {
		t.Fatalf("deny = %+v", deny)
	}
	var denied PermissionDecision
	if err := json.Unmarshal(deny.Response.Response, &denied); err != nil {
		t.Fatalf("Unmarshal(deny) error = %v", err)
	}
	if denied.Behavior != PermissionBehaviorDeny {
		t.Fatalf("deny decision = %+v", denied)
	}

	interrupt := InterruptRequest("req-3")
	if interrupt.Type != TypeControlRequest || interrupt.Request == nil || interrupt.Request.Subtype != ControlSubtypeInterrupt {
		t.Fatalf("interrupt = %+v", interrupt)
	}
}

func TestMapTool(t *testing.T) {
	tests := []struct {
		name       string
		claudeName string
		input      string
		wantName   string
		wantOK     bool
		wantFields map[string]string
		goneFields []string
	}{
		{
			name:       "read",
			input:      `{"file_path":"/tmp/a","offset":10}`,
			wantName:   toolname.Read,
			wantOK:     true,
			wantFields: map[string]string{"path": `"/tmp/a"`, "offset": "10"},
			goneFields: []string{"file_path"},
		},
		{
			name:       "edit",
			input:      `{"file_path":"/tmp/a","old_string":"a","new_string":"b"}`,
			wantName:   toolname.Edit,
			wantOK:     true,
			wantFields: map[string]string{"path": `"/tmp/a"`},
			goneFields: []string{"file_path"},
		},
		{
			name:       "bash",
			input:      `{"command":"go test ./...","timeout":60000}`,
			wantName:   toolname.Shell,
			wantOK:     true,
			wantFields: map[string]string{"command": `"go test ./..."`, "timeout_ms": "60000"},
			goneFields: []string{"timeout"},
		},
		{
			name:       "glob",
			input:      `{"pattern":"**/*.go","path":"/tmp"}`,
			wantName:   toolname.Glob,
			wantOK:     true,
			wantFields: map[string]string{"patterns": "[\"**/*.go\"]", "path": "\"/tmp\""},
			goneFields: []string{"pattern"},
		},
		{
			name:       "grep",
			input:      `{"pattern":"TODO","path":"/tmp","glob":"*.go"}`,
			wantName:   toolname.Grep,
			wantOK:     true,
			wantFields: map[string]string{"paths": "[\"/tmp\"]", "includes": "[\"*.go\"]", "pattern": "\"TODO\""},
			goneFields: []string{"path", "glob"},
		},
		{
			name:       "notebook_edit",
			claudeName: "NotebookEdit",
			input:      `{"notebook_path":"/tmp/a.ipynb","cell_id":"cell-1","new_source":"print(1)","cell_type":"code"}`,
			wantName:   toolname.NotebookEdit,
			wantOK:     true,
			wantFields: map[string]string{"path": `"/tmp/a.ipynb"`, "cell_id": `"cell-1"`, "new_source": `"print(1)"`},
			goneFields: []string{"notebook_path"},
		},
		{
			name:     "unknown",
			input:    `{"foo":"bar"}`,
			wantOK:   false,
			wantName: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claudeName := tc.claudeName
			if claudeName == "" {
				claudeName = mapToolName(tc.wantName)
			}
			mapping, input, ok := MapTool(claudeName, json.RawMessage(tc.input))
			if ok != tc.wantOK {
				t.Fatalf("MapTool() ok = %v, want %v", ok, tc.wantOK)
			}
			if mapping.ChordName != tc.wantName {
				t.Fatalf("MapTool() name = %q, want %q", mapping.ChordName, tc.wantName)
			}
			if !tc.wantOK {
				return
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(input, &fields); err != nil {
				t.Fatalf("Unmarshal(input) error = %v", err)
			}
			for field, want := range tc.wantFields {
				if got := string(fields[field]); got != want {
					t.Fatalf("field %q = %s, want %s", field, got, want)
				}
			}
			for _, field := range tc.goneFields {
				if _, ok := fields[field]; ok {
					t.Fatalf("field %q must be removed: %s", field, input)
				}
			}
		})
	}
}

// mapToolName maps a chord tool name back to the Claude name exercised by the
// table, keeping the test cases readable.
func mapToolName(chordName string) string {
	switch chordName {
	case toolname.Read:
		return "Read"
	case toolname.Edit:
		return "Edit"
	case toolname.Shell:
		return "Bash"
	case toolname.Glob:
		return "Glob"
	case toolname.Grep:
		return "Grep"
	default:
		return "Unmapped"
	}
}

func TestSessionStreamsEventsAndStderr(t *testing.T) {
	script := `printf '%s\n' '{"type":"system","subtype":"init","session_id":"s-1"}' '{"type":"result","subtype":"success","is_error":false,"result":"hi"}'; printf 'guard line\n' >&2`
	var stderr []string
	session, err := Start(ProcessConfig{
		Command: "/bin/sh",
		Args:    []string{"-c", script},
		OnStderr: func(line string) {
			stderr = append(stderr, line)
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	var events []Event
	for event := range session.Events() {
		events = append(events, event)
	}
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if len(events) != 2 || events[0].Type != TypeSystem || !events[1].Succeeded() {
		t.Fatalf("events = %+v", events)
	}
	if len(stderr) != 1 || !strings.Contains(stderr[0], "guard line") {
		t.Fatalf("stderr = %v", stderr)
	}
}

func TestBuildArgsNotifyMCP(t *testing.T) {
	args, err := BuildArgs(LaunchOptions{Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSequence(args, []string{"--safe-mode"}) || containsSequence(args, []string{"--bare"}) {
		t.Fatalf("args %v must keep --safe-mode; --bare disables OAuth and keychain auth", args)
	}
	if !containsSequence(args, []string{FlagAllowedTools, MCPNotifyToolName}) {
		t.Fatalf("args %v missing preapproved notify MCP tool", args)
	}
	var raw string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == FlagMCPConfig {
			raw = args[i+1]
			break
		}
	}
	if raw == "" {
		t.Fatalf("args %v missing %s", args, FlagMCPConfig)
	}
	var config struct {
		MCPServers map[string]struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatalf("decode mcp config %q: %v", raw, err)
	}
	if len(config.MCPServers) != 1 {
		t.Fatalf("mcp servers = %+v, want exactly the private notify server", config.MCPServers)
	}
	server, ok := config.MCPServers[MCPNotifyServerName]
	if !ok {
		t.Fatalf("mcp servers = %+v, missing %q", config.MCPServers, MCPNotifyServerName)
	}
	if server.Type != "sdk" || server.Name != MCPNotifyServerName {
		t.Fatalf("notify server = %+v, want an in-process SDK server", server)
	}
	if server.Command != "" {
		t.Fatalf("notify server command = %q, want no child process", server.Command)
	}
}

func TestBuildArgsWithoutNotifyKeepsMCPClosed(t *testing.T) {
	args, err := BuildArgs(LaunchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if containsSequence(args, []string{FlagAllowedTools}) {
		t.Fatalf("args %v unexpectedly preapprove a tool", args)
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] != FlagMCPConfig {
			continue
		}
		var config struct {
			MCPServers map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(args[i+1]), &config); err != nil {
			t.Fatalf("decode mcp config %q: %v", args[i+1], err)
		}
		if len(config.MCPServers) != 0 {
			t.Fatalf("mcp servers = %+v, want none", config.MCPServers)
		}
		return
	}
	t.Fatalf("args %v missing %s", args, FlagMCPConfig)
}

func TestNotifyMCPToolMapping(t *testing.T) {
	if !ManagedTool(MCPNotifyToolName) {
		t.Fatalf("ManagedTool(%q) = false", MCPNotifyToolName)
	}
	for _, name := range ManagedToolNames() {
		if name == MCPNotifyToolName {
			t.Fatalf("built-in tool list must not include the private MCP tool: %v", ManagedToolNames())
		}
	}
	input := json.RawMessage(`{"message":"status"}`)
	mapping, chordInput, ok := MapTool(MCPNotifyToolName, input)
	if !ok || mapping.ChordName != toolname.Notify {
		t.Fatalf("MapTool(%q) = %+v, ok=%v; want %q", MCPNotifyToolName, mapping, ok, toolname.Notify)
	}
	if string(chordInput) != string(input) {
		t.Fatalf("MapTool input = %s, want unchanged %s", chordInput, input)
	}
}

func containsSequence(haystack, needle []string) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		matched := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
