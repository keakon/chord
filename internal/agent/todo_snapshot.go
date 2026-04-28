package agent

import (
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func snapshotTodos(items []tools.TodoItem) []recovery.TodoState {
	return recovery.SnapshotTodoStates(items)
}

func restoreSnapshotTodos(states []recovery.TodoState) []tools.TodoItem {
	return recovery.RestoreTodoItems(states)
}
