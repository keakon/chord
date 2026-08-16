package agent

import "github.com/keakon/chord/internal/message"

func reusableMessagePrefixLen(previous, current []message.Message) int {
	limit := min(len(previous), len(current))
	reusable := 0
	for reusable < limit && stableReductionMessageEquivalent(&previous[reusable], &current[reusable]) {
		reusable++
	}
	return reusable
}
