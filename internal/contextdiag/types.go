package contextdiag

import "time"

type Binding struct {
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id"` // Canonical durable task, distinct from runtime instance.
	Epoch     uint64 `json:"epoch"`   // Runtime binding epoch.
}
type RequestID struct {
	Binding Binding `json:"binding"`
	Seq     uint64  `json:"seq"`     // Store sequence, independent of the turn ID.
	Attempt uint64  `json:"attempt"` // Incremented for every transport submission.
}
type Estimate struct {
	Tokens       int64 `json:"tokens"` // Only the estimated portion.
	TextBytes    int64 `json:"text_bytes"`
	UnknownItems int   `json:"unknown_items"`
}
type Source struct {
	Kind     string   `json:"kind"`  // tool / skill / instructions / conversation / runtime
	Label    string   `json:"label"` // Display name; no arguments or message bodies.
	Count    int      `json:"count"`
	Estimate Estimate `json:"estimate"`
}
type Category struct {
	Kind     string   `json:"kind"`
	Estimate Estimate `json:"estimate"`
	Sources  []Source `json:"sources"` // At most five sources.
	Other    Estimate `json:"other"`
}
type Usage struct {
	State      string `json:"state"` // missing / reported / partial
	FullInput  int64  `json:"full_input"`
	Output     int64  `json:"output"`
	CacheRead  int64  `json:"cache_read"`
	CacheWrite int64  `json:"cache_write"` // Includes the 1h subset; do not add it twice.
}
type Rule struct {
	Tool          string `json:"tool"`
	Reason        string `json:"reason"`
	Count         int    `json:"count"`
	SavedEstimate int64  `json:"saved_estimate"` // Per-rule estimate; not additive net savings.
}
type Reduction struct {
	State        string   `json:"state"` // observed / disabled / unavailable
	Before       int64    `json:"before"`
	After        int64    `json:"after"`
	Estimator    string   `json:"estimator"` // reduction_frozen_ratio
	Ratio        float64  `json:"ratio"`
	Protected    bool     `json:"protected"`
	ReusedStable bool     `json:"reused_stable"`
	Reasons      []string `json:"reasons"` // Stable codes, translated by the UI.
	Rules        []Rule   `json:"rules"`   // At most sixteen rules plus Other.
}
type Cache struct {
	BaselineState  string   `json:"baseline_state"`  // first / comparable / domain_changed / unavailable
	LocalChanges   []string `json:"local_changes"`   // system / tools / messages; all applicable changes.
	PrefixState    string   `json:"prefix_state"`    // same / append / truncate / rewrite / unknown
	ExpectedPrefix int64    `json:"expected_prefix"` // Estimated local common prefix, not a hit guarantee.
	ExpectedUsable bool     `json:"expected_usable"` // False for system/tools changes or incomplete projections.
	Explanation    string   `json:"explanation"`     // read_observed / write_only / no_report / unknown
}
type Snapshot struct {
	ID                RequestID  `json:"id"`
	HistoryGeneration uint64     `json:"history_generation"`
	ModelRef          string     `json:"model_ref"`
	WireFamily        string     `json:"wire_family"`
	Transport         string     `json:"transport"` // http / websocket / custom
	State             string     `json:"state"`     // dispatched / completed / failed / cancelled / superseded
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        time.Time  `json:"finished_at"`
	Revision          uint64     `json:"revision"`
	EstimateScope     string     `json:"estimate_scope"` // provider_input
	Estimator         string     `json:"estimator"`      // utf8_bytes_div3
	Completeness      string     `json:"completeness"`   // estimated / partial / unavailable
	Categories        []Category `json:"categories"`
	Limitations       []string   `json:"limitations"` // Stable codes; no raw errors or URLs.
	Reduction         Reduction  `json:"reduction"`
	Usage             Usage      `json:"usage"`
	Cache             Cache      `json:"cache"`
	// WindowLimit is the usable input budget the request was dispatched
	// against. Shares of the window must divide by this value: reading a
	// historical snapshot against the current model's budget misstates how
	// full the window was whenever the model changed between requests.
	WindowLimit int64 `json:"window_limit"`
}
