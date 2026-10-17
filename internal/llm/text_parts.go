package llm

import "strings"

// emptyWireTextPlaceholder labels a message whose history carries no text the
// wire shape can express. Every provider rejects a text block or text part with
// no text, so an empty value fails the whole request instead of degrading to an
// empty message.
const emptyWireTextPlaceholder = "[no text content]"

// wireText keeps a converted text value non-empty. History legitimately holds
// empty text — a tool that printed nothing, an imported result whose source
// output was empty, a message whose only attachment could not be resolved — and
// no provider accepts a text block without text.
func wireText(text string) string {
	if text == "" {
		return emptyWireTextPlaceholder
	}
	return text
}

// joinAdjacentPartText inserts a newline between separately-authored text
// parts only when neither side already contributes one.
func joinAdjacentPartText(prev, text string) string {
	if prev == "" {
		return text
	}
	if text == "" {
		return prev
	}
	if !strings.HasSuffix(prev, "\n") && !strings.HasPrefix(text, "\n") {
		prev += "\n"
	}
	return prev + text
}
