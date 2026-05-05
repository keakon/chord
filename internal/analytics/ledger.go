package analytics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
)

const maxUsageSummaryFirstUserPreview = 80

// UsageEvent is one immutable usage record in usage.jsonl.
type UsageEvent struct {
	Version          int               `json:"version"`
	EventID          string            `json:"event_id"`
	SessionID        string            `json:"session_id"`
	ProjectID        string            `json:"project_id,omitempty"`
	ProjectPath      string            `json:"project_path,omitempty"`
	OccurredAt       time.Time         `json:"occurred_at"`
	Timezone         string            `json:"timezone,omitempty"`
	LocalDate        string            `json:"local_date,omitempty"`
	AgentID          string            `json:"agent_id"`
	AgentKind        string            `json:"agent_kind,omitempty"`
	AgentName        string            `json:"agent_name,omitempty"`
	Purpose          string            `json:"purpose,omitempty"`
	TurnID           uint64            `json:"turn_id,omitempty"`
	SelectedModelRef string            `json:"selected_model_ref,omitempty"`
	RunningModelRef  string            `json:"running_model_ref,omitempty"`
	Provider         string            `json:"provider,omitempty"`
	ModelID          string            `json:"model_id,omitempty"`
	UsageRaw         UsageSnapshot     `json:"usage_raw"`
	BillingUsage     BillingUsage      `json:"billing_usage"`
	Cost             UsageCost         `json:"cost"`
	PricingSnapshot  PricingSnapshot   `json:"pricing_snapshot"`
	Diagnostic       map[string]string `json:"diagnostic,omitempty"`
}

