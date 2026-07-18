package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

const subAgentContextRecoveryHeadroom = 4096

func (s *SubAgent) recoverFromContextLength(err error) bool {
	if s == nil || s.turn == nil || !llm.IsContextLengthExceeded(err) || s.turn.SubAgentContextRecoveryCount >= 1 {
		return false
	}
	messages := s.ctxMgr.Snapshot()
	target := s.ctxMgr.GetUsableInputBudget()
	if target <= 0 {
		target = s.ctxMgr.GetMaxTokens()
	}
	if target <= 0 {
		return false
	}
	if target > subAgentContextRecoveryHeadroom {
		target -= subAgentContextRecoveryHeadroom
	} else {
		target = target * 3 / 4
	}
	compressed, ok := s.compactContextForTarget(messages, target, "provider rejected an oversized request")
	if !ok {
		return false
	}
	s.turn.SubAgentContextRecoveryCount++
	log.Infof("SubAgent context length recovery compressed history agent=%v turn_id=%v before=%v after=%v target_tokens=%v", s.instanceID, s.turn.ID, len(messages), len(compressed), target)
	s.llmRequestInFlight.Store(true)
	s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
	return true
}

func (s *SubAgent) prepareContextForLLM(messages []message.Message) []message.Message {
	if s == nil || len(messages) <= 2 {
		return messages
	}
	budget := s.ctxMgr.GetUsableInputBudget()
	if budget <= 0 {
		budget = s.ctxMgr.GetMaxTokens()
	}
	if budget <= 0 {
		return messages
	}
	usage := s.compactUsage
	if usage <= 0 || usage >= 1 {
		usage = config.DefaultSubAgentCompactUsage
	}
	estimated := ctxmgr.EstimateMessagesTokens(messages)
	if estimated < int(float64(budget)*usage) {
		return messages
	}
	target := int(float64(budget) * usage * 0.85)
	if compressed, ok := s.compactContextForTarget(messages, target, "proactive context budget protection"); ok {
		log.Infof("SubAgent proactively compressed context agent=%v turn_id=%v before=%v after=%v estimated_tokens=%v target_tokens=%v", s.instanceID, s.turn.ID, len(messages), len(compressed), estimated, target)
		return compressed
	}
	return messages
}

func (s *SubAgent) compactContextForTarget(messages []message.Message, target int, reason string) ([]message.Message, bool) {
	compressed := s.ctxMgr.CompressForTarget(messages, target)
	if len(compressed) == 0 || len(compressed) >= len(messages) {
		return nil, false
	}
	archiveRef, archiveErr := s.archiveContextRecoveryHistory(messages)
	if archiveErr != nil {
		log.Warnf("SubAgent context compression could not archive durable history agent=%v error=%v", s.instanceID, archiveErr)
		return nil, false
	}
	checkpoint := message.Message{
		Role: message.RoleUser,
		Content: fmt.Sprintf(
			"[system] SubAgent context checkpoint: %d earlier messages were removed for %s. Preserve the task contract, write scope, owner coordination, verification evidence, and unresolved limitations. Task: %s. Owner: %s/%s. Full pre-checkpoint history: %s.",
			len(messages)-len(compressed), reason, strings.TrimSpace(s.taskDesc), strings.TrimSpace(s.ownerAgentID), strings.TrimSpace(s.ownerTaskID), archiveRef,
		),
	}
	if len(compressed) > 1 && strings.HasPrefix(compressed[1].Content, "[system] Context was compressed") {
		compressed[1] = checkpoint
	} else {
		compressed = append(compressed[:1], append([]message.Message{checkpoint}, compressed[1:]...)...)
	}
	if s.recovery != nil {
		if s.parent != nil {
			s.parent.flushPersist()
		}
		if rewriteErr := s.recovery.RewriteLog(s.instanceID, compressed); rewriteErr != nil {
			log.Warnf("SubAgent context compression could not rewrite durable history agent=%v error=%v", s.instanceID, rewriteErr)
			return nil, false
		}
	}
	s.ctxMgr.RestoreMessages(compressed)
	stats := highLevelContextReductionStats(messages, compressed)
	s.reductionMu.Lock()
	s.reductionStats = stats
	s.reductionMu.Unlock()
	if s.parent != nil {
		s.parent.orchestrationMetrics.subAgentCompactions.Add(1)
		if stats.TokensSaved > 0 {
			s.parent.orchestrationMetrics.subAgentTokensSaved.Add(uint64(stats.TokensSaved))
		}
	}
	return compressed, true
}

func (s *SubAgent) archiveContextRecoveryHistory(messages []message.Message) (string, error) {
	if s == nil || strings.TrimSpace(s.sessionDir) == "" || strings.TrimSpace(s.instanceID) == "" {
		return "unavailable", nil
	}
	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal context archive: %w", err)
	}
	baseID := fmt.Sprintf("context-recovery-%d-%d-%d", s.turn.ID, s.turn.SubAgentContextRecoveryCount+1, time.Now().UnixNano())
	_, relPath, err := persistSubAgentArtifact(
		s.sessionDir,
		s.instanceID,
		baseID,
		"context_history",
		fmt.Sprintf("SubAgent %s context recovery history", s.instanceID),
		"```json\n"+string(data)+"\n```",
	)
	if err != nil {
		return "", err
	}
	if relPath == "" {
		return "unavailable", nil
	}
	return filepath.ToSlash(relPath), nil
}
