package agent

import (
	"context"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

// lspFollowupLead opens every late-diagnostics follow-up. The diagnostics are
// not in the earlier tool results, so the model is told where they came from
// and which rules apply instead of guessing.
const lspFollowupLead = "LSP diagnostics arrived after the tool results of file edits in this turn were returned (a later analysis pass of the language server). They are not in those tool results. Apply the LSP diagnostic follow-up rules to them."

// lspFollowupActivity labels the status while a turn that is about to end
// waits for its edits' analysis passes.
const lspFollowupActivity = "LSP diagnostics"

// eventLateDiagnosticsSettled resumes a turn end that waited for late LSP
// diagnostics (payload: lateTurnEnd).
const eventLateDiagnosticsSettled = "late_diagnostics_settled"

// LSPFollowupItem is one written file's late LSP diagnostics, as reported by
// the language-server layer. Ack consumes the item there once the follow-up
// carrying it is persisted, so it is never delivered twice.
type LSPFollowupItem struct {
	DisplayPath string
	Lines       string
	Reviews     []message.LSPReview
	Ack         func()
}

// LSPFollowupSource reports late LSP diagnostics for files the current turn
// wrote.
type LSPFollowupSource interface {
	// Pending returns the late diagnostics worth delivering now, without
	// consuming them.
	Pending(paths []string, displayBaseDir string) []LSPFollowupItem
	// Awaitable reports whether Await would block for paths.
	Awaitable(paths []string) bool
	// Await blocks, bounded, until the paths' analysis passes arrived.
	Await(ctx context.Context, paths []string)
}

// lspFollowup is the follow-up attached to the request being assembled.
type lspFollowup struct {
	text  string
	items []LSPFollowupItem
}

// lateTurnEnd carries a terminal response whose turn end waits for late
// diagnostics; it is finished exactly as it would have been without the wait.
type lateTurnEnd struct {
	turn        *Turn
	paths       []string
	assistant   message.Message
	payload     *LLMResponsePayload
	isTruncated bool
}

// SetLSPFollowupSource installs the source of late LSP diagnostics.
func (a *MainAgent) SetLSPFollowupSource(source LSPFollowupSource) {
	a.lspFollowupSource = source
}

// buildLSPFollowupOverlay stages and returns the turn-tail message carrying
// late diagnostics for files this turn wrote. Only the current turn counts:
// a finished turn's late diagnostics are dropped rather than waking or
// starting a turn, and other agents' writes are not in this transcript.
func (a *MainAgent) buildLSPFollowupOverlay() (message.Message, bool) {
	a.stagedLSPFollowupMu.Lock()
	a.stagedLSPFollowup = nil
	a.stagedLSPFollowupMu.Unlock()
	if a.lspFollowupSource == nil || a.ctxMgr == nil {
		return message.Message{}, false
	}
	paths := currentTurnWrittenPaths(a.ctxMgr.ScanBackward)
	if len(paths) == 0 {
		return message.Message{}, false
	}
	items := a.lspFollowupSource.Pending(paths, a.workDir())
	text := formatLSPFollowup(items)
	if text == "" {
		return message.Message{}, false
	}
	a.stagedLSPFollowupMu.Lock()
	a.stagedLSPFollowup = &lspFollowup{text: text, items: items}
	a.stagedLSPFollowupMu.Unlock()
	return message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: lspFollowupContent(text)}, true
}

// emitStagedLSPFollowup persists the follow-up the dispatched request carried
// and surfaces it as a card backed by that message. The durable row holds the
// same content the request carried, so later requests replay what the model
// already saw.
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
	for _, item := range staged.items {
		if item.Ack != nil {
			item.Ack()
		}
	}
	text := staged.text
	a.persistAsyncAfter(identity.MainAgentID, msg, func(err error) {
		if err != nil {
			a.notePersistenceFailure(err)
			return
		}
		a.emitToTUI(LSPFollowupEvent{Message: text, MessageIndex: messageIndex})
	})
}

// deferTurnEndForLateDiagnostics holds a turn that is about to end while its
// edits' analysis passes may still arrive, so findings on the last edit reach
// the model instead of being dropped with the turn. The wait runs off the
// event loop and is bounded by the source; the resume event finishes the turn
// or continues it with the follow-up attached.
func (a *MainAgent) deferTurnEndForLateDiagnostics(end lateTurnEnd) bool {
	if a.lspFollowupSource == nil || a.ctxMgr == nil || end.turn == nil || end.isTruncated {
		return false
	}
	end.paths = currentTurnWrittenPaths(a.ctxMgr.ScanBackward)
	if len(end.paths) == 0 {
		return false
	}
	if !a.lspFollowupSource.Awaitable(end.paths) && len(a.lspFollowupSource.Pending(end.paths, a.workDir())) == 0 {
		return false
	}
	a.emitActivity("main", ActivityExecuting, lspFollowupActivity)
	source := a.lspFollowupSource
	go func() {
		source.Await(end.turn.Ctx, end.paths)
		a.sendEvent(Event{Type: eventLateDiagnosticsSettled, TurnID: end.turn.ID, Payload: end})
	}()
	return true
}

// handleLateDiagnosticsSettled resumes a turn end after its wait. A turn that
// was cancelled or replaced meanwhile is left to whoever ended it.
func (a *MainAgent) handleLateDiagnosticsSettled(evt Event) {
	end, ok := evt.Payload.(lateTurnEnd)
	if !ok || end.turn == nil || a.turn != end.turn || end.turn.Ctx.Err() != nil {
		return
	}
	if len(a.lspFollowupSource.Pending(end.paths, a.workDir())) > 0 {
		log.Debugf("late LSP diagnostics continue the turn turn_id=%v paths=%v", end.turn.ID, len(end.paths))
		a.mergePendingInputsForTurnContinuation()
		a.beginMainLLMAfterPreparation(end.turn.Ctx, end.turn.ID, "")
		return
	}
	a.finishTerminalResponse(end.assistant, end.payload, end.isTruncated)
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

// currentTurnWrittenPaths lists the files successful tool results wrote since
// the latest user-authored message, newest first.
func currentTurnWrittenPaths(scan func(func(*message.Message) bool)) []string {
	var paths []string
	seen := make(map[string]struct{})
	scan(func(msg *message.Message) bool {
		if message.IsUserAuthored(*msg) || msg.IsCompactionSummary {
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
