package agent

import "github.com/keakon/chord/internal/message"

// injectCompactionFileContextForTest runs the injection for the agent's
// current model ref and reports where the first snapshot landed, or -1 when
// nothing was injected.
func injectCompactionFileContextForTest(a *MainAgent, messages []message.Message) ([]message.Message, int) {
	_, modelRef := a.mainLLMAndRef()
	out := a.injectCompactionFileContext(messages, modelRef)
	if len(out) == len(messages) {
		return out, -1
	}
	for i, m := range out {
		if isCompactionFileSnapshot(m) {
			return out, i
		}
	}
	return out, -1
}
