package requesttrim

const (
	compactConfirmAgeTurns = 2
	compactErrorAgeTurns   = 3
	// Age is currentBatch minus producing batch, so a tool result is already
	// age 1 at the first request whose response can react to it. Success and
	// read-like thresholds therefore start at 2: the model must see a fresh
	// payload in full exactly once before any size-based summary may replace
	// it (age-1 reduction hid outputs the model had never seen, which it
	// reported to users as "output truncated").
	compactBashSuccessAgeTurns     = 2
	compactShellReadOnlyAgeTurns   = 3
	compactReadLikeAgeTurns        = 2
	compactStaleAgeTurns           = 3
	compactHighRiskProtectAgeTurns = 4
	compactDiffProtectAgeTurns     = 12
	compactBashSuccessBytes        = 3000
	compactReadLikeOutputBytes     = 3000
	reduceSnippetChars             = 500
	SummaryLineSnippetChars        = 180
	compactStaleOutputBytes        = 1500
	compactMinToolResultsPrune     = 6
	compactMinIncrementalTokens    = 2048
)
