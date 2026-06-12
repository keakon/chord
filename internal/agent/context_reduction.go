package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

type ContextReductionStats struct {
	Messages        int
	Bytes           int
	CurrentBytes    int
	CurrentMessages int
	TokensBefore    int
	TokensAfter     int
	TokensSaved     int
	Protected       bool
	ReusedStable    bool
	ProtectReason   string
	ReuseReason     string
	SavedDelta      int
	PreviousModel   string
	ModelChanged    bool
	ModelRunLength  int
	ByToolAndRule   map[string]ContextReductionBucket
}

const (
	// Cache protection starts on the projected third same-model request: the
	// first two successful requests give provider prompt caches time to be
	// written and become reliably hittable.
	contextReductionCacheProtectMinModelRunLength = 3
	contextReductionWrapUpGraceRequests           = 1

	contextProtectReasonNone           = ""
	contextProtectReasonWarmupLowUsage = "warmup_low_usage"
	contextProtectReasonWrapUpGrace    = "wrap_up_grace"

	contextReuseReasonNone                = ""
	contextReuseReasonBelowIncrementalMin = "below_incremental_min"
	contextReuseReasonNoPreviousSavings   = "no_previous_savings"
	contextReuseReasonHighPressure        = "high_pressure"
	contextReuseReasonForcePrune          = "force_prune"
)

type ContextReductionBucket struct {
	Messages    int
	Bytes       int
	TokensSaved int
}

type contextReductionPolicy struct {
	ConfirmAgeTurns         int
	ErrorAgeTurns           int
	HighRiskProtectAgeTurns int
	ShellSuccessAgeTurns    int
	ReadLikeAgeTurns        int
	StaleAgeTurns           int
	ShellSuccessBytes       int
	ReadLikeOutputBytes     int
	StaleOutputBytes        int
	WrapUpGraceRequests     int
	MinToolResultsPrune     int
	CacheAwareMinUsage      float64
	WarmupMessageLimit      int
	MinIncrementalTokens    int
	HighPressureUsage       float64
	ForcePruneUsage         float64
}

func defaultContextReductionPolicy() contextReductionPolicy {
	return contextReductionPolicy{
		ConfirmAgeTurns:         compactConfirmAgeTurns,
		ErrorAgeTurns:           compactErrorAgeTurns,
		HighRiskProtectAgeTurns: compactHighRiskProtectAgeTurns,
		ShellSuccessAgeTurns:    compactBashSuccessAgeTurns,
		ReadLikeAgeTurns:        compactReadLikeAgeTurns,
		StaleAgeTurns:           compactStaleAgeTurns,
		ShellSuccessBytes:       compactBashSuccessBytes,
		ReadLikeOutputBytes:     compactReadLikeOutputBytes,
		StaleOutputBytes:        compactStaleOutputBytes,
		WrapUpGraceRequests:     contextReductionWrapUpGraceRequests,
		MinToolResultsPrune:     compactMinToolResultsPrune,
		CacheAwareMinUsage:      0.75,
		WarmupMessageLimit:      32,
		MinIncrementalTokens:    4096,
		HighPressureUsage:       0.80,
		ForcePruneUsage:         0.90,
	}
}

func (a *MainAgent) contextReductionPolicy() contextReductionPolicy {
	policy := defaultContextReductionPolicy()
	if a == nil {
		return policy
	}
	for _, cfg := range []*config.Config{a.globalConfig, a.projectConfig} {
		if cfg == nil {
			continue
		}
		policy.applyConfig(cfg.Context.Reduction)
	}
	return policy
}

