package agent

import "maps"

// retireCheckpointItems applies the explicit retirement list of a fresh
// submission to the carried typed state, so an item the model declares
// finished (or explicitly withdrawn) never travels into the next generation.
//
// Every carried list, including both open-issue buckets, matches a retirement
// entry by checkpointItemKey: only surrounding whitespace is insignificant.
func retireCheckpointItems(state checkpointTypedState, retired []string) checkpointTypedState {
	if len(retired) == 0 {
		return state
	}
	state.Completed = removeCheckpointItems(state.Completed, retired)
	state.Decisions = removeCheckpointItems(state.Decisions, retired)
	state.OpenIssues = removeCheckpointItems(state.OpenIssues, retired)
	state.CarriedOpenIssues = removeCheckpointItems(state.CarriedOpenIssues, retired)
	state.Claims = maps.Clone(state.Claims)
	for _, item := range retired {
		delete(state.Claims, checkpointItemKey(item))
	}
	return state
}
