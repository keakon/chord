package agent

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

func (a *MainAgent) refreshVisibleContextReductionStats(messages []message.Message) {
	if a == nil {
		return
	}
	a.clearReductionCache(true)
	_ = a.prepareMessagesForLLMWithOptions(messages, false)
}

// statsRefreshDeferral delays the restore-time reduction scan (file stat/hash
// revalidation included) so it runs after the restore burst settles instead of
// stalling activateLoadedSession for a sidebar statistic.
const statsRefreshDeferral = 250 * time.Millisecond

// scheduleReductionStatsRefresh recomputes the visible context-reduction stats
// on the event loop shortly after the caller returns. The handler re-derives
// the history from the manager, so a session switch between scheduling and
// firing simply refreshes the new session's stats.
func (a *MainAgent) scheduleReductionStatsRefresh() {
	if a == nil {
		return
	}
	time.AfterFunc(statsRefreshDeferral, func() {
		a.sendEvent(Event{Type: EventRefreshReductionStats})
	})
}

// handleRefreshReductionStats runs the deferred stats refresh on the event
// loop, where reading turn state needs no synchronization.
func (a *MainAgent) handleRefreshReductionStats() {
	if a == nil || a.ctxMgr == nil {
		return
	}
	a.refreshVisibleContextReductionStats(a.ctxMgr.Snapshot())
}

func highLevelContextReductionStats(mgr *ctxmgr.Manager, original, reduced []message.Message) ContextReductionStats {
	stats := ContextReductionStats{
		TokensBefore: estimateMessagesTokens(mgr, original),
		TokensAfter:  estimateMessagesTokens(mgr, reduced),
	}
	stats.setCurrentMessageSurface(reduced)
	if stats.TokensBefore > stats.TokensAfter {
		stats.TokensSaved = stats.TokensBefore - stats.TokensAfter
	}
	stats.setCurrentReductionSavings(original, reduced)
	return stats
}

func (s *ContextReductionStats) setCurrentReductionSavings(original, reduced []message.Message) {
	s.Messages = 0
	s.Bytes = 0
	limit := min(len(original), len(reduced))
	for i := range limit {
		saved := contextContributorBytes(original[i]) - contextContributorBytes(reduced[i])
		if saved <= 0 {
			continue
		}
		s.Messages++
		s.Bytes += saved
	}
}

func (s *ContextReductionStats) setCurrentMessageSurface(messages []message.Message) {
	s.CurrentBytes = ctxmgr.MessagePayloadBytes(messages)
	s.CurrentMessages = len(messages)
}

func (a *MainAgent) setCurrentRequestSurface(stats *ContextReductionStats, messages []message.Message) {
	stats.setCurrentMessageSurface(messages)
	if a != nil && a.ctxMgr != nil {
		stats.CurrentBytes += a.ctxMgr.SystemPromptPayloadBytes()
	}
	if a != nil {
		stats.CurrentBytes += toolDefinitionBytes(a.mainLLMToolDefinitions())
	}
}

func (a *MainAgent) fillReductionModelContinuity(stats *ContextReductionStats) {
	if a == nil || stats == nil {
		return
	}
	stats.fillModelContinuity(a.llmModelContinuitySnapshot())
}

func (s *ContextReductionStats) fillModelContinuity(snapshot llmModelContinuitySnapshot) {
	if s == nil {
		return
	}
	s.PreviousModel = snapshot.PreviousModel
	s.ModelChanged = snapshot.PreviousModel != "" && snapshot.CurrentModel != "" && snapshot.PreviousModel != snapshot.CurrentModel
	s.ModelRunLength = snapshot.ProjectedModelRunLength
}