func (p *contextReductionPolicy) applyConfig(cfg config.ContextReductionConfig) {
	if cfg.ConfirmAgeTurns > 0 {
		p.ConfirmAgeTurns = cfg.ConfirmAgeTurns
	}
	if cfg.ErrorAgeTurns > 0 {
		p.ErrorAgeTurns = cfg.ErrorAgeTurns
	}
	if cfg.HighRiskProtectAgeTurns > 0 {
		p.HighRiskProtectAgeTurns = cfg.HighRiskProtectAgeTurns
	}
	if cfg.ShellSuccessAgeTurns > 0 {
		p.ShellSuccessAgeTurns = cfg.ShellSuccessAgeTurns
	}
	if cfg.ReadLikeAgeTurns > 0 {
		p.ReadLikeAgeTurns = cfg.ReadLikeAgeTurns
	}
	if cfg.StaleAgeTurns > 0 {
		p.StaleAgeTurns = cfg.StaleAgeTurns
	}
	if cfg.ShellSuccessBytes > 0 {
		p.ShellSuccessBytes = cfg.ShellSuccessBytes
	}
	if cfg.ReadLikeOutputBytes > 0 {
		p.ReadLikeOutputBytes = cfg.ReadLikeOutputBytes
	}
	if cfg.StaleOutputBytes > 0 {
		p.StaleOutputBytes = cfg.StaleOutputBytes
	}
	if cfg.WrapUpGraceRequests > 0 {
		p.WrapUpGraceRequests = cfg.WrapUpGraceRequests
	}
	if cfg.MinToolResultsPrune > 0 {
		p.MinToolResultsPrune = cfg.MinToolResultsPrune
	}
	if cfg.CacheAwareMinUsage > 0 {
		p.CacheAwareMinUsage = cfg.CacheAwareMinUsage
	}
	if cfg.WarmupMessageLimit > 0 {
		p.WarmupMessageLimit = cfg.WarmupMessageLimit
	}
	if cfg.MinIncrementalTokens > 0 {
		p.MinIncrementalTokens = cfg.MinIncrementalTokens
	}
	if cfg.HighPressureUsage > 0 {
		p.HighPressureUsage = cfg.HighPressureUsage
	}
	if cfg.ForcePruneUsage > 0 {
		p.ForcePruneUsage = cfg.ForcePruneUsage
	}
}

func (p contextReductionPolicy) protectCachedContextReason(messageCount, estimatedTokens, inputBudget int) string {
	if inputBudget <= 0 || p.CacheAwareMinUsage <= 0 || p.WarmupMessageLimit <= 0 {
		return contextProtectReasonNone
	}
	if messageCount > p.WarmupMessageLimit {
		return contextProtectReasonNone
	}
	if float64(estimatedTokens)/float64(inputBudget) < p.CacheAwareMinUsage {
		return contextProtectReasonWarmupLowUsage
	}
	return contextProtectReasonNone
}

func (p contextReductionPolicy) shouldProtectCachedContextForModelRun(messageCount, estimatedTokens, inputBudget, modelRunLength int) (bool, string) {
	reason := p.protectCachedContextReason(messageCount, estimatedTokens, inputBudget)
	if reason == contextProtectReasonNone {
		return false, reason
	}
	if modelRunLength < contextReductionCacheProtectMinModelRunLength {
		return false, reason
	}
	return true, reason
}

func (p contextReductionPolicy) contextUsage(estimatedTokens, inputBudget int) float64 {
	if inputBudget <= 0 {
		return 1
	}
	return float64(estimatedTokens) / float64(inputBudget)
}

func (p contextReductionPolicy) reuseStableReductionSurfaceReason(stats, previous ContextReductionStats, estimatedTokens, inputBudget int) (string, int) {
	if p.MinIncrementalTokens <= 0 || stats.TokensSaved <= 0 || previous.TokensSaved <= 0 {
		return contextReuseReasonNoPreviousSavings, 0
	}
	usage := p.contextUsage(estimatedTokens, inputBudget)
	if p.ForcePruneUsage > 0 && usage >= p.ForcePruneUsage {
		return contextReuseReasonForcePrune, stats.TokensSaved - previous.TokensSaved
	}
	if p.HighPressureUsage > 0 && usage >= p.HighPressureUsage {
		return contextReuseReasonHighPressure, stats.TokensSaved - previous.TokensSaved
	}
	delta := stats.TokensSaved - previous.TokensSaved
	if delta < p.MinIncrementalTokens {
		return contextReuseReasonBelowIncrementalMin, delta
	}
	return contextReuseReasonNone, delta
}

var readResultRangeRe = regexp.MustCompile(`^READ_RESULT\b.*\blines=(\d+)-(\d+)\b.*\btotal=(\d+)\b`)

