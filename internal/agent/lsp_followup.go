package agent

import (
	"strings"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

// lspFollowupLead opens every late-diagnostics follow-up. The diagnostics are
// not in the earlier tool results, so the model is told where they came from
// and which rules apply instead of guessing.
const lspFollowupLead = "LSP diagnostics arrived after the tool results of recent file edits were returned (a later analysis pass of the language server). They are not in those tool results. Apply the LSP diagnostic follow-up rules to them."

// lspFollowupBackscanMessages bounds how far back the request assembly looks
// for recently written paths. Late watches expire on their own; this bound
// keeps an old transcript from being scanned on every request.
const lspFollowupBackscanMessages = 200

// LSPFollowupItem is one written file's late LSP diagnostics, as reported by
// the language-server layer. Ack consumes the item there once the follow-up
// carrying it is persisted, so it is never delivered twice.
type LSPFollowupItem struct {
	DisplayPath string
	Lines       string
	Reviews     []message.LSPReview
	Ack         func()
}

// LSPFollowupSource reports late LSP diagnostics for files recent turns wrote.
type LSPFollowupSource interface {
	// Pending returns the late diagnostics worth delivering now for the agent
	// whose request is being assembled, without consuming them.
	Pending(paths []string, displayBaseDir, agentID string) []LSPFollowupItem
}

// lspFollowup is the follow-up attached to the request being assembled.
type lspFollowup struct {
	text  string
	items []LSPFollowupItem
}

func (f *lspFollowup) overlay() message.Message {
	return message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: lspFollowupContent(f.text)}
}

// Reading pending diagnostics neither stages a request nor acknowledges its
// source, so budget estimation can use exactly the text dispatch will carry.
func pendingLSPFollowup(source LSPFollowupSource, scan func(func(*message.Message) bool), baseDir, agentID string) *lspFollowup {
	if source == nil {
		return nil
	}
	paths := recentWrittenPaths(scan)
	if len(paths) == 0 {
		return nil
	}
	items := source.Pending(paths, baseDir, agentID)
	text := formatLSPFollowup(items)
	if text == "" {
		return nil
	}
	return &lspFollowup{text: text, items: items}
}

// SetLSPFollowupSource installs the source of late LSP diagnostics.
func (a *MainAgent) SetLSPFollowupSource(source LSPFollowupSource) {
	a.lspFollowupSource = source
	for _, sub := range a.subs.snapshotSubAgents() {
		sub.SetLSPFollowupSource(source)
	}
}

// buildLSPFollowupOverlay stages and returns the turn-tail message carrying
// late diagnostics for recently written files. The write may belong to an
// earlier turn: analysis passes can take seconds after the tool result, and an
// idle agent is never woken for them. Only paths within the backscan window
// are considered; other agents' writes are not in this transcript.
func (a *MainAgent) buildLSPFollowupOverlay() (message.Message, bool) {
	a.stagedLSPFollowupMu.Lock()
	a.stagedLSPFollowup = nil
	a.stagedLSPFollowupMu.Unlock()
	if a.lspFollowupSource == nil || a.ctxMgr == nil {
		return message.Message{}, false
	}
	followup := pendingLSPFollowup(a.lspFollowupSource, a.ctxMgr.ScanBackward, a.workDir(), a.instanceID)
	if followup == nil {
		return message.Message{}, false
	}
	a.stagedLSPFollowupMu.Lock()
	a.stagedLSPFollowup = followup
	a.stagedLSPFollowupMu.Unlock()
	return followup.overlay(), true
}

// emitStagedLSPFollowup persists the follow-up the dispatched request carried
// and surfaces it as a card backed by that message. The durable row holds the
// same content the request carried, so later requests replay what the model
// already saw. The model has already received the text with the dispatched
// request, so the source is acknowledged as soon as the row is appended; a
// persistence failure defers only the card until recovery, not the content.
func (a *MainAgent) emitStagedLSPFollowup() {
	if a == nil || a.ctxMgr == nil {
		return
	}
	a.stagedLSPFollowupMu.Lock()
	staged := a.stagedLSPFollowup
	a.stagedLSPFollowup = nil
	a.stagedLSPFollowupMu.Unlock()
	if staged == nil {
		return
	}
	var reviews []message.LSPReview
	for _, item := range staged.items {
		reviews = append(reviews, item.Reviews...)
	}
	msg := message.Message{Role: message.RoleUser, Kind: message.KindLSPFollowup, Content: lspFollowupContent(staged.text), LSPReviews: reviews}
	messageIndex := a.ctxMgr.MessageCount()
	a.ctxMgr.Append(msg)
	ackLSPFollowupItems(staged.items)
	text := staged.text
	enqueued := a.persistAsyncAfter(identity.MainAgentID, msg, func(err error) {
		if err != nil {
			a.noteOverlayAppendPersistFailure(msg, identity.MainAgentID, messageIndex)
			a.notePersistenceFailure(err)
			return
		}
		a.emitToTUI(LSPFollowupEvent{Message: text, MessageIndex: messageIndex})
	})
	if !enqueued {
		// No persistence channel: the in-memory row remains the request's
		// record, so emit the card now instead of leaving it pending forever.
		a.emitToTUI(LSPFollowupEvent{Message: text, MessageIndex: messageIndex})
	}
}

func ackLSPFollowupItems(items []LSPFollowupItem) {
	for _, item := range items {
		if item.Ack != nil {
			item.Ack()
		}
	}
}

func lspFollowupContent(text string) string {
	return "<system-reminder>\n" + text + "\n</system-reminder>"
}

func formatLSPFollowup(items []LSPFollowupItem) string {
	var b strings.Builder
	for _, item := range items {
		lines := strings.TrimSpace(item.Lines)
		if lines == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString(lspFollowupLead)
		}
		b.WriteString("\n\n")
		b.WriteString(item.DisplayPath)
		b.WriteString(":\n")
		b.WriteString(lines)
	}
	return b.String()
}

// recentWrittenPaths lists files successful tool results wrote in the recent
// transcript, newest first. It crosses user turn boundaries because a late
// analysis pass may only arrive after the turn that caused it ended, but stops
// at a compaction summary and at a fixed message budget.
func recentWrittenPaths(scan func(func(*message.Message) bool)) []string {
	var paths []string
	seen := make(map[string]struct{})
	scanned := 0
	scan(func(msg *message.Message) bool {
		scanned++
		if scanned > lspFollowupBackscanMessages || msg.IsCompactionSummary {
			return true
		}
		if msg.Role != message.RoleTool || msg.FileState == nil {
			return false
		}
		for _, write := range msg.FileState.Writes {
			path := strings.TrimSpace(write.Path)
			if path == "" {
				continue
			}
			if _, dup := seen[path]; dup {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
		return false
	})
	return paths
}
