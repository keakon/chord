package sessionimport

import (
	"strconv"
	"strings"
)

// Codex's shell tools answer with a header block, a bare "Output:" line, and the
// command's own output. `exec_command` and `write_stdin` (unified exec) use this
// envelope, and the legacy `shell` tool used a subset of it:
//
//	Command: <the command, echoed across as many lines as it spans>
//	Chunk ID: <id>
//	Wall time: <seconds> seconds
//	Process exited with code <status>
//	Process running with session ID <id>
//	Original token count: <n>
//	Output:
//	<body>
//
// Only "Wall time:" and "Output:" are always present; the rest depend on the
// version that wrote the rollout and on whether the process had finished. The
// order is the one codex-rs writes (`response_header` in
// core/src/tools/context.rs), and "Process exited" and "Process running" are
// alternatives.
const (
	codexShellCommandPrefix = "Command: "
	codexShellChunkPrefix   = "Chunk ID: "
	codexShellWallPrefix    = "Wall time: "
	codexShellExitPrefix    = "Process exited with code "
	codexShellRunningPrefix = "Process running with session ID "
	codexShellTokensPrefix  = "Original token count: "
	codexShellBodyMarker    = "Output:"
)

// codexShellResult is a decoded shell result: the exit status the envelope
// recorded and the output body with the envelope removed.
type codexShellResult struct {
	// ExitCode is the recorded status, or 0 when the header recorded none —
	// the older format that wrote only "Wall time:" finished the process
	// without reporting a status, and a caller reads 0 as no evidence of
	// failure.
	ExitCode int
	// Settled reports whether the process had exited. A header with no
	// "Process running" line describes a finished process, so its body is the
	// final output; a yielded one keeps the session id its continuation names.
	Settled bool
	Output  string
}

// codexHasShellEnvelope reports whether a source tool answers with that
// envelope. The check is keyed on the source name because a tool that returns
// file content can contain the same header lines by coincidence, and decoding
// those would rewrite the file's text.
func codexHasShellEnvelope(name string) bool {
	switch strings.TrimSpace(name) {
	case "exec_command", "write_stdin", "shell":
		return true
	default:
		return false
	}
}

// parseCodexShellResult splits the envelope. It reports false for anything that
// is not that envelope, in which case the caller keeps the output verbatim
// rather than dropping or truncating it.
func parseCodexShellResult(output string) (codexShellResult, bool) {
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		if line != codexShellBodyMarker {
			continue
		}
		res, ok := parseCodexShellHeader(lines[:i])
		if !ok {
			continue
		}
		res.Output = strings.Join(lines[i+1:], "\n")
		return res, true
	}
	return codexShellResult{}, false
}

// parseCodexShellHeader decodes the lines above "Output:". It reads them from
// the bottom up so the echoed command, which spans as many lines as the command
// itself, needs no special handling: everything above the fields is that echo.
func parseCodexShellHeader(header []string) (codexShellResult, bool) {
	var res codexShellResult
	i := len(header) - 1
	if i >= 0 && strings.HasPrefix(header[i], codexShellTokensPrefix) {
		if _, err := strconv.Atoi(strings.TrimPrefix(header[i], codexShellTokensPrefix)); err != nil {
			return codexShellResult{}, false
		}
		i--
	}
	switch {
	case i >= 0 && strings.HasPrefix(header[i], codexShellExitPrefix):
		code, err := strconv.Atoi(strings.TrimPrefix(header[i], codexShellExitPrefix))
		if err != nil {
			return codexShellResult{}, false
		}
		res.ExitCode, res.Settled = code, true
		i--
	case i >= 0 && strings.HasPrefix(header[i], codexShellRunningPrefix):
		// Still running: there is no outcome, and the session id names the
		// continuation the source recorded.
		res.Settled = false
		i--
	default:
		// No status line at all is the older envelope, which only a finished
		// process wrote.
		res.Settled = true
	}
	// "Wall time: 1.2500 seconds" is the one line every version writes, so it
	// is what tells this envelope apart from the `exec` one, whose header reads
	// "Wall time 1.2 seconds" without the colon.
	if i < 0 || !strings.HasPrefix(header[i], codexShellWallPrefix) {
		return codexShellResult{}, false
	}
	seconds, ok := strings.CutSuffix(strings.TrimPrefix(header[i], codexShellWallPrefix), " seconds")
	if !ok {
		return codexShellResult{}, false
	}
	if _, err := strconv.ParseFloat(seconds, 64); err != nil {
		return codexShellResult{}, false
	}
	i--
	if i >= 0 && strings.HasPrefix(header[i], codexShellChunkPrefix) {
		i--
	}
	// What is left is the echoed command, which opens with its own label.
	if i >= 0 && !strings.HasPrefix(header[0], codexShellCommandPrefix) {
		return codexShellResult{}, false
	}
	return res, true
}