func reduceDiagnosticsToolOutput(content string) (string, bool) {
	idx := strings.Index(content, "\n\nDiagnostics:\n")
	sepLen := len("\n\nDiagnostics:\n")
	if idx < 0 {
		idx = strings.Index(content, "\nDiagnostics:\n")
		sepLen = len("\nDiagnostics:\n")
	}
	if idx < 0 {
		return content, false
	}
	prefix := strings.TrimRight(content[:idx], "\n")
	diagnostics := strings.TrimSpace(content[idx+sepLen:])
	if diagnostics == "" {
		return content, false
	}
	lines := strings.Split(diagnostics, "\n")
	summary := preferredDiagnosticsSummaryLine(lines)
	if summary == "" {
		summary = "diagnostics were present"
	}
	return prefix + "\n\nDiagnostics summary:\n[Older diagnostics details omitted; latest tool results should be trusted over this stale output.]\n" + summary, true
}

func preferredDiagnosticsSummaryLine(lines []string) string {
	preferredPrefixes := []string{
		"Python diagnostics skipped:",
		"Ruff diagnostics failed:",
		"Ruff quick diagnostics failed:",
	}
	for _, prefix := range preferredPrefixes {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, prefix) {
				return trimmed
			}
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !isNoisyDiagnosticsSummaryLine(trimmed) {
			return trimmed
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type requestReductionClass string

const (
	requestReductionNone        requestReductionClass = ""
	requestReductionRepeated    requestReductionClass = "repeated_output"
	requestReductionToolError   requestReductionClass = "tool_error"
	requestReductionConfirm     requestReductionClass = "confirmation"
	requestReductionDiagnostics requestReductionClass = "diagnostics"
	requestReductionReadLike    requestReductionClass = "read_like"
	requestReductionSearch      requestReductionClass = "search_result"
	requestReductionJSON        requestReductionClass = "json_blob"
	requestReductionLongLog     requestReductionClass = "long_log"
	requestReductionShellOK     requestReductionClass = "shell_success"
	requestReductionGeneric     requestReductionClass = "generic_stale"
)

type requestReductionContext struct {
	ToolName    string
	Meta        toolCallMeta
	Content     string
	ToolStatus  string
	Age         int
	UserTurnAge int
	Policy      contextReductionPolicy
	Repeated    bool
	ToolResults int
}

func classifyRequestReductionToolOutput(ctx requestReductionContext) requestReductionClass {
	if ctx.Repeated && ctx.Age >= 1 {
		return requestReductionRepeated
	}
	if ctx.UserTurnAge < ctx.Policy.HighRiskProtectAgeTurns && isHighRiskToolOutput(ctx) {
		return requestReductionNone
	}
	if ctx.Age >= ctx.Policy.ErrorAgeTurns && (isToolResultErrorStatus(ctx.ToolStatus) || isToolErrorContent(ctx.Content)) {
		return requestReductionToolError
	}
	if ctx.Age >= ctx.Policy.ConfirmAgeTurns && isConfirmationOutput(ctx.Content) {
		return requestReductionConfirm
	}
	if ctx.Age >= 1 && (ctx.ToolName == tools.NameEdit || ctx.ToolName == tools.NameWrite) && strings.Contains(ctx.Content, "Diagnostics:") {
		return requestReductionDiagnostics
	}
	if ctx.Age >= ctx.Policy.ShellSuccessAgeTurns && len(ctx.Content) > ctx.Policy.ShellSuccessBytes && ctx.ToolName == tools.NameShell {
		if looksLikeStructuredJSON(ctx.Content) {
			return requestReductionJSON
		}
		if looksLikeBuildLikeLog(ctx) {
			return requestReductionLongLog
		}
		return requestReductionShellOK
	}
	if ctx.Age >= ctx.Policy.ReadLikeAgeTurns && len(ctx.Content) > ctx.Policy.ReadLikeOutputBytes {
		switch {
		case looksLikeSearchResult(ctx):
			return requestReductionSearch
		case looksLikeStructuredJSON(ctx.Content):
			return requestReductionJSON
		case contextReductionIsReadLike(ctx.ToolName):
			return requestReductionReadLike
		case looksLikeBuildLikeLog(ctx):
			return requestReductionLongLog
		}
	}
	if ctx.ToolResults >= ctx.Policy.MinToolResultsPrune && ctx.Age >= ctx.Policy.StaleAgeTurns && len(ctx.Content) > ctx.Policy.StaleOutputBytes {
		if looksLikeSearchResult(ctx) {
			return requestReductionSearch
		}
		if looksLikeStructuredJSON(ctx.Content) {
			return requestReductionJSON
		}
		if looksLikeBuildLikeLog(ctx) {
			return requestReductionLongLog
		}
		return requestReductionGeneric
	}
	return requestReductionNone
}

func isHighRiskToolOutput(ctx requestReductionContext) bool {
	if strings.TrimSpace(ctx.Content) == "" {
		return false
	}
	if looksLikeDiffOrPatch(ctx.Content) {
		return true
	}
	content := strings.ToLower(highRiskScanPrefix(ctx.Content))
	for _, marker := range []string{
		"traceback",
		"panic:",
		"exception",
		"segmentation fault",
		"permission denied",
		"access denied",
		"unauthorized",
		"forbidden",
		"expected:",
		"actual:",
		"assertion failed",
		"assert failed",
		"npm err!",
		"fatal:",
	} {
		if strings.Contains(content, marker) {
			return true
		}
	}
	if ctx.ToolName == tools.NameShell && strings.Contains(content, "failed") {
		return true
	}
	return false
}

func highRiskScanPrefix(content string) string {
	const maxHighRiskScanBytes = 64 * 1024
	if len(content) <= maxHighRiskScanBytes {
		return content
	}
	return content[:maxHighRiskScanBytes]
}

func looksLikeDiffOrPatch(content string) bool {
	seenHeader := false
	start := 0
	for range 80 {
		if start > len(content) {
			break
		}
		line := content[start:]
		if newline := strings.IndexByte(line, '\n'); newline >= 0 {
			line = line[:newline]
			start += newline + 1
		} else {
			start = len(content) + 1
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "diff --git "), strings.HasPrefix(trimmed, "--- "), strings.HasPrefix(trimmed, "+++ "):
			seenHeader = true
		case seenHeader && strings.HasPrefix(trimmed, "@@"):
			return true
		case strings.HasPrefix(trimmed, "*** Begin Patch"), strings.HasPrefix(trimmed, "*** Update File:"), strings.HasPrefix(trimmed, "*** Add File:"), strings.HasPrefix(trimmed, "*** Delete File:"):
			return true
		}
	}
	return false
}

func reduceRequestToolOutput(class requestReductionClass, ctx requestReductionContext) (string, string, bool) {
	switch class {
	case requestReductionRepeated:
		return fmt.Sprintf("[Repeated %s output omitted; an identical call appears later.]", toolNameOrUnknown(ctx.ToolName)), "repeated", true
	case requestReductionToolError:
		return "[Older tool error omitted]", "error", true
	case requestReductionConfirm:
		return "[Confirmed]", "confirmation", true
	case requestReductionDiagnostics:
		if compacted, ok := reduceDiagnosticsToolOutput(ctx.Content); ok {
			return compacted, "diagnostics", true
		}
		return fmt.Sprintf("[Older %s output omitted from this request to save context.]", toolNameOrUnknown(ctx.Meta.Name)), "stale", true
	case requestReductionReadLike:
		return reduceReadLikeOutputSummary(ctx.ToolName, ctx.Meta.Args, ctx.Content), "read_like", true
	case requestReductionSearch:
		return reduceSearchLikeOutputSummary(ctx), "search_result", true
	case requestReductionJSON:
		if compacted, ok := reduceJSONBlobSummary(ctx); ok {
			return compacted, "json_blob", true
		}
		return fmt.Sprintf("[Older %s output omitted from this request to save context.]", toolNameOrUnknown(ctx.Meta.Name)), "stale", true
	case requestReductionLongLog:
		return reduceLongLogOutputSummary(ctx), "long_log", true
	case requestReductionShellOK:
		return fmt.Sprintf("[Older %s output omitted from this request to save context.]", tools.NameShell), "shell_success", true
	case requestReductionGeneric:
		return fmt.Sprintf("[Older %s output omitted from this request to save context.]", toolNameOrUnknown(ctx.Meta.Name)), "stale", true
	default:
		return "", "", false
	}
}

func looksLikeSearchResult(ctx requestReductionContext) bool {
	switch ctx.ToolName {
	case tools.NameGrep, tools.NameGlob:
		return true
	case tools.NameLsp:
		var parsed struct {
			Operation string `json:"operation"`
		}
		if err := json.Unmarshal([]byte(ctx.Meta.Args), &parsed); err != nil {
			return false
		}
		return strings.TrimSpace(parsed.Operation) == "references"
	default:
		return false
	}
}

func looksLikeStructuredJSON(content string) bool {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) < 2 {
		return false
	}
	return (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) || (strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))
}

