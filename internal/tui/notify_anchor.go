package tui

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// notifyAnchor records where a live sub-agent notify card was shown relative
// to the durable transcript, so a restored session can replay the cards the
// live run displayed but that never reached the persisted transcript.
//
// The durable transcript (main.jsonl) carries only a subset of notify events:
// progress is delivered as a per-agent latest snapshot and any entry still in
// the in-memory owner inbox when the session ends is never persisted, and very
// long sessions additionally fold the mailbox log. The live TUI, by contrast,
// shows every AgentNotifyEvent. Without this record a restored session silently
// drops those cards and renumbers everything that follows, which is exactly the
// "#106 vs #45" display-sequence drift reported by the user.
//
// anchorMsgCount is the length of the transcript the card's target view is
// rendered from, captured when the live card was shown: the count of
// transcript messages that already existed, i.e. the card belongs after
// message index anchorMsgCount-1 of that transcript. Main-targeted cards
// interleave into the main transcript, SubAgent-targeted cards into that
// agent's conversation; anchor positions are meaningless across transcripts,
// so a replay only merges the anchors matching its view. main.jsonl carries no
// timestamps, so this count is the only stable positional anchor available.
type notifyAnchor struct {
	Seq            int    `json:"seq"`
	AnchorMsgCount int    `json:"anchor_msg_count"`
	MessageID      string `json:"message_id,omitempty"`
	AgentID        string `json:"agent_id"`
	TaskID         string `json:"task_id,omitempty"`
	Kind           string `json:"kind"`
	Subtype        string `json:"subtype,omitempty"`
	Content        string `json:"content"`
	TargetAgentID  string `json:"target_agent_id,omitempty"`
	ParentAgentID  string `json:"parent_agent_id,omitempty"`
}

const notifyAnchorFileName = "tui_notify_anchors.jsonl"

// notifyAnchorPath returns the per-session log the TUI appends live notify
// anchors to, or "" when the agent does not expose a session directory (e.g.
// headless modes without a persisted session).
func (m *Model) notifyAnchorPath() string {
	if m.agent == nil {
		return ""
	}
	provider, ok := m.agent.(interface{ SessionDir() string })
	if !ok {
		return ""
	}
	dir := strings.TrimSpace(provider.SessionDir())
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, notifyAnchorFileName)
}

// notifyTargetMessages returns the transcript a notify card's anchor position
// is measured against: the conversation of the card's resolved target view,
// not the focused one. GetMessages would return the focused agent's history
// here, but the live card belongs to the target agent's view — a viewer
// watching a SubAgent must not make a main-targeted card's position relative
// to the SubAgent transcript.
func (m *Model) notifyTargetMessages(targetAgentID string) []message.Message {
	targeted, ok := m.agent.(agent.TargetedConversationController)
	if !ok {
		return nil
	}
	return targeted.GetMessagesForTarget(agent.ConversationTarget{AgentID: targetAgentID})
}

// recordNotifyAnchor appends a single live notify's positional anchor to the
// session log. It is called from the AgentNotifyEvent handler for every notify
// the live TUI renders, including the ones that will never be persisted. The
// log is the sole source of truth on restore; it is written live-only because
// the restore path renders delivered cards from the transcript and replays the
// rest from this file, so it must never be polluted by restore-time re-emits.
func (m *Model) recordNotifyAnchor(evt agent.AgentNotifyEvent) {
	path := m.notifyAnchorPath()
	if path == "" {
		return
	}
	target := resolveNotifyTargetAgentID(evt.TargetAgentID, evt.ParentAgentID)
	msgs := m.notifyTargetMessages(target)
	if msgs == nil {
		log.Debugf("notify anchor skipped: target transcript unavailable target=%v", target)
		return
	}
	entry := notifyAnchor{
		Seq:            m.nextNotifyAnchorSeq,
		AnchorMsgCount: len(msgs),
		MessageID:      strings.TrimSpace(evt.MessageID),
		AgentID:        strings.TrimSpace(evt.AgentID),
		TaskID:         strings.TrimSpace(evt.TaskID),
		Kind:           strings.TrimSpace(evt.Kind),
		Subtype:        strings.TrimSpace(evt.Subtype),
		Content:        strings.TrimSpace(evt.Message),
		TargetAgentID:  strings.TrimSpace(evt.TargetAgentID),
		ParentAgentID:  strings.TrimSpace(evt.ParentAgentID),
	}
	m.nextNotifyAnchorSeq++
	data, err := json.Marshal(entry)
	if err != nil {
		log.Debugf("marshal notify anchor failed seq=%v err=%v", entry.Seq, err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Debugf("open notify anchor log failed path=%v err=%v", path, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		log.Debugf("write notify anchor failed seq=%v err=%v", entry.Seq, err)
		return
	}
	_, _ = f.Write([]byte("\n"))
}

// readNotifyAnchors loads the recorded live notify anchors from disk. A missing
// file is not an error: it simply means no anchors were recorded (e.g. a fresh
// session or a headless run), and the restore falls back to the delivered
// transcript cards alone.
func readNotifyAnchors(path string) ([]notifyAnchor, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []notifyAnchor
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var a notifyAnchor
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			continue // skip corrupt lines; one bad row must not break restore
		}
		if a.Content == "" && a.AgentID == "" && a.Kind == "" {
			continue
		}
		out = append(out, a)
	}
	return out, sc.Err()
}

