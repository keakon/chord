package agent

import "github.com/keakon/chord/internal/message"

// readFileStateForTest records a read FileState from the file's current on-disk
// tracking, mirroring how a successful read captures its state.
func readFileStateForTest(path string) *message.ToolFileState {
	state := trackedExistingFileState(path)
	if state == nil {
		return nil
	}
	return &message.ToolFileState{Reads: []message.TrackedFileState{*state}}
}