func looksLikeBuildLikeLog(ctx requestReductionContext) bool {
	if ctx.ToolName != tools.NameShell && ctx.ToolName != tools.NameEdit && ctx.ToolName != tools.NameWrite {
		return false
	}
	content := strings.TrimSpace(ctx.Content)
	if content == "" {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "error") ||
			strings.Contains(lower, "warning") ||
			strings.Contains(lower, "failed") ||
			strings.Contains(lower, "failure") ||
			strings.Contains(lower, "panic:") ||
			strings.Contains(lower, "traceback") ||
			strings.Contains(lower, "exception") ||
			strings.Contains(lower, "diagnostics:") ||
			strings.HasPrefix(lower, "fail ") ||
			strings.HasPrefix(lower, "--- fail") ||
			strings.HasPrefix(lower, "build failed") ||
			strings.HasPrefix(lower, "lint failed") {
			return true
		}
	}
	return false
}

func reduceSearchLikeOutputSummary(ctx requestReductionContext) string {
	toolName := toolNameOrUnknown(ctx.ToolName)
	snippetLines := summarizeRepresentativeLines(ctx.Content, 4)
	if len(snippetLines) == 0 {
		snippetLines = []string{"- (no preserved matches)"}
	}
	scope := reduceSearchScope(ctx)
	return fmt.Sprintf("[Older %s results summarized for this request to save context; %s; matches=%d]\n%s", toolName, scope, countMeaningfulLines(ctx.Content), strings.Join(snippetLines, "\n"))
}

