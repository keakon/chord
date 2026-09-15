package agent

import "maps"

func retireCheckpointItems(state checkpointTypedState, retired []string) checkpointTypedState {
	if len(retired) == 0 {
		return state
	}
	state.Completed = removeCheckpointItems(state.Completed, retired)
	state.Decisions = removeCheckpointItems(state.Decisions, retired)
	state.OpenIssues = removeCheckpointItems(state.OpenIssues, retired)
	state.Claims = maps.Clone(state.Claims)
	for _, item := range retired {
		delete(state.Claims, checkpointItemKey(item))
	}
	return state
}
