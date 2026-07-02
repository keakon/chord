package llm

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

const interruptedAssistantNotice = "\n\n[Previous assistant output was interrupted before completion. If continuing, resume from this point without repeating earlier content.]"

func assistantContentForReplay(msg message.Message) string {
	if msg.StopReason == "interrupted" && strings.TrimSpace(msg.Content) != "" {
		return msg.Content + interruptedAssistantNotice
	}
	return msg.Content
}

func responseHasText(resp *message.Response) bool {
	return resp != nil && strings.TrimSpace(resp.Content) != ""
}

func markInterruptedTextResponse(resp *message.Response) *message.Response {
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return nil
	}
	resp.StopReason = "interrupted"
	resp.ToolCalls = nil
	resp.ThinkingBlocks = nil
	resp.ReasoningContent = ""
	return resp
}
