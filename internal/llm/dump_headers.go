package llm

import "net/http"

// dumpRequestHeaders copies diagnostic HTTP headers before an asynchronous dump.
// Unknown headers are omitted because request overrides may contain credentials.
func dumpRequestHeaders(headers http.Header) http.Header {
	var result http.Header
	for name, values := range headers {
		name = http.CanonicalHeaderKey(name)
		switch name {
		case headerContentType, headerContentEncoding, headerAcceptEncoding,
			headerUserAgent, headerOpenAIBeta, "Accept", "Originator",
			"X-Session-Id", "Session-Id", "Thread-Id", "X-Client-Request-Id",
			http.CanonicalHeaderKey(headerSessionID),
			http.CanonicalHeaderKey(responsesClientMetadataInstallationID),
			http.CanonicalHeaderKey(responsesClientMetadataWindowID),
			http.CanonicalHeaderKey(responsesClientMetadataTurnMetadata),
			"Anthropic-Version", "Anthropic-Beta", "X-App":
			if result == nil {
				result = make(http.Header)
			}
			result[name] = append(result[name], values...)
		case "Authorization", "Proxy-Authorization", "Api-Key", "X-Api-Key", "X-Goog-Api-Key", "Cookie":
			if result == nil {
				result = make(http.Header)
			}
			result[name] = []string{"[redacted]"}
		}
	}
	return result
}
