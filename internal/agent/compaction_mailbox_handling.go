package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// settleCompactionDroppedMailboxRows settles the SubAgent mailbox delivery
// rows a compaction replace is about to destroy. Restore and this settle step
// judge the same durable row with two different, complementary standards —
// they are deliberately not one mirrored boundary:
//
//   - Restore (restoreSessionState) treats the row's mere presence in the
//     durable transcript as proof that a dispatch reached the main
//     conversation, and conservatively skips re-delivery: replaying inside
//     the at-least-once window could make the model act on the same message
//     twice, so the row keeps the decision pending for a later stage that can
//     still see it.
//   - This settle step is that later stage, and it runs when the row is about
//     to be destroyed — the last stop that can still tell delivery from loss.
//     Its standard is model presentation, judged from the transcript rather
//     than from the row's existence: a row counts as presented as soon as any
//     assistant output follows it anywhere in the transcript, even when that
//     output belongs to a later round or request-level reduction later trims
//     the exact copy the model saw.
//
// Rows with assistant output after them were demonstrably shown to the model,
// so a consumed ack is written before the replace commits; an ack failure
// aborts the compaction, leaving the transcript (and the row) intact for a
// retry. A row with no assistant output after it can only be a crash leftover:
// a normal dispatch is followed by its round's model output and is acked at
// turn teardown, so an unconsumed row with nothing after it is the tail of a
// round that died before it produced durable model output. Such a row is not
// acked — the message must reach a later dispatch instead of being marked
// consumed on a delivery that never demonstrably happened. Replaying may
// re-show a message the model already saw (the crash lost its earlier
// output), but a repeat is preferred over a lost notification, so the replay
// runs only after the replace commits and reloads the log record
// (requeueMailboxMessagesAfterCompaction).
func (a *MainAgent) settleCompactionDroppedMailboxRows(current []message.Message, headSplit int) (replayIDs []string, err error) {
	if a == nil || headSplit <= 0 || headSplit > len(current) {
		return nil, nil
	}
	assistantAfter := false
	for i := len(current) - 1; i >= 0; i-- {
		msg := current[i]
		if msg.Role == message.RoleAssistant {
			assistantAfter = true
			continue
		}
		if i >= headSplit || msg.Kind != message.KindSubAgentMailbox || msg.Mailbox == nil {
			continue
		}
		messageID := strings.TrimSpace(msg.Mailbox.MessageID)
		if messageID == "" || a.isSubAgentMailboxConsumed(messageID) {
			continue
		}
		if assistantAfter {
			if err := a.markSubAgentMailboxConsumed(messageID); err != nil {
				return nil, fmt.Errorf("ack delivered mailbox %s before compaction drops its transcript row: %w", messageID, err)
			}
			continue
		}
		replayIDs = append(replayIDs, messageID)
	}
	return replayIDs, nil
}

// requeueMailboxMessagesAfterCompaction re-enqueues mailbox messages whose
// only durable delivery evidence (their transcript row) was just destroyed by
// a committed compaction replace. They were never presented to the model, so
// they must reach a later dispatch instead of lingering unconsumed until the
// next restore. Records are reloaded from the mailbox log and delivered
// through the same path restore replay uses (enqueueRestoredMailboxMessage),
// so terminal settlement guards apply identically. A message already staged
// for delivery in this session (queued for the main inbox, waiting in the
// pending batch, active, or parked under an owner) is skipped: re-enqueueing
// it would queue a second copy of the same durable record. The replace has
// already committed, so nothing here may fail the apply: a record the reload
// cannot find stays unconsumed in the log, where a future restore replays it.
func (a *MainAgent) requeueMailboxMessagesAfterCompaction(replayIDs []string) {
	if a == nil || len(replayIDs) == 0 {
		return
	}
	msgs, err := loadSubAgentMailboxMessages(strings.TrimSpace(a.sessionDir))
	if err != nil {
		log.Errorf("reload mailbox log after compaction dropped transcript rows: %v", err)
		return
	}
	byID := make(map[string]SubAgentMailboxMessage, len(msgs))
	for _, msg := range msgs {
		if id := strings.TrimSpace(msg.MessageID); id != "" {
			byID[id] = msg
		}
	}
	for _, id := range replayIDs {
		msg, ok := byID[id]
		if !ok {
			log.Warnf("compaction dropped the transcript row of mailbox %s but the mailbox log holds no record; the message stays unconsumed for a future restore", id)
			continue
		}
		// The compaction ordering guarantees the drained inbox held no replay
		// id when settle ran (a replay id's row was never dispatched), but a
		// duplicate event can still have staged the same durable record while
		// its row existed, so requeue keeps the same hasQueuedMailboxMessage
		// idempotency guard the event path uses instead of queuing a second
		// copy of that record.
		if a.hasQueuedMailboxMessage(id) {
			continue
		}
		a.enqueueRestoredMailboxMessage(msg)
	}
}
