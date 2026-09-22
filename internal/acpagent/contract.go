package acpagent

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	acp "github.com/coder/acp-go-sdk"
	"github.com/keakon/golog/log"
)

// This file holds what the mux frontend and the session child have to agree on.
// Both sides run the same binary and the same SDK, so the capability set, the
// initialize answer, the session-root check and the session id shape live in
// one place instead of being mirrored by hand.

// AgentCapabilities is the capability set Chord advertises to ACP clients.
func AgentCapabilities() acp.AgentCapabilities {
	return acp.AgentCapabilities{
		PromptCapabilities: acp.PromptCapabilities{Image: true},
		SessionCapabilities: acp.SessionCapabilities{
			Close: new(acp.SessionCloseCapabilities),
		},
	}
}

// InitializeResponse builds the agent's answer to initialize. version is the
// Chord version reported as the agent version.
func InitializeResponse(version string) acp.InitializeResponse {
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentInfo:         &acp.Implementation{Name: "chord", Version: version},
		AgentCapabilities: AgentCapabilities(),
	}
}

// LogInitialize records what the client negotiated. The frontend answers
// initialize itself and passes the same request to every child, so the two sides
// have to agree on what is logged about it.
func LogInitialize(req acp.InitializeRequest) {
	if req.ProtocolVersion != acp.ProtocolVersionNumber {
		log.Warnf("acp client protocol version differs client=%v agent=%v", req.ProtocolVersion, acp.ProtocolVersionNumber)
	}
	if req.ClientInfo != nil {
		log.Infof("acp client connected name=%v version=%v", req.ClientInfo.Name, req.ClientInfo.Version)
	}
}

// SessionCwd validates the working directory a session/new asks for and returns
// it trimmed. The frontend checks it before spawning so a bad path never costs
// a child process, and the child checks it again because it is the process that
// roots the session there.
func SessionCwd(raw string) (string, *acp.RequestError) {
	cwd := strings.TrimSpace(raw)
	if cwd == "" {
		return "", acp.NewInvalidParams(map[string]any{"error": "cwd is required"})
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return "", acp.NewInvalidParams(map[string]any{"cwd": cwd, "error": "cwd is not a directory"})
	}
	return cwd, nil
}

// sessionSeq numbers the ACP sessions a process mints; it only keeps the minted
// ids unique within one process.
var sessionSeq atomic.Uint64

// NewSessionID mints the ACP session id. It stays opaque and process-scoped on
// purpose: the client only echoes it back, and it must survive in-process
// session switches that move the Chord session directory under it. The mux
// frontend mints the id its client sees with it and passes it to the session
// child as --session-child so the child names its log file after it; the two
// sides use independent protocol ids that the mux rewrites per request.
func NewSessionID() acp.SessionId {
	return acp.SessionId(fmt.Sprintf("chord-%d-%d", os.Getpid(), sessionSeq.Add(1)))
}
