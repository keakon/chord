package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

// prepareContextContinuation runs on the owning agent's event loop, before a
// new turn. The selected assistant can precede local facts; remove only its
// conversational payload, after the durable rewrite succeeds.
func prepareContextContinuation(manager *ctxmgr.Manager, flush func(), rewrite func([]message.Message) error) error {
	messages := manager.Snapshot()
	index := message.LastConversationMessageIndex(messages)
	if index < 0 {
		return nil
	}
	last := messages[index]
	if last.Role != message.RoleAssistant || len(last.ThinkingBlocks) == 0 ||
		strings.TrimSpace(last.Content) != "" || len(last.ToolCalls) > 0 || len(last.Parts) > 0 {
		return nil
	}
	prefix := messages[:index]
	if len(last.Question) > 0 {
		prefix = append(prefix, message.Message{
			Role: message.RoleSystem, Kind: message.KindQuestionState,
			Question: last.Question, RequestBatch: last.RequestBatch,
		})
	}
	if flush != nil {
		flush()
	}
	return manager.ReplacePrefixAtomic(index+1, prefix, func(tail []message.Message) ([]message.Message, error) {
		remaining := make([]message.Message, 0, len(prefix)+len(tail))
		remaining = append(remaining, prefix...)
		remaining = append(remaining, tail...)
		if err := rewrite(remaining); err != nil {
			return nil, fmt.Errorf("rewrite continuation transcript: %w", err)
		}
		return remaining, nil
	})
}

func (a *MainAgent) prepareContextContinuation() error {
	return prepareContextContinuation(a.ctxMgr, a.flushPersist, func(messages []message.Message) error {
		if a.questions.failed || a.questions.active != nil {
			return errQuestionHistoryRewriteUnsettled
		}
		if manager := a.recoveryManager(); manager != nil {
			return manager.RewriteLog(identity.MainAgentID, messages)
		}
		return nil
	})
}

func (s *SubAgent) prepareContextContinuation() error {
	var flush func()
	if s.parent != nil {
		flush = s.parent.flushPersist
	}
	return prepareContextContinuation(s.ctxMgr, flush, func(messages []message.Message) error {
		if manager := s.recoveryManager(); manager != nil {
			return manager.RewriteLog(s.instanceID, messages)
		}
		return nil
	})
}
