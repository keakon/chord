package recovery

type PendingCompactionResume struct {
	Kind               string `json:"kind,omitempty"`
	Mode               string `json:"mode,omitempty"`
	UserIntent         string `json:"user_intent,omitempty"`
	RecoveryPrompt     string `json:"recovery_prompt,omitempty"`
	OversizeSuspended  bool   `json:"oversize_suspended,omitempty"`
	OversizeRetryCount int    `json:"oversize_retry_count,omitempty"`
}
