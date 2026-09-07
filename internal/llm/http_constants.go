package llm

import "github.com/keakon/chord/internal/config"

const (
	headerAcceptEncoding       = "Accept-Encoding"
	headerCodexTurnState       = "x-codex-turn-state"
	headerCodexBetaFeatures    = "x-codex-beta-features"
	headerValueRemoteCompactV2 = "remote_compaction_v2"
	headerContentEncoding      = "Content-Encoding"
	headerContentType          = "Content-Type"
	headerOpenAIBeta           = "OpenAI-Beta"
	headerSessionID            = "session_id"
	headerUserAgent            = "User-Agent"
	headerValueApplicationJSON = "application/json"

	// The response direction is only ever gzip (servers do not compress LLM
	// streams, and zstd responses are not requested or decoded), so the
	// Accept-Encoding/Content-Encoding value shares the configured gzip
	// request-compression encoding.
	headerValueGzip = config.RequestCompressionGzip

	maxHTTPErrorBodyBytes = 4096
)