// deliveredNotifyMessageIDs returns the set of mailbox message ids that the
// durable transcript already renders as cards. Anchors whose message id is in
// this set are skipped on replay so delivered cards are not duplicated.
func deliveredNotifyMessageIDs(msgs []message.Message) map[string]struct{} {
	set := make(map[string]struct{})
	for _, msg := range msgs {
		if msg.Role == "user" && msg.Kind == message.KindSubAgentMailbox && msg.Mailbox != nil {
			if id := strings.TrimSpace(msg.Mailbox.MessageID); id != "" {
				set[id] = struct{}{}
			}
		}
	}
	return set
}

// resolveNotifyTargetAgentID mirrors the live AgentNotifyEvent handler's
// target-resolution rule so a replayed card is badged identically to the live
// one it stands in for.
func resolveNotifyTargetAgentID(targetAgentID, parentAgentID string) string {
	targetAgentID = strings.TrimSpace(targetAgentID)
	if targetAgentID == "main" {
		targetAgentID = ""
	}
	if targetAgentID == "" {
		parentAgentID = strings.TrimSpace(parentAgentID)
		if parentAgentID != "" && parentAgentID != "main" {
			targetAgentID = parentAgentID
		}
	}
	return targetAgentID
}

// mergeNotifyAnchors inserts replayed notify cards into the rebuilt block list
// at their true transcript positions.
//
// Each base block carries MsgIndex (the source message index); rebuilt blocks
// all set it, so the position of a notify with anchor A is immediately before
// the first base block whose MsgIndex >= A. Notifies emitted in the same gap
// (same anchor) keep their emission order via Seq. Anchors whose message id is
// already delivered are dropped to avoid duplicating transcript-rendered cards.
func mergeNotifyAnchors(base []*Block, anchors []notifyAnchor, delivered map[string]struct{}, nextID *int) []*Block {
	if len(anchors) == 0 {
		return base
	}
	entries := make([]notifyAnchor, 0, len(anchors))
	for _, a := range anchors {
		if strings.TrimSpace(a.MessageID) != "" {
			if _, ok := delivered[a.MessageID]; ok {
				continue // already rendered from the durable transcript
			}
		}
		entries = append(entries, a)
	}
	if len(entries) == 0 {
		return base
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].AnchorMsgCount != entries[j].AnchorMsgCount {
			return entries[i].AnchorMsgCount < entries[j].AnchorMsgCount
		}
		return entries[i].Seq < entries[j].Seq
	})

	result := make([]*Block, 0, len(base)+len(entries))
	ei := 0
	for _, b := range base {
		m := effectiveAnchorMsgIndex(b)
		for ei < len(entries) && entries[ei].AnchorMsgCount <= m {
			target := resolveNotifyTargetAgentID(entries[ei].TargetAgentID, entries[ei].ParentAgentID)
			result = append(result, newSubAgentMailboxBlock(*nextID, entries[ei].Kind, entries[ei].Subtype, entries[ei].AgentID, entries[ei].TaskID, entries[ei].Content, target))
			*nextID++
			ei++
		}
		result = append(result, b)
	}
	for ; ei < len(entries); ei++ {
		target := resolveNotifyTargetAgentID(entries[ei].TargetAgentID, entries[ei].ParentAgentID)
		result = append(result, newSubAgentMailboxBlock(*nextID, entries[ei].Kind, entries[ei].Subtype, entries[ei].AgentID, entries[ei].TaskID, entries[ei].Content, target))
		*nextID++
	}
	return result
}

