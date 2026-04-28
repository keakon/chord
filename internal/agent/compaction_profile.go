package agent

import (
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

type compactionProfile string

const (
	compactionProfileAuto         compactionProfile = config.CompactionProfileAuto
	compactionProfileContinuation compactionProfile = config.CompactionProfileContinuation
	compactionProfileArchival     compactionProfile = config.CompactionProfileArchival
)

func (a *MainAgent) configuredCompactionProfile() compactionProfile {
	for _, cfg := range []*config.Config{a.projectConfig, a.globalConfig} {
		if cfg == nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(cfg.Context.Compaction.Profile)) {
		case config.CompactionProfileContinuation:
			return compactionProfileContinuation
		case config.CompactionProfileArchival:
			return compactionProfileArchival
		case config.CompactionProfileAuto:
			return compactionProfileAuto
		}
	}
	return compactionProfileAuto
}

func (a *MainAgent) resolveCompactionProfile(todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState, evidenceItems []evidenceItem) compactionProfile {
	switch profile := a.configuredCompactionProfile(); profile {
	case compactionProfileContinuation, compactionProfileArchival:
		return profile
	default:
		if hasOpenTodosForCompaction(todos) || hasActiveSubAgentsForCompaction(subAgents) || hasActiveBackgroundObjectsForCompaction(backgroundObjects) || hasContinuationPressureEvidence(evidenceItems) {
			return compactionProfileContinuation
		}
		return compactionProfileArchival
	}
}

func applyCompactionProfile(profile compactionProfile, messages []message.Message, contextLimit int, evidenceItems []evidenceItem) ([]evidenceItem, []message.Message) {
	switch profile {
	case compactionProfileArchival:
		return filterCompactionEvidenceForArchival(evidenceItems), nil
	default:
		return evidenceItems, selectRecentTailMessages(messages, compactRecentTailTurns, recentTailTokenBudget(contextLimit))
	}
}

func filterCompactionEvidenceForArchival(items []evidenceItem) []evidenceItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]evidenceItem, 0, len(items))
	for _, item := range items {
		switch item.Kind {
		case evidenceUserCorrection, evidenceToolError, evidenceEscalate:
			out = append(out, item)
		}
	}
	return out
}

func hasOpenTodosForCompaction(todos []tools.TodoItem) bool {
	for _, todo := range todos {
		switch strings.TrimSpace(todo.Status) {
		case "pending", "in_progress":
			return true
		}
	}
	return false
}

func hasActiveSubAgentsForCompaction(subAgents []SubAgentInfo) bool {
	for _, sub := range subAgents {
		if subAgentStateNeedsPromptContext(sub.State) {
			return true
		}
	}
	return false
}

func hasActiveBackgroundObjectsForCompaction(objects []recovery.BackgroundObjectState) bool {
	for _, obj := range objects {
		if !obj.FinishedAt.IsZero() {
			continue
		}
		switch strings.TrimSpace(strings.ToLower(obj.Status)) {
		case "", "running", "pending", "queued", "started":
			return true
		case "completed", "cancelled", "failed", "exited", "done", "finished":
			continue
		default:
			return true
		}
	}
	return false
}

func hasContinuationPressureEvidence(items []evidenceItem) bool {
	for _, item := range items {
		switch item.Kind {
		case evidenceUserCorrection, evidenceToolError, evidenceEscalate:
			return true
		}
	}
	return false
}
