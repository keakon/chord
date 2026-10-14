package agent

import (
	"context"
	"fmt"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

type countingCompactionProvider struct {
	keys          []string
	models        []string
	calls         int
	invalidations []string
	tunings       []llm.RequestTuning
	progress      []message.StreamProgressDelta
	response      *message.Response
	responses     []*message.Response
	err           error
}

func (p *countingCompactionProvider) CompleteStream(
	_ context.Context,
	key string,
	model string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	tuning llm.RequestTuning,
	cb llm.StreamCallback,
) (*message.Response, error) {
	p.calls++
	p.keys = append(p.keys, key)
	p.models = append(p.models, model)
	p.tunings = append(p.tunings, tuning)
	for _, progress := range p.progress {
		if cb != nil {
			cb(message.StreamDelta{Progress: &progress})
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	if len(p.responses) >= p.calls {
		return p.responses[p.calls-1], nil
	}
	if p.response != nil {
		return p.response, nil
	}
	return &message.Response{}, nil
}

func (p *countingCompactionProvider) Complete(
	_ context.Context,
	key string,
	model string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
) (*message.Response, error) {
	p.calls++
	p.keys = append(p.keys, key)
	p.models = append(p.models, model)
	if p.err != nil {
		return nil, p.err
	}
	if len(p.responses) >= p.calls {
		return p.responses[p.calls-1], nil
	}
	if p.response != nil {
		return p.response, nil
	}
	return &message.Response{}, nil
}

func (p *countingCompactionProvider) InvalidateRouting(reason string) {
	p.invalidations = append(p.invalidations, reason)
}

func validCompactionSummaryForTest(history string) string {
	return fmt.Sprintf(
		"## Current User Request\n- continue current task\n\n## Active Objective\n- continue current task\n\n## Background Goals\n- none\n\n## User Constraints\n- none\n\n## Progress\n- progress recorded\n\n## Key Decisions\n- decisions captured\n\n## Files and Evidence\n- Archived history: %s\n- src/current_task.go\n\n## Todo State\n- Active/relevant to latest request: (none)\n- Completed/background: (none)\n- Stale/superseded: (none)\n\n## SubAgent State\n- none active\n\n## Open Problems\n- none\n\n## Next Step\n- Inspect src/current_task.go and continue the current task.",
		history,
	)
}

// summarizeCompactionHeadForTest invokes summarizeCompactionHead with the
// continuation profile defaults previously baked into the deleted 2-arg wrapper.
func summarizeCompactionHeadForTest(a *MainAgent, head []message.Message, historyPath string) (summary string, modelRef string, err error) {
	summary, modelRef, err = a.summarizeCompactionHead(context.Background(), head, historyPath, nil, nil, a.GetTodos(), a.taskInfosForCompaction(), jobStatesForSnapshot(), compactionAnchors{})
	return summary, modelRef, err
}

// splitMessagesForCompactionForTest validates compaction partitioning without
// going through the full MainAgent setup.
func splitMessagesForCompactionForTest(messages []message.Message, contextLimit int) (head []message.Message, evidence []message.Message) {
	a := &MainAgent{}
	a.resetRuntimeEvidenceFromMessages(messages)
	recentTail := selectRecentTailMessages(nil, messages, compactRecentTailTurns, recentTailTokenBudget(contextLimit))
	evidenceItems := a.evidenceItemsForCompaction(contextLimit)
	return splitMessagesForCompactionWithSelections(messages, recentTail, evidenceItems)
}

// splitMessagesForCompactionForTestWithAgent uses an explicit MainAgent so tests
// can inspect runtime evidence accumulation across calls.
func splitMessagesForCompactionForTestWithAgent(a *MainAgent, messages []message.Message, contextLimit int) (head []message.Message, evidence []message.Message) {
	recentTail := selectRecentTailMessages(nil, messages, compactRecentTailTurns, recentTailTokenBudget(contextLimit))
	items := a.evidenceItemsForCompaction(contextLimit)
	return splitMessagesForCompactionWithSelections(messages, recentTail, items)
}
