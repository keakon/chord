package llm

// LLM streaming decoders intentionally use sonic's default fast configuration.
// Provider and OpenAI-compatible gateway payloads are high-volume compatibility
// inputs rather than strict local protocol boundaries; accepting sonic's default
// string semantics keeps those hot paths fast and tolerant. Use a dedicated
// config, like MCP does, for boundaries that must reject malformed JSON strings
// or for decoded strings that should not retain the source buffer.
//
// Outgoing request bodies stay on encoding/json deliberately: on Go 1.26
// (jsonv2-backed stdlib) marshaling a ~1.7MB conversation measured faster
// than sonic ConfigStd (1.57ms/1.75MB/6 allocs vs 1.85ms/9.6MB/30 allocs on
// an M1 Pro). Sonic's advantage is on the decode side; do not switch the
// marshal sites without re-measuring.
