package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/golog/log"

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
	compressed := s.ctxMgr.CompressForTarget(messages, target)
	if len(compressed) == 0 || len(compressed) >= len(messages) {
		return false
	}
	archiveRef, archiveErr := s.archiveContextRecoveryHistory(messages)
	if archiveErr != nil {
		log.Warnf("SubAgent context recovery could not archive durable history agent=%v error=%v", s.instanceID, archiveErr)
		return false
	}
	checkpoint := message.Message{
		Role: message.RoleUser,
		Content: fmt.Sprintf(
			"[system] SubAgent context checkpoint: %d earlier messages were removed after the provider rejected an oversized request. Continue the same task using the preserved recent context. Full pre-checkpoint history: %s.",
			len(messages)-len(compressed),
			archiveRef,
		),
	}
	if len(compressed) > 1 && strings.HasPrefix(compressed[1].Content, "[system] Context was compressed") {
		compressed[1] = checkpoint
	} else {
		compressed = append(compressed[:1], append([]message.Message{checkpoint}, compressed[1:]...)...)
	}
	if s.recovery != nil {
		s.parent.flushPersist()
		if rewriteErr := s.recovery.RewriteLog(s.instanceID, compressed); rewriteErr != nil {
			log.Warnf("SubAgent context recovery could not rewrite durable history agent=%v error=%v", s.instanceID, rewriteErr)
			return false
		}
	}
	s.ctxMgr.RestoreMessages(compressed)
	s.turn.SubAgentContextRecoveryCount++
	log.Infof("SubAgent context length recovery compressed history agent=%v turn_id=%v before=%v after=%v target_tokens=%v", s.instanceID, s.turn.ID, len(messages), len(compressed), target)
	s.llmRequestInFlight.Store(true)
	s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
	return true
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
