package llm

// LLM streaming decoders intentionally use sonic's default fast configuration.
// Provider and OpenAI-compatible gateway payloads are high-volume compatibility
// inputs rather than strict local protocol boundaries; accepting sonic's default
// string semantics keeps those hot paths fast and tolerant. Use a dedicated
// config, like MCP does, for boundaries that must reject malformed JSON strings
// or for decoded strings that should not retain the source buffer.