// UsageAggregate is a rollup used in usage-summary.json.
type UsageAggregate struct {
	LLMCalls            int64   `json:"llm_calls"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens    int64   `json:"cache_write_tokens,omitempty"`
	ReasoningTokens     int64   `json:"reasoning_tokens,omitempty"`
	ProviderTotalTokens int64   `json:"provider_total_tokens,omitempty"`
	BillingTotalTokens  int64   `json:"billing_total_tokens,omitempty"`
	TotalCost           float64 `json:"total_cost"`
}

// SessionUsageSummary is the materialized session-level view of usage.jsonl.
type SessionUsageSummary struct {
	Version                                     int                                   `json:"version"`
	SessionID                                   string                                `json:"session_id"`
	ProjectID                                   string                                `json:"project_id,omitempty"`
	ProjectPath                                 string                                `json:"project_path,omitempty"`
	CreatedAt                                   time.Time                             `json:"created_at,omitempty"`
	LastUpdatedAt                               time.Time                             `json:"last_updated_at,omitempty"`
	LastEventID                                 string                                `json:"last_event_id,omitempty"`
	EventCount                                  int64                                 `json:"event_count,omitempty"`
	Timezone                                    string                                `json:"timezone,omitempty"`
	FirstUserMessage                            string                                `json:"first_user_message,omitempty"`
	FirstUserMessageIsCompactionSummary         bool                                  `json:"first_user_message_is_compaction_summary,omitempty"`
	OriginalFirstUserMessage                    string                                `json:"original_first_user_message,omitempty"`
	OriginalFirstUserMessageIsCompactionSummary bool                                  `json:"original_first_user_message_is_compaction_summary,omitempty"`
	Status                                      string                                `json:"status,omitempty"`
	UsageTotal                                  UsageAggregate                        `json:"usage_total"`
	ByProvider                                  map[string]*UsageAggregate            `json:"by_provider,omitempty"`
	ByModelRef                                  map[string]*UsageAggregate            `json:"by_model_ref,omitempty"`
	ByAgent                                     map[string]*UsageAggregate            `json:"by_agent,omitempty"`
	ByPurpose                                   map[string]*UsageAggregate            `json:"by_purpose,omitempty"`
	ByDate                                      map[string]*UsageAggregate            `json:"by_date,omitempty"`
	ByDateModelRef                              map[string]map[string]*UsageAggregate `json:"by_date_model_ref,omitempty"`
	ByDateAgent                                 map[string]map[string]*UsageAggregate `json:"by_date_agent,omitempty"`
}

// UsageLedger manages append-only usage.jsonl and usage-summary.json.
type UsageLedger struct {
	mu                       sync.RWMutex
	sessionDir               string
	projectPath              string
	projectID                string
	firstUserMessage         string
	originalFirstUserMessage string // never overwritten after initial set
	eventSeq                 uint64
	summaryLoaded            bool
	summary                  *SessionUsageSummary
}

// NewUsageLedger creates a ledger bound to one session directory.
func NewUsageLedger(sessionDir, projectPath string) *UsageLedger {
	return &UsageLedger{
		sessionDir:  sessionDir,
		projectPath: strings.TrimSpace(projectPath),
		projectID:   ProjectIDForPath(projectPath),
	}
}

// LoadSessionUsageSummary loads the fresh summary for a session, rebuilding it
// from usage.jsonl when the cached summary is stale or missing.
func LoadSessionUsageSummary(sessionDir string) (*SessionUsageSummary, error) {
	return NewUsageLedger(sessionDir, "").Summary()
}

// SetFirstUserMessage records the first user message preview for future summary writes.
// It also sets OriginalFirstUserMessage, which is never overwritten by compaction.
func (l *UsageLedger) SetFirstUserMessage(content string) error {
	preview := usageFirstUserPreview(content)
	if preview == "" {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.firstUserMessage != "" {
		return nil
	}
	l.firstUserMessage = preview
	l.originalFirstUserMessage = preview
	if l.summaryLoaded && l.summary != nil {
		if l.summary.FirstUserMessage == "" {
			l.summary.FirstUserMessage = preview
		}
		if l.summary.OriginalFirstUserMessage == "" {
			l.summary.OriginalFirstUserMessage = preview
		}
		return l.writeSummaryLocked(l.summary)
	}
	return nil
}

// RewriteFirstUserMessage replaces the cached first-user preview and updates
// usage-summary.json so session lists stay in sync after session rewrites.
func (l *UsageLedger) RewriteFirstUserMessage(content string) error {
	return l.rewriteFirstUserMessage(content, "", false)
}

// RewriteFirstUserMessageWithOriginalForCompaction behaves like
// RewriteFirstUserMessage but marks the rewritten first-user preview as a
// synthetic compaction summary and allows the caller to seed
// OriginalFirstUserMessage when neither the ledger nor the on-disk summary has
// it set yet. This is used by the compaction rewrite path where the original
// first user message must be captured before main.jsonl is replaced with the
// compaction summary; otherwise the fallback would read the summary itself.
func (l *UsageLedger) RewriteFirstUserMessageWithOriginalForCompaction(content, originalHint string) error {
	return l.rewriteFirstUserMessage(content, originalHint, true)
}

func (l *UsageLedger) rewriteFirstUserMessage(content, originalHint string, firstUserIsCompactionSummary bool) error {
	preview := usageFirstUserPreview(content)
	originalPreview := usageFirstUserPreview(originalHint)

	l.mu.Lock()
	defer l.mu.Unlock()

	summary, err := l.ensureSummaryLocked()
	if err != nil {
		return err
	}
	if l.originalFirstUserMessage == "" && summary != nil && !summary.OriginalFirstUserMessageIsCompactionSummary {
		l.originalFirstUserMessage = summary.OriginalFirstUserMessage
	}
	if l.originalFirstUserMessage == "" && originalPreview != "" {
		l.originalFirstUserMessage = originalPreview
	}
	if l.originalFirstUserMessage == "" {
		l.originalFirstUserMessage = l.firstUserMessageLocked()
	}
	l.firstUserMessage = preview
	summary.FirstUserMessage = preview
	summary.FirstUserMessageIsCompactionSummary = firstUserIsCompactionSummary
	if summary.OriginalFirstUserMessage == "" || summary.OriginalFirstUserMessageIsCompactionSummary {
		summary.OriginalFirstUserMessage = l.originalFirstUserMessage
		summary.OriginalFirstUserMessageIsCompactionSummary = false
	}
	return l.writeSummaryLocked(summary)
}

// Summary returns the cached session summary, rebuilding it from the ledger when needed.
func (l *UsageLedger) Summary() (*SessionUsageSummary, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	summary, err := l.ensureSummaryLocked()
	if err != nil {
		return nil, err
	}
	return cloneUsageSummary(summary), nil
}

// BuildSessionStats rebuilds runtime SessionStats from usage.jsonl.
func (l *UsageLedger) BuildSessionStats() (SessionStats, int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	stats := SessionStats{
		ByModel: make(map[string]*ModelStats),
		ByAgent: make(map[string]*AgentStats),
	}
	var eventCount int64
	err := scanUsageEvents(l.usagePath(), func(evt UsageEvent) {
		eventCount++
		raw := evt.UsageRaw
		stats.InputTokens += raw.InputTokens
		stats.OutputTokens += raw.OutputTokens
		stats.CacheReadTokens += raw.CacheReadTokens
		stats.CacheWriteTokens += raw.CacheWriteTokens
		stats.ReasoningTokens += raw.ReasoningTokens
		stats.LLMCalls++
		stats.EstimatedCost += evt.Cost.TotalCost

		modelKey := usageModelKey(evt)
		ms, ok := stats.ByModel[modelKey]
		if !ok {
			ms = &ModelStats{}
			stats.ByModel[modelKey] = ms
		}
		ms.Calls++
		ms.InputTokens += raw.InputTokens
		ms.OutputTokens += raw.OutputTokens
		ms.CacheReadTokens += raw.CacheReadTokens
		ms.CacheWriteTokens += raw.CacheWriteTokens
		ms.ReasoningTokens += raw.ReasoningTokens
		ms.EstimatedCost += evt.Cost.TotalCost

		agentID := normalizeAgentID(evt.AgentID)
		as, ok := stats.ByAgent[agentID]
		if !ok {
			as = &AgentStats{ByModel: make(map[string]*ModelStats)}
			stats.ByAgent[agentID] = as
		}
		as.InputTokens += raw.InputTokens
		as.OutputTokens += raw.OutputTokens
		as.CacheReadTokens += raw.CacheReadTokens
		as.CacheWriteTokens += raw.CacheWriteTokens
		as.ReasoningTokens += raw.ReasoningTokens
		as.LLMCalls++
		as.EstimatedCost += evt.Cost.TotalCost

		ams, ok := as.ByModel[modelKey]
		if !ok {
			ams = &ModelStats{}
			as.ByModel[modelKey] = ams
		}
		ams.Calls++
		ams.InputTokens += raw.InputTokens
		ams.OutputTokens += raw.OutputTokens
		ams.CacheReadTokens += raw.CacheReadTokens
		ams.CacheWriteTokens += raw.CacheWriteTokens
		ams.ReasoningTokens += raw.ReasoningTokens
		ams.EstimatedCost += evt.Cost.TotalCost
	})
	return stats, eventCount, err
}

// AppendEvent appends one usage event and refreshes usage-summary.json.
func (l *UsageLedger) AppendEvent(event UsageEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	event = l.prepareEventLocked(event)

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal usage event: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(l.sessionDir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	summary, summaryErr := l.ensureSummaryLocked()

	f, err := os.OpenFile(l.usagePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open usage ledger: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("append usage ledger: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close usage ledger: %w", err)
	}

	if summaryErr != nil {
		l.summaryLoaded = false
		l.summary = nil
		rebuilt, err := l.rebuildSummaryLocked()
		if err != nil {
			return fmt.Errorf("rebuild usage summary: %w", err)
		}
		l.summary = rebuilt
		l.summaryLoaded = true
		return nil
	}

	applyUsageEvent(summary, event)
	if err := l.writeSummaryLocked(summary); err != nil {
		l.summaryLoaded = false
		l.summary = nil
		return fmt.Errorf("write usage summary: %w", err)
	}
	l.summary = summary
	l.summaryLoaded = true
	return nil
}

func (l *UsageLedger) prepareEventLocked(event UsageEvent) UsageEvent {
	if event.Version == 0 {
		event.Version = usageEventVersion
	}
	if strings.TrimSpace(event.EventID) == "" {
		l.eventSeq++
		event.EventID = fmt.Sprintf("%d-%06d", time.Now().UnixNano(), l.eventSeq)
	}
	if strings.TrimSpace(event.SessionID) == "" {
		event.SessionID = filepath.Base(l.sessionDir)
	}
	if strings.TrimSpace(event.ProjectPath) == "" {
		event.ProjectPath = l.projectPath
	}
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = ProjectIDForPath(event.ProjectPath)
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	if event.Timezone == "" {
		event.Timezone = event.OccurredAt.Location().String()
	}
	if event.LocalDate == "" {
		event.LocalDate = event.OccurredAt.In(event.OccurredAt.Location()).Format("2006-01-02")
	}
	if event.RunningModelRef == "" {
		event.RunningModelRef = event.SelectedModelRef
	}
	if event.Provider == "" || event.ModelID == "" {
		event.Provider, event.ModelID = SplitModelRef(event.RunningModelRef)
	}
	if event.AgentID == "" {
		event.AgentID = "main"
	}
	if event.BillingUsage.BillingTotalTokens == 0 &&
		event.BillingUsage.InputTokens == 0 &&
		event.BillingUsage.OutputTokens == 0 &&
		event.BillingUsage.CacheReadTokens == 0 &&
		event.BillingUsage.CacheWriteTokens == 0 {
		event.BillingUsage = NormalizeBillingUsage(event.UsageRaw)
	}
	if event.Cost.Currency == "" {
		event.Cost.Currency = "USD"
	}
	if event.PricingSnapshot.Source == "" {
		event.PricingSnapshot.Source = "config"
	}
	if event.ProjectPath != "" && l.projectPath == "" {
		l.projectPath = event.ProjectPath
	}
	if event.ProjectID != "" && l.projectID == "" {
		l.projectID = event.ProjectID
	}
	return event
}

func (l *UsageLedger) ensureSummaryLocked() (*SessionUsageSummary, error) {
	if l.summaryLoaded && l.summary != nil {
		return l.summary, nil
	}

	summary, _ := readUsageSummaryFile(l.summaryPath())
	lastEvent, hasLastEvent, err := readLastCompleteUsageEvent(l.usagePath())
	if err != nil {
		return nil, err
	}
	summaryFresh := summary != nil && summary.Version >= usageSummaryVersion

	switch {
	case hasLastEvent && summaryFresh && summary.LastEventID == lastEvent.EventID:
		l.adoptSummaryLocked(summary)
		return l.summary, nil
	case !hasLastEvent && summaryFresh && summary.LastEventID == "":
		l.adoptSummaryLocked(summary)
		return l.summary, nil
	default:
		rebuilt, err := l.rebuildSummaryLocked()
		if err != nil {
			return nil, err
		}
		l.summary = rebuilt
		l.summaryLoaded = true
		return l.summary, nil
	}
}

func (l *UsageLedger) rebuildSummaryLocked() (*SessionUsageSummary, error) {
	summary := l.newEmptySummaryLocked()
	err := scanUsageEvents(l.usagePath(), func(evt UsageEvent) {
		applyUsageEvent(summary, evt)
	})
	if err != nil {
		return nil, err
	}
	if summary.FirstUserMessage == "" {
		summary.FirstUserMessage = l.firstUserMessageLocked()
	}
	if err := l.writeSummaryLocked(summary); err != nil {
		return nil, err
	}
	return summary, nil
}

func (l *UsageLedger) adoptSummaryLocked(summary *SessionUsageSummary) {
	if summary == nil {
		return
	}
	if summary.FirstUserMessage == "" {
		summary.FirstUserMessage = l.firstUserMessageLocked()
	}
	if summary.SessionID == "" {
		summary.SessionID = filepath.Base(l.sessionDir)
	}
	if summary.ProjectPath == "" {
		summary.ProjectPath = l.projectPath
	}
	if summary.ProjectID == "" {
		summary.ProjectID = ProjectIDForPath(summary.ProjectPath)
	}
	if summary.Status == "" {
		summary.Status = "active"
	}
	if summary.ByProvider == nil {
		summary.ByProvider = make(map[string]*UsageAggregate)
	}
	if summary.ByModelRef == nil {
		summary.ByModelRef = make(map[string]*UsageAggregate)
	}
	if summary.ByAgent == nil {
		summary.ByAgent = make(map[string]*UsageAggregate)
	}
	if summary.ByPurpose == nil {
		summary.ByPurpose = make(map[string]*UsageAggregate)
	}
	if summary.ByDate == nil {
		summary.ByDate = make(map[string]*UsageAggregate)
	}
	if summary.ByDateModelRef == nil {
		summary.ByDateModelRef = make(map[string]map[string]*UsageAggregate)
	}
	if summary.ByDateAgent == nil {
		summary.ByDateAgent = make(map[string]map[string]*UsageAggregate)
	}
	l.summary = summary
	l.summaryLoaded = true
}

func (l *UsageLedger) newEmptySummaryLocked() *SessionUsageSummary {
	return &SessionUsageSummary{
		Version:          usageSummaryVersion,
		SessionID:        filepath.Base(l.sessionDir),
		ProjectID:        l.projectID,
		ProjectPath:      l.projectPath,
		Timezone:         time.Local.String(),
		FirstUserMessage: l.firstUserMessageLocked(),
		Status:           "active",
		ByProvider:       make(map[string]*UsageAggregate),
		ByModelRef:       make(map[string]*UsageAggregate),
		ByAgent:          make(map[string]*UsageAggregate),
		ByPurpose:        make(map[string]*UsageAggregate),
		ByDate:           make(map[string]*UsageAggregate),
		ByDateModelRef:   make(map[string]map[string]*UsageAggregate),
		ByDateAgent:      make(map[string]map[string]*UsageAggregate),
	}
}

func (l *UsageLedger) writeSummaryLocked(summary *SessionUsageSummary) error {
	if summary == nil {
		return nil
	}
	if err := os.MkdirAll(l.sessionDir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal usage summary: %w", err)
	}
	tmpPath := filepath.Join(l.sessionDir, fmt.Sprintf("usage-summary.%d.json.tmp", time.Now().UnixNano()))
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write temp usage summary: %w", err)
	}
	if err := os.Rename(tmpPath, l.summaryPath()); err != nil {
		return fmt.Errorf("rename usage summary: %w", err)
	}
	return nil
}

func applyUsageEvent(summary *SessionUsageSummary, event UsageEvent) {
	if summary == nil {
		return
	}
	if summary.Version == 0 {
		summary.Version = usageSummaryVersion
	}
	if summary.SessionID == "" {
		summary.SessionID = event.SessionID
	}
	if summary.ProjectID == "" {
		summary.ProjectID = event.ProjectID
	}
	if summary.ProjectPath == "" {
		summary.ProjectPath = event.ProjectPath
	}
	if summary.Timezone == "" {
		summary.Timezone = event.Timezone
	}
	if summary.Status == "" {
		summary.Status = "active"
	}
	if summary.CreatedAt.IsZero() {
		summary.CreatedAt = event.OccurredAt
	}
	summary.LastUpdatedAt = event.OccurredAt
	summary.LastEventID = event.EventID
	summary.EventCount++

	addUsageAggregate(&summary.UsageTotal, event)
	addUsageAggregate(summaryGroup(summary.ByProvider, event.Provider), event)
	addUsageAggregate(summaryGroup(summary.ByModelRef, usageModelKey(event)), event)
	addUsageAggregate(summaryGroup(summary.ByAgent, normalizeAgentID(event.AgentID)), event)
	addUsageAggregate(summaryGroup(summary.ByPurpose, usageKeyOrUnknown(event.Purpose)), event)
	dateKey := usageKeyOrUnknown(event.LocalDate)
	addUsageAggregate(summaryGroup(summary.ByDate, dateKey), event)
	addUsageAggregate(summaryNestedGroup(summary.ByDateModelRef, dateKey, usageModelKey(event)), event)
	addUsageAggregate(summaryNestedGroup(summary.ByDateAgent, dateKey, normalizeAgentID(event.AgentID)), event)
}

func addUsageAggregate(dst *UsageAggregate, event UsageEvent) {
	if dst == nil {
		return
	}
	dst.LLMCalls++
	dst.InputTokens += event.UsageRaw.InputTokens
	dst.OutputTokens += event.UsageRaw.OutputTokens
	dst.CacheReadTokens += event.UsageRaw.CacheReadTokens
	dst.CacheWriteTokens += event.UsageRaw.CacheWriteTokens
	dst.ReasoningTokens += event.UsageRaw.ReasoningTokens
	dst.ProviderTotalTokens += event.UsageRaw.InputTokens + event.UsageRaw.OutputTokens
	dst.BillingTotalTokens += event.BillingUsage.BillingTotalTokens
	dst.TotalCost += event.Cost.TotalCost
}

func summaryGroup(groups map[string]*UsageAggregate, key string) *UsageAggregate {
	if groups == nil {
		return nil
	}
	key = usageKeyOrUnknown(key)
	agg, ok := groups[key]
	if !ok {
		agg = &UsageAggregate{}
		groups[key] = agg
	}
	return agg
}

func summaryNestedGroup(groups map[string]map[string]*UsageAggregate, outerKey, innerKey string) *UsageAggregate {
	if groups == nil {
		return nil
	}
	outerKey = usageKeyOrUnknown(outerKey)
	inner, ok := groups[outerKey]
	if !ok {
		inner = make(map[string]*UsageAggregate)
		groups[outerKey] = inner
	}
	return summaryGroup(inner, innerKey)
}

func cloneUsageSummary(in *SessionUsageSummary) *SessionUsageSummary {
	if in == nil {
		return nil
	}
	out := *in
	out.ByProvider = cloneUsageAggregateMap(in.ByProvider)
	out.ByModelRef = cloneUsageAggregateMap(in.ByModelRef)
	out.ByAgent = cloneUsageAggregateMap(in.ByAgent)
	out.ByPurpose = cloneUsageAggregateMap(in.ByPurpose)
	out.ByDate = cloneUsageAggregateMap(in.ByDate)
	out.ByDateModelRef = cloneUsageAggregateNestedMap(in.ByDateModelRef)
	out.ByDateAgent = cloneUsageAggregateNestedMap(in.ByDateAgent)
	return &out
}

func cloneUsageAggregateMap(in map[string]*UsageAggregate) map[string]*UsageAggregate {
	if len(in) == 0 {
		return make(map[string]*UsageAggregate)
	}
	out := make(map[string]*UsageAggregate, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		cp := *v
		out[k] = &cp
	}
	return out
}

func cloneUsageAggregateNestedMap(in map[string]map[string]*UsageAggregate) map[string]map[string]*UsageAggregate {
	if len(in) == 0 {
		return make(map[string]map[string]*UsageAggregate)
	}
	out := make(map[string]map[string]*UsageAggregate, len(in))
	for key, inner := range in {
		out[key] = cloneUsageAggregateMap(inner)
	}
	return out
}

func readUsageSummaryFile(path string) (*SessionUsageSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var summary SessionUsageSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

func readLastCompleteUsageEvent(path string) (UsageEvent, bool, error) {
	var (
		last UsageEvent
		ok   bool
	)
	err := scanUsageEvents(path, func(evt UsageEvent) {
		last = evt
		ok = true
	})
	return last, ok, err
}

func scanUsageEvents(path string, fn func(UsageEvent)) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var evt UsageEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		if evt.Version == 0 {
			evt.Version = usageEventVersion
		}
		if evt.BillingUsage.BillingTotalTokens == 0 &&
			evt.BillingUsage.InputTokens == 0 &&
			evt.BillingUsage.OutputTokens == 0 &&
			evt.BillingUsage.CacheReadTokens == 0 &&
			evt.BillingUsage.CacheWriteTokens == 0 {
			evt.BillingUsage = NormalizeBillingUsage(evt.UsageRaw)
		}
		if evt.Cost.Currency == "" {
			evt.Cost.Currency = "USD"
		}
		if evt.Provider == "" || evt.ModelID == "" {
			evt.Provider, evt.ModelID = SplitModelRef(evt.RunningModelRef)
		}
		if evt.LocalDate == "" && !evt.OccurredAt.IsZero() {
			evt.LocalDate = evt.OccurredAt.In(evt.OccurredAt.Location()).Format("2006-01-02")
		}
		fn(evt)
	}
	return scanner.Err()
}

func usageModelKey(event UsageEvent) string {
	if ref := strings.TrimSpace(event.RunningModelRef); ref != "" {
		return ref
	}
	if event.Provider != "" && event.ModelID != "" {
		return event.Provider + "/" + event.ModelID
	}
	return usageKeyOrUnknown(event.ModelID)
}

func usageKeyOrUnknown(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	return s
}

func usageFirstUserPreview(content string) string {
	content = strings.ReplaceAll(content, "\r\n", " ")
	content = strings.ReplaceAll(content, "\n", " ")
	content = strings.ReplaceAll(content, "\r", " ")
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) > maxUsageSummaryFirstUserPreview {
		content = string([]rune(content)[:maxUsageSummaryFirstUserPreview]) + "…"
	}
	return content
}

func (l *UsageLedger) firstUserMessageLocked() string {
	if l.firstUserMessage != "" {
		return l.firstUserMessage
	}

	f, err := os.Open(l.mainPath())
	if err != nil {
		return ""
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	for {
		var msg message.Message
		if err := dec.Decode(&msg); err != nil {
			break
		}
		if msg.Role != "user" {
			continue
		}
		// Skip compaction summary messages: when called after a compaction has
		// already rewritten main.jsonl, the first user message is the summary
		// itself; using it here would poison OriginalFirstUserMessage.
		if msg.IsCompactionSummary {
			continue
		}
		preview := usageFirstUserPreview(message.UserPromptPlainText(msg))
		if preview != "" {
			l.firstUserMessage = preview
			return preview
		}
	}
	return ""
}

// OriginalFirstUserMessage returns the original first user message preview,
// preserved across compaction. Returns empty string if not yet set.
func (l *UsageLedger) OriginalFirstUserMessage() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.originalFirstUserMessage
}

func (l *UsageLedger) usagePath() string {
	return filepath.Join(l.sessionDir, "usage.jsonl")
}

func (l *UsageLedger) summaryPath() string {
	return filepath.Join(l.sessionDir, "usage-summary.json")
}

func (l *UsageLedger) mainPath() string {
	return filepath.Join(l.sessionDir, "main.jsonl")
}
