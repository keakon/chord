package agent

func (t *Turn) snapshotPendingToolCalls() []PendingToolCall {
	if t == nil || len(t.PendingToolMeta) == 0 {
		return nil
	}
	calls := make([]PendingToolCall, 0, len(t.PendingToolMeta))
	for _, call := range t.PendingToolMeta {
		calls = append(calls, call)
	}
	return calls
}