// effectiveAnchorMsgIndex returns the message index a block represents for
// anchor-placement purposes. Compaction-summary blocks use MsgIndex -1 and sit
// at the very front, so a non-negative anchor never inserts before them.
func effectiveAnchorMsgIndex(b *Block) int {
	if b == nil {
		return 0
	}
	return b.MsgIndex
}

// notifyAnchorsForView keeps the anchors whose card belongs to the view that
// is replaying them: "" (main) keeps main-targeted cards; a SubAgent instance
// ID keeps the cards addressed to that agent. A card's position is relative to
// the transcript its view is rendered from, so an anchor pointing at another
// view's transcript cannot be mapped onto this block list.
func notifyAnchorsForView(anchors []notifyAnchor, viewAgentID string) []notifyAnchor {
	out := make([]notifyAnchor, 0, len(anchors))
	for _, a := range anchors {
		if resolveNotifyTargetAgentID(a.TargetAgentID, a.ParentAgentID) == viewAgentID {
			out = append(out, a)
		}
	}
	return out
}

// adoptNotifyAnchorSeqs raises the seq counter past every loaded entry, so
// anchors recorded after a restart keep their emission order among cards
// sharing an anchor instead of sorting onto older entries.
func (m *Model) adoptNotifyAnchorSeqs(anchors []notifyAnchor) {
	for _, a := range anchors {
		if a.Seq >= m.nextNotifyAnchorSeq {
			m.nextNotifyAnchorSeq = a.Seq + 1
		}
	}
}

// replayNotifyAnchorsForView reads the live notify anchor log (when present)
// and merges the undelivered notify cards for one view into the rebuilt block
// list. It only pays off on rebuilds that dropped the live cards: a session
// restore and a focus switch rebuild the base from the durable transcript, so
// the live-only cards would vanish without a replay; a live rebuild keeps the
// viewport's own copy and must not replay.
//
// viewAgentID is the view the merge belongs to, "" (main) or the focused
// SubAgent instance ID; only its own anchors merge here.
func (m *Model) replayNotifyAnchorsForView(base []*Block, msgs []message.Message, viewAgentID string) []*Block {
	path := m.notifyAnchorPath()
	if path == "" {
		return base
	}
	anchors, err := readNotifyAnchors(path)
	if err != nil || len(anchors) == 0 {
		if err != nil {
			log.Debugf("read notify anchors failed path=%v err=%v", path, err)
		}
		return base
	}
	m.adoptNotifyAnchorSeqs(anchors)
	anchors = notifyAnchorsForView(anchors, viewAgentID)
	if len(anchors) == 0 {
		return base
	}
	delivered := deliveredNotifyMessageIDs(msgs)
	nextID := m.nextBlockID
	merged := mergeNotifyAnchors(base, anchors, delivered, &nextID)
	m.nextBlockID = nextID
	return merged
}

// clearCompactedNotifyAnchors drops the anchors whose positions pointed into
// the main transcript before a durable compaction rewrote it: archived history
// took the live-only cards' positions with it and the compacted transcript
// lines up with none of the recorded counts. Anchors recorded for other views'
// transcripts (SubAgent conversations) were not rewritten by the compaction and
// keep their positions.
func (m *Model) clearCompactedNotifyAnchors() {
	path := m.notifyAnchorPath()
	if path == "" {
		return
	}
	anchors, err := readNotifyAnchors(path)
	if err != nil {
		log.Debugf("read notify anchors for compaction clear failed path=%v err=%v", path, err)
		return
	}
	kept := make([]notifyAnchor, 0, len(anchors))
	for _, a := range anchors {
		if resolveNotifyTargetAgentID(a.TargetAgentID, a.ParentAgentID) != "" {
			kept = append(kept, a)
		}
	}
	if len(kept) == len(anchors) {
		return
	}
	if err := writeNotifyAnchors(path, kept); err != nil {
		log.Debugf("rewrite notify anchors after compaction failed path=%v err=%v", path, err)
		// A failed rewrite would leave stale main anchors that replay onto the
		// compacted transcript; dropping the log degrades to live-only cards
		// being lost instead of mispositioned cards appearing.
		if truncErr := os.WriteFile(path, nil, 0o644); truncErr != nil {
			log.Debugf("truncate notify anchors after failed rewrite path=%v err=%v", path, truncErr)
		}
	}
}

// writeNotifyAnchors atomically replaces the anchor log with the given entries.
func writeNotifyAnchors(path string, anchors []notifyAnchor) error {
	data := make([]byte, 0, len(anchors)*160)
	for _, a := range anchors {
		line, err := json.Marshal(a)
		if err != nil {
			return err
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