func (a *MainAgent) setContextReductionStats(stats ContextReductionStats) {
	if a == nil {
		return
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	a.contextReductionStats = cloneContextReductionStats(stats)
}

func cloneContextReductionStats(stats ContextReductionStats) ContextReductionStats {
	stats.ByToolAndRule = cloneContextReductionBuckets(stats.ByToolAndRule)
	stats.SkippedByReason = cloneContextReductionIntMap(stats.SkippedByReason)
	stats.OverCompression = cloneContextReductionIntMap(stats.OverCompression)
	stats.OverCompressionByTool = cloneContextReductionIntMap(stats.OverCompressionByTool)
	stats.ByRetentionLevel = cloneContextReductionIntMap(stats.ByRetentionLevel)
	return stats
}

func cloneContextReductionBuckets(buckets map[string]ContextReductionBucket) map[string]ContextReductionBucket {
	if buckets == nil {
		return nil
	}
	cloned := make(map[string]ContextReductionBucket, len(buckets))
	maps.Copy(cloned, buckets)
	return cloned
}

func cloneContextReductionIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	cloned := make(map[string]int, len(values))
	maps.Copy(cloned, values)
	return cloned
}

func (a *MainAgent) resetContextReductionStats() {
	a.setContextReductionStats(ContextReductionStats{})
}

type contextContributor struct {
	Index  int
	Role   message.Role
	Tool   string
	Bytes  int
	Tokens int
}

func contextContributorLabel(c contextContributor) string {
	if c.Tool != "" {
		return fmt.Sprintf("#%d %s/%s bytes=%d tokens_est=%d", c.Index, c.Role, c.Tool, c.Bytes, c.Tokens)
	}
	return fmt.Sprintf("#%d %s bytes=%d tokens_est=%d", c.Index, c.Role, c.Bytes, c.Tokens)
}

func contextContributorBytes(msg message.Message) int {
	n := len(msg.Content)
	for _, part := range msg.Parts {
		n += len(part.Text)
	}
	for _, tc := range msg.ToolCalls {
		n += len(tc.Args)
	}
	for _, tb := range msg.ThinkingBlocks {
		n += len(tb.Thinking)
	}
	return n
}

func topContextContributors(mgr *ctxmgr.Manager, messages []message.Message, limit int) []contextContributor {
	if limit <= 0 || len(messages) == 0 {
		return nil
	}
	callMeta := requesttrim.BuildToolCallMeta(messages)
	contributors := make([]contextContributor, 0, len(messages))
	for i, msg := range messages {
		bytes := contextContributorBytes(msg)
		if bytes <= 0 {
			continue
		}
		toolName := ""
		if msg.Role == message.RoleTool {
			toolName = strings.TrimSpace(callMeta[msg.ToolCallID].Name)
		}
		contributors = append(contributors, contextContributor{
			Index:  i,
			Role:   msg.Role,
			Tool:   toolName,
			Bytes:  bytes,
			Tokens: estimateMessageTokens(mgr, msg),
		})
	}
	sort.SliceStable(contributors, func(i, j int) bool {
		return contributors[i].Bytes > contributors[j].Bytes
	})
	if len(contributors) > limit {
		contributors = contributors[:limit]
	}
	return contributors
}

func (a *MainAgent) GetContextReductionStats() ContextReductionStats {
	if a == nil {
		return ContextReductionStats{}
	}
	target := a.focusedAgentSnapshot()
	if externalBackendForTarget(target) != "" {
		return ContextReductionStats{}
	}
	if target.sub != nil {
		return target.sub.GetContextReductionStats()
	}
	return a.currentMainContextReductionStats()
}

func (a *MainAgent) currentMainContextReductionStats() ContextReductionStats {
	if a == nil {
		return ContextReductionStats{}
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	return cloneContextReductionStats(a.contextReductionStats)
}

func isZeroContextReductionStats(stats ContextReductionStats) bool {
	return stats.Messages == 0 &&
		stats.Bytes == 0 &&
		stats.TokensSaved == 0 &&
		!stats.Protected &&
		!stats.ReusedStable &&
		len(stats.ByToolAndRule) == 0 &&
		len(stats.SkippedByReason) == 0 &&
		len(stats.OverCompression) == 0 &&
		len(stats.OverCompressionByTool) == 0
}

func (a *MainAgent) preparedContextReductionStatsForTurn(turnID uint64) ContextReductionStats {
	if a == nil || turnID == 0 {
		return ContextReductionStats{}
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if a.lastPreparedLLMTurnID != turnID {
		return ContextReductionStats{}
	}
	return cloneContextReductionStats(a.lastPreparedReductionStats)
}
