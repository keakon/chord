package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// settleCompactionDroppedMailboxRows settles the SubAgent mailbox delivery
// rows a compaction replace is about to destroy. The durable transcript row
// is the delivery evidence restore uses to distinguish "already shown to the
// model" from "still pending", so a row must not disappear while its message
// is still unconsumed:
//
//   - presented rows (a completed assistant output follows the row): the model
//     already saw and acted on the message, so a consumed ack is written before
//     the replace commits. An ack failure aborts the compaction — the
//     transcript stays intact and no delivery evidence is destroyed without
//     settlement.
//   - unpresented rows (crash leftovers, nothing follows): no ack is written —
//     the message was never shown and must not be marked consumed. The returned
//     message IDs are re-enqueued by requeueMailboxMessagesAfterCompaction once
//     the replace has committed.
//
// The presented/accepted approximation mirrors restore's durable-row boundary:
// a row counts as presented as soon as any assistant output follows it, even
// when request-level reduction later trims the exact copy the model saw.
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
// so terminal settlement guards apply identically. The replace has already
// committed, so nothing here may fail the apply: a record the reload cannot
// find stays unconsumed in the log, where a future restore replays it.
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
		a.enqueueRestoredMailboxMessage(msg)
	}
}