func reduceSearchScope(ctx requestReductionContext) string {
	switch ctx.ToolName {
	case tools.NameGrep:
		var parsed struct {
			Pattern  string   `json:"pattern"`
			Paths    []string `json:"paths"`
			Includes []string `json:"includes"`
		}
		_ = json.Unmarshal([]byte(ctx.Meta.Args), &parsed)
		paths := reduceSearchList(parsed.Paths, ".")
		includes := reduceSearchList(parsed.Includes, "")
		return fmt.Sprintf("pattern=%q paths=%q includes=%q", strings.TrimSpace(parsed.Pattern), paths, includes)
	case tools.NameGlob:
		var parsed struct {
			Patterns []string `json:"patterns"`
			Path     string   `json:"path"`
		}
		_ = json.Unmarshal([]byte(ctx.Meta.Args), &parsed)
		return fmt.Sprintf("patterns=%q path=%q", reduceSearchList(parsed.Patterns, ""), blankToDefault(strings.TrimSpace(parsed.Path), "."))
	case tools.NameLsp:
		var parsed struct {
			Operation string `json:"operation"`
			Path      string `json:"path"`
			Line      int    `json:"line"`
		}
		_ = json.Unmarshal([]byte(ctx.Meta.Args), &parsed)
		return fmt.Sprintf("operation=%q path=%q line=%d", strings.TrimSpace(parsed.Operation), strings.TrimSpace(parsed.Path), parsed.Line)
	default:
		return "query preserved"
	}
}

func reduceSearchList(values []string, fallback string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return fallback
	}
	return strings.Join(parts, ",")
}

func reduceJSONBlobSummary(ctx requestReductionContext) (string, bool) {
	var decoded any
	if err := json.Unmarshal([]byte(ctx.Content), &decoded); err != nil {
		return "", false
	}
	switch v := decoded.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 8 {
			keys = append(keys[:8], fmt.Sprintf("... (+%d more)", len(v)-8))
		}
		return fmt.Sprintf("[Older %s JSON object summarized to save context; keys=%d]\n- top-level keys: %s", toolNameOrUnknown(ctx.ToolName), len(v), strings.Join(keys, ", ")), true
	case []any:
		items := summarizeJSONArrayItems(v, 3)
		if len(items) == 0 {
			items = []string{"- (no preserved items)"}
		}
		return fmt.Sprintf("[Older %s JSON array summarized to save context; items=%d]\n%s", toolNameOrUnknown(ctx.ToolName), len(v), strings.Join(items, "\n")), true
	default:
		return "", false
	}
}

