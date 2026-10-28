package llm

import "github.com/keakon/chord/internal/config"

const (
	headerAccept               = "Accept"
	headerAcceptEncoding       = "Accept-Encoding"
	headerCodexTurnState       = "x-codex-turn-state"
	headerContentEncoding      = "Content-Encoding"
	headerContentType          = "Content-Type"
	headerOpenAIBeta           = "OpenAI-Beta"
	headerOriginator           = "Originator"
	headerSessionID            = "Session-Id"
	headerThreadID             = "Thread-Id"
	headerClientRequestID      = "X-Client-Request-Id"
	headerXSessionID           = "X-Session-Id"
	headerUserAgent            = "User-Agent"
	headerValueApplicationJSON = "application/json"

	// The response direction is only ever gzip (servers do not compress LLM
	// streams, and zstd responses are not requested or decoded), so the
	// Accept-Encoding/Content-Encoding value shares the configured gzip
	// request-compression encoding.
	headerValueGzip = config.RequestCompressionGzip

	maxHTTPErrorBodyBytes = 4096
)
