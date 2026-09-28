package agent

import "maps"

// retireCheckpointItems applies the explicit retirement list of a fresh
// submission to the carried typed state, so an item the model declares
// finished (or explicitly withdrawn) never travels into the next generation.
//
// Open issues are matched on openIssueKey rather than the exact-after-trim
// checkpointItemKey: the retirement has to reach an entry the model re-spaced
// or re-cased when it restated it, otherwise the entry would survive in the
// historical bucket the model believes it removed. The other lists keep the
// existing exact-after-trim contract — a claim is a map key, and the
// decisions/completed retirement behavior is unchanged.
func retireCheckpointItems(state checkpointTypedState, retired []string) checkpointTypedState {
	if len(retired) == 0 {
		return state
	}
	state.Completed = removeCheckpointItems(state.Completed, retired)
	state.Decisions = removeCheckpointItems(state.Decisions, retired)
	state.OpenIssues = retireByOpenIssueKey(state.OpenIssues, retired, func(item string) string { return item }, nil)
	state.CarriedOpenIssues = retireByOpenIssueKey(state.CarriedOpenIssues, retired, func(item checkpointOpenIssue) string { return item.Text }, func(item checkpointOpenIssue) string {
		if item.ID != "" {
			return item.ID
		}
		return openIssueID(item.Text)
	})
	state.Claims = maps.Clone(state.Claims)
	for _, item := range retired {
		delete(state.Claims, checkpointItemKey(item))
	}
	return state
}

// retireByOpenIssueKey removes every entry of items whose open-issue identity
// appears in retired. The identity of both sides is openIssueKey, so the
// retirement list and the entry it targets are compared under the same
// lexical normalization.
func retireByOpenIssueKey[T any](items []T, retired []string, text func(T) string, id func(T) string) []T {
	if len(items) == 0 || len(retired) == 0 {
		return items
	}
	excluded := make(map[string]struct{}, len(retired))
	for _, item := range retired {
		if key := openIssueKey(item); key != "" {
			excluded[key] = struct{}{}
		}
		if key := openIssueID(item); key != "" {
			excluded[key] = struct{}{}
		}
	}
	kept := make([]T, 0, len(items))
	for _, item := range items {
		key := openIssueKey(text(item))
		if _, found := excluded[key]; found {
			continue
		}
		if id != nil {
			if itemID := id(item); itemID != "" {
				if _, found := excluded[itemID]; found {
					continue
				}
			}
		}
		kept = append(kept, item)
	}
	return kept
}
