package agent

import (
	"strings"
)

type llmModelContinuitySnapshot struct {
	PreviousModel           string
	CurrentModel            string
	ProjectedModelRunLength int
}

func (a *MainAgent) llmModelContinuitySnapshot() llmModelContinuitySnapshot {
	if a == nil {
		return llmModelContinuitySnapshot{}
	}
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	current := a.runningModelRef
	if current == "" {
		current = a.providerModelRef
	}
	snapshot := llmModelContinuitySnapshot{
		PreviousModel: a.previousLLMModelRef,
		CurrentModel:  current,
	}
	if current == "" {
		return snapshot
	}
	if a.lastLLMRequestModelRef != current {
		snapshot.ProjectedModelRunLength = 1
		return snapshot
	}
	if a.llmModelRunLength <= 0 {
		snapshot.ProjectedModelRunLength = 1
		return snapshot
	}
	snapshot.ProjectedModelRunLength = a.llmModelRunLength + 1
	return snapshot
}

func (a *MainAgent) recordLLMModelRun(ref string) {
	if a == nil {
		return
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	a.llmMu.Lock()
	defer a.llmMu.Unlock()
	if a.lastLLMRequestModelRef == ref {
		a.llmModelRunLength++
	} else {
		a.lastLLMRequestModelRef = ref
		a.llmModelRunLength = 1
	}
}

func (a *MainAgent) resetLLMModelRun() {
	if a == nil {
		return
	}
	a.llmMu.Lock()
	defer a.llmMu.Unlock()
	a.lastLLMRequestModelRef = ""
	a.llmModelRunLength = 0
}

// modelChangedSinceLastPreparedRequest reports whether the running model
// differs from the one used by the previous prepared LLM request. The stable
// reduction surface was produced under the previous model's budget, so a change
// here means it must not be reused: the new model is entitled to its own
// reduction. Returns false when there was no previous prepared request (fresh
// session), since there is nothing to invalidate.
func (a *MainAgent) modelChangedSinceLastPreparedRequest() bool {
	if a == nil {
		return false
	}
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	if a.lastLLMRequestModelRef == "" {
		return false
	}
	current := a.runningModelRef
	if current == "" {
		current = a.providerModelRef
	}
	return current != a.lastLLMRequestModelRef
}
