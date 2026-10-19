package sessionimport

import (
	"encoding/json"
	"strings"
)

// Some Codex versions recorded a tool's result payload itself — the output text
// plus its metadata — as a JSON string instead of the output text it wraps:
//
//	{"output":"FAIL\n","metadata":{"exit_code":1,"duration_seconds":0.1}}
//
// That is FunctionCallOutputPayload serialized one level too deep, and the
// importer used to keep it verbatim, so a command's output arrived with every
// newline escaped and an apply_patch result read as JSON instead of the summary
// the source agent saw. The exit status inside it was not read either, so a
// failed shell call came back as a success.
type codexResultPayload struct {
	Output   string
	ExitCode int
}

// codexRecordsResultPayload reports which tools record that payload rather than
// their own output. A tool that returns file content is not among them, so
// reading a file that happens to hold such a payload keeps the file's bytes
// instead of having them decoded.
func codexRecordsResultPayload(name string) bool {
	return codexHasShellEnvelope(name) || strings.TrimSpace(name) == "apply_patch"
}

// parseCodexResultPayload decodes that payload. The whole text has to be the one
// object, with a string "output" and an integer "metadata.exit_code", so a
// command whose own output is JSON is only read as a payload when it names
// exactly those fields; anything else reports false and stays verbatim.
func parseCodexResultPayload(text string) (codexResultPayload, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return codexResultPayload{}, false
	}
	var payload struct {
		Output   *string `json:"output"`
		Metadata *struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return codexResultPayload{}, false
	}
	if payload.Output == nil || payload.Metadata == nil || payload.Metadata.ExitCode == nil {
		return codexResultPayload{}, false
	}
	return codexResultPayload{Output: *payload.Output, ExitCode: *payload.Metadata.ExitCode}, true
}
