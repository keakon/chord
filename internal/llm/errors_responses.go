package llm

const (
	invalidResponsesRequestCode = "invalid_responses_request"
	invalidCodexRequestMessage  = "invalid codex request"
)

// hasResponsesClientContractSignal identifies a relay's client-contract refusal.
// Changing replayed reasoning or rotating credentials cannot repair this error.
func hasResponsesClientContractSignal(apiErr *APIError) bool {
	return apiErrorSignalContains(apiErr, invalidResponsesRequestCode) ||
		apiErrMessageContainsAny(apiErr, invalidCodexRequestMessage)
}