func summarizeJSONArrayItems(items []any, limit int) []string {
	if limit <= 0 || len(items) == 0 {
		return nil
	}
	limit = min(limit, len(items))
	out := make([]string, 0, limit)
	for i := range limit {
		rendered, _ := json.Marshal(items[i])
		out = append(out, "- "+strings.ReplaceAll(compactTextSnippet(string(rendered), 180), "\n", " "))
	}
	return out
}

func reduceLongLogOutputSummary(ctx requestReductionContext) string {
	counts := summarizeLogSignalCounts(ctx.Content)
	lines := summarizeRepresentativeLogLines(ctx.Content, 4)
	if len(lines) == 0 {
		lines = []string{"- (no preserved log lines)"}
	}
	return fmt.Sprintf("[Older %s log summarized for this request to save context; errors=%d warnings=%d failed=%d]\n%s", toolNameOrUnknown(ctx.ToolName), counts.Errors, counts.Warnings, counts.Failures, strings.Join(lines, "\n"))
}

type logSignalCounts struct {
	Errors   int
	Warnings int
	Failures int
}

func summarizeLogSignalCounts(content string) logSignalCounts {
	var out logSignalCounts
	for _, line := range strings.Split(content, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "error") || strings.Contains(lower, "panic:") || strings.Contains(lower, "exception") || strings.Contains(lower, "traceback") {
			out.Errors++
		}
		if strings.Contains(lower, "warning") || strings.Contains(lower, "warn") {
			out.Warnings++
		}
		if strings.Contains(lower, "failed") || strings.Contains(lower, "failure") {
			out.Failures++
		}
	}
	return out
}

func summarizeRepresentativeLogLines(content string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	seen := make(map[string]struct{})
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if !strings.Contains(lower, "error") && !strings.Contains(lower, "warning") && !strings.Contains(lower, "failed") && !strings.Contains(lower, "panic:") && !strings.Contains(lower, "traceback") && !strings.Contains(lower, "exception") {
			continue
		}
		lineKey := compactTextSnippet(trimmed, 220)
		if _, ok := seen[lineKey]; ok {
			continue
		}
		seen[lineKey] = struct{}{}
		out = append(out, "- "+strings.ReplaceAll(lineKey, "\n", " "))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func summarizeRepresentativeLines(content string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, "- "+strings.ReplaceAll(compactTextSnippet(trimmed, 180), "\n", " "))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func countMeaningfulLines(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func isNoisyDiagnosticsSummaryLine(line string) bool {
	return strings.HasPrefix(line, "Diagnostics status:") ||
		strings.HasPrefix(line, "Used LSP diagnostics") ||
		strings.HasPrefix(line, "Used Ruff quick diagnostics") ||
		strings.HasPrefix(line, "Full Python semantic diagnostics")
}

func contextReductionIsReadLike(name string) bool {
	return tools.IsReadLike(name)
}

type readRequestSummary struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func parseReadRequestSummary(argsJSON string) readRequestSummary {
	if strings.TrimSpace(argsJSON) == "" {
		return readRequestSummary{}
	}
	var parsed readRequestSummary
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
		return readRequestSummary{}
	}
	parsed.Path = strings.TrimSpace(parsed.Path)
	return parsed
}

type displayedReadRange struct {
	Start int
	End   int
	Total int
	OK    bool
}

func parseDisplayedReadRange(content string) displayedReadRange {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		var out displayedReadRange
		if m := readResultRangeRe.FindStringSubmatch(line); len(m) == 4 {
			start, startErr := strconv.Atoi(m[1])
			end, endErr := strconv.Atoi(m[2])
			total, totalErr := strconv.Atoi(m[3])
			if startErr == nil && endErr == nil && totalErr == nil {
				out.Start = start
				out.End = end
				out.Total = total
				out.OK = true
				return out
			}
		}
		if n, _ := fmt.Sscanf(line, "(showing lines %d-%d of %d total", &out.Start, &out.End, &out.Total); n == 3 {
			out.OK = true
			return out
		}
		if n, _ := fmt.Sscanf(line, "(showing line %d of %d total", &out.Start, &out.Total); n == 2 {
			out.End = out.Start
			out.OK = true
			return out
		}
	}
	return displayedReadRange{}
}

