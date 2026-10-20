package agent

import (
	"encoding/json"

	"github.com/keakon/chord/internal/agent/agentdiff"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

// externalToolPreWrite is the pre-edit snapshot of a file-editing tool call: the
// chord-shaped call plus the target file's content before the external CLI
// applies the change. The external CLI never emits Chord tool results, so the
// adapter captures state at tool_use and turns it into a real unified diff at
// tool_result through the shared agentdiff helpers — giving Edit/Write cards the
// same hunks and real line numbers Chord's own edit/write tools show.
type externalToolPreWrite struct {
	Name    string
	Args    json.RawMessage
	Path    string
	Content string
	Existed bool
}

func (w *externalWorker) captureToolPreWrite(id, name string, args json.RawMessage) {
	if name != toolname.Edit && name != toolname.Write {
		return
	}
	tc := message.ToolCall{Name: name, Args: args}
	path, content, existed := agentdiff.CapturePreWriteState(tc, w.workDir)
	if path == "" {
		return
	}
	w.toolMu.Lock()
	w.toolPreWrites[id] = externalToolPreWrite{Name: name, Args: args, Path: path, Content: content, Existed: existed}
	w.toolMu.Unlock()
}

func (w *externalWorker) buildToolDiff(id string) agentdiff.Summary {
	w.toolMu.Lock()
	pre, ok := w.toolPreWrites[id]
	delete(w.toolPreWrites, id)
	w.toolMu.Unlock()
	if !ok {
		return agentdiff.Summary{}
	}
	return agentdiff.GenerateToolDiff(message.ToolCall{Name: pre.Name, Args: pre.Args}, pre.Content, pre.Path, pre.Existed)
}