func reduceReadOutputSummary(argsJSON, content string) string {
	displayed := parseDisplayedReadRange(content)
	body := stripReadResultHeaderLine(content)

	// Preferred path: a READ_RESULT range is present, so rebuild the same header
	// the read tool emits, marked truncated=stale, keeping only leading lines.
	if displayed.OK && strings.TrimSpace(body) != "" {
		headLines, headEnd := reduceReadHeadLines(body, displayed.Start, displayed.End, reduceSnippetChars)
		if len(headLines) > 0 {
			linesField := fmt.Sprintf("%d-%d", displayed.Start, headEnd)
			header := tools.FormatReadResultHeader(linesField, displayed.Total, tools.ReadTruncatedStale, "", "")
			return header + "\n" + strings.Join(headLines, "\n")
		}
	}

	// Fallback for read output without a parseable range (e.g. legacy sessions
	// or non-paged content): keep the path hint and a short excerpt.
	request := parseReadRequestSummary(argsJSON)
	snippet := strings.TrimSpace(compactTextSnippet(content, reduceSnippetChars))
	if snippet == "" {
		snippet = "(no preserved excerpt)"
	}
	requestedRange := ""
	if request.Offset > 0 || request.Limit > 0 {
		requestedRange = fmt.Sprintf("; requested range: offset=%d limit=%d", request.Offset, request.Limit)
	}
	if request.Path == "" {
		return "[Older " + tools.NameRead + " output truncated for this request to save context" + requestedRange + "]\n" + snippet
	}
	return fmt.Sprintf("[Older %s output truncated for this request to save context; path=%q%s]\n%s", tools.NameRead, request.Path, requestedRange, snippet)
}

// stripReadResultHeaderLine returns content without its leading READ_RESULT
// metadata line, leaving only the raw source body.
func stripReadResultHeaderLine(content string) string {
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		if readResultRangeRe.MatchString(strings.TrimSpace(content[:idx])) {
			return content[idx+1:]
		}
	}
	return content
}

// reduceReadHeadLines keeps whole leading lines of a read body within a rough
// character budget, never splitting a line. startLine is the 1-based source
// line of the first body line; endLine bounds the head so it never claims more
// than the original range. It returns the surviving head lines and the source
// line number of the last kept line.
func reduceReadHeadLines(body string, startLine, endLine, budget int) ([]string, int) {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) == 0 {
		return nil, startLine
	}
	maxLines := endLine - startLine + 1
	if maxLines > 0 && maxLines < len(lines) {
		lines = lines[:maxLines]
	}

	kept := 0
	used := 0
	for kept < len(lines) {
		cost := len(lines[kept]) + 1
		if kept > 0 && used+cost > budget {
			break
		}
		used += cost
		kept++
	}
	return lines[:kept], startLine + kept - 1
}

func reduceReadLikeOutputSummary(toolName, argsJSON, content string) string {
	switch strings.TrimSpace(toolName) {
	case tools.NameRead:
		return reduceReadOutputSummary(argsJSON, content)
	case tools.NameWebFetch:
		return reduceWebFetchOutputSummary(argsJSON, content)
	default:
		return fmt.Sprintf("[Older %s output omitted from this request to save context.]", toolNameOrUnknown(toolName))
	}
}

func reduceWebFetchOutputSummary(argsJSON, content string) string {
	var parsed struct {
		URL     string `json:"url"`
		Raw     bool   `json:"raw"`
		Timeout int    `json:"timeout"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &parsed)
	snippetSource := stripWebFetchResultHeaders(content)
	snippet := strings.TrimSpace(compactTextSnippet(snippetSource, reduceSnippetChars))
	if snippet == "" {
		snippet = "(no preserved excerpt)"
	}
	return fmt.Sprintf(
		"[Older %s output truncated for this request to save context; url=%q raw=%t timeout=%d]\n%s",
		tools.NameWebFetch,
		strings.TrimSpace(parsed.URL),
		parsed.Raw,
		parsed.Timeout,
		snippet,
	)
}

func stripWebFetchResultHeaders(content string) string {
	if _, after, ok := strings.Cut(content, "\n\n"); ok {
		return after
	}
	return content
}
