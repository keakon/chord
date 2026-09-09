package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func anchorBlock(id, msgIndex int) *Block {
	return &Block{ID: id, Type: BlockUser, MsgIndex: msgIndex}
}

func TestMergeNotifyAnchorsOrderingAndSameAnchor(t *testing.T) {
	// Base blocks mirror a transcript: user(0), assistant text(1), tool(1),
	// user(2), assistant(3).
	base := []*Block{
		anchorBlock(0, 0),
		anchorBlock(1, 1),
		anchorBlock(2, 1),
		anchorBlock(3, 2),
		anchorBlock(4, 3),
	}
	anchors := []notifyAnchor{
		{Seq: 1, AnchorMsgCount: 1, AgentID: "a", Kind: "progress", Content: "A"},  // before msg 1
		{Seq: 2, AnchorMsgCount: 2, AgentID: "b", Kind: "progress", Content: "B"},  // before msg 2
		{Seq: 3, AnchorMsgCount: 2, AgentID: "c", Kind: "progress", Content: "C"},  // same gap as B, after B
		{Seq: 4, AnchorMsgCount: 5, AgentID: "d", Kind: "completed", Content: "D"}, // beyond all
	}
	delivered := map[string]struct{}{}
	nextID := 100
	got := mergeNotifyAnchors(base, anchors, delivered, &nextID)

	wantLen := 9
	if len(got) != wantLen {
		t.Fatalf("len = %d, want %d", len(got), wantLen)
	}
	// Expected: u0, A, a1, t1, B, C, u2, a3, D
	expect := []struct {
		idx  int
		kind string
		text string
	}{
		{1, "progress", "A"},
		{4, "progress", "B"},
		{5, "progress", "C"},
		{8, "completed", "D"},
	}
	for _, e := range expect {
		b := got[e.idx]
		if b.Type != BlockStatus {
			t.Errorf("block[%d].Type = %v, want BlockStatus", e.idx, b.Type)
		}
		if b.StatusKind != e.kind {
			t.Errorf("block[%d].StatusKind = %q, want %q", e.idx, b.StatusKind, e.kind)
		}
		if b.Content != e.text {
			t.Errorf("block[%d].Content = %q, want %q", e.idx, b.Content, e.text)
		}
	}
	// Base blocks must keep their relative order and MsgIndex.
	if got[0].ID != 0 || got[2].ID != 1 || got[3].ID != 2 || got[6].ID != 3 || got[7].ID != 4 {
		t.Errorf("base block order broken: %v", blockIDs(got))
	}
	if nextID != 104 {
		t.Errorf("nextID = %d, want 104", nextID)
	}
}

func TestMergeNotifyAnchorsDedupDelivered(t *testing.T) {
	base := []*Block{anchorBlock(0, 0), anchorBlock(1, 1)}
	anchors := []notifyAnchor{
		{Seq: 1, AnchorMsgCount: 1, AgentID: "a", Kind: "progress", Content: "live-only"},
		{Seq: 2, AnchorMsgCount: 1, AgentID: "b", Kind: "progress", MessageID: "delivered-1", Content: "dup"},
	}
	delivered := map[string]struct{}{"delivered-1": {}}
	nextID := 10
	got := mergeNotifyAnchors(base, anchors, delivered, &nextID)
	// Only the live-only anchor should be inserted (delivered one is skipped).
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[1].Content != "live-only" {
		t.Errorf("inserted block content = %q, want live-only", got[1].Content)
	}
}

func TestMergeNotifyAnchorsEmpty(t *testing.T) {
	base := []*Block{anchorBlock(0, 0)}
	if got := mergeNotifyAnchors(base, nil, nil, new(int)); len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
}

func TestReadNotifyAnchors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, notifyAnchorFileName)
	lines := []string{
		`{"seq":1,"anchor_msg_count":2,"message_id":"m1","agent_id":"a","kind":"progress","content":"hi"}`,
		"", // blank line must be tolerated
		`{"seq":2,"anchor_msg_count":3,"agent_id":"b","kind":"completed","content":"done"}`,
		`not-json`, // corrupt line skipped
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	got, err := readNotifyAnchors(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (blank + corrupt skipped)", len(got))
	}
	if got[0].AnchorMsgCount != 2 || got[0].MessageID != "m1" || got[0].Content != "hi" {
		t.Errorf("entry0 = %+v", got[0])
	}
	if got[1].AnchorMsgCount != 3 || got[1].Content != "done" {
		t.Errorf("entry1 = %+v", got[1])
	}

	// Missing file is not an error.
	if missing, err := readNotifyAnchors(filepath.Join(dir, "nope.jsonl")); err != nil || missing != nil {
		t.Errorf("missing file: got %v, err %v; want nil, nil", missing, err)
	}
}

func TestDeliveredNotifyMessageIDs(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: "m1"}},
		{Role: "user", Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: "m2"}},
		{Role: "user", Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{}}, // no id
		{Role: "assistant"},
	}
	set := deliveredNotifyMessageIDs(msgs)
	if _, ok := set["m1"]; !ok {
		t.Error("m1 missing")
	}
	if _, ok := set["m2"]; !ok {
		t.Error("m2 missing")
	}
	if _, ok := set[""]; ok {
		t.Error("empty id should not be in set")
	}
	if len(set) != 2 {
		t.Errorf("set size = %d, want 2", len(set))
	}
}

func TestResolveNotifyTargetAgentID(t *testing.T) {
	cases := []struct {
		target, parent, want string
	}{
		{"agent-2", "", "agent-2"},
		{"main", "", ""},
		{"", "agent-9", "agent-9"},
		{"", "main", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := resolveNotifyTargetAgentID(c.target, c.parent); got != c.want {
			t.Errorf("resolveNotifyTargetAgentID(%q,%q) = %q, want %q", c.target, c.parent, got, c.want)
		}
	}
}

func blockIDs(blocks []*Block) []int {
	ids := make([]int, len(blocks))
	for i, b := range blocks {
		ids[i] = b.ID
	}
	return ids
}

// notifyAnchorTestAgent extends the shared controller fake with the session
// dir and the target-routed transcript reads the anchor record/replay paths
// use.
type notifyAnchorTestAgent struct {
	sessionControlAgent
	dir            string
	targetMessages map[string][]message.Message
}

func (a *notifyAnchorTestAgent) SessionDir() string { return a.dir }

func (a *notifyAnchorTestAgent) GetMessagesForTarget(target agent.ConversationTarget) []message.Message {
	if target.AgentID == "" || target.AgentID == "main" {
		return a.messages
	}
	return a.targetMessages[target.AgentID]
}

func (a *notifyAnchorTestAgent) SendUserMessageToTarget(agent.ConversationTarget, string) {}

func (a *notifyAnchorTestAgent) ContinueFromContextForTarget(agent.ConversationTarget) {}

func (a *notifyAnchorTestAgent) RemoveLastMessageForTarget(agent.ConversationTarget) {}

func TestNotifyAnchorsForView(t *testing.T) {
	anchors := []notifyAnchor{
		{Seq: 1, TargetAgentID: "main"},                           // main view
		{Seq: 2, ParentAgentID: "main"},                           // main view
		{Seq: 3, ParentAgentID: "agent-2"},                        // agent-2's view
		{Seq: 4, TargetAgentID: "agent-1", ParentAgentID: "main"}, // agent-1's view
		{Seq: 5, TargetAgentID: "main", ParentAgentID: "agent-1"}, // "main" collapses; parent fallback names agent-1
	}
	main := notifyAnchorsForView(anchors, "")
	if len(main) != 2 || main[1].Seq != 2 {
		t.Errorf("main view has %d entries, want [1 2]", len(main))
	}
	if got := notifyAnchorsForView(anchors, "agent-1"); len(got) != 2 || got[0].Seq != 4 || got[1].Seq != 5 {
		t.Errorf("agent-1 view = %+v, want seqs [4 5]", got)
	}
	if got := notifyAnchorsForView(anchors, "agent-2"); len(got) != 1 || got[0].Seq != 3 {
		t.Errorf("agent-2 view = %+v, want one seq-3 anchor", got)
	}
	if got := notifyAnchorsForView(anchors, "agent-9"); len(got) != 0 {
		t.Errorf("unknown view = %+v, want none", got)
	}
}

func TestAdoptNotifyAnchorSeqs(t *testing.T) {
	m := &Model{nextNotifyAnchorSeq: 0}
	m.adoptNotifyAnchorSeqs([]notifyAnchor{{Seq: 1}, {Seq: 5}, {Seq: 2}})
	if m.nextNotifyAnchorSeq != 6 {
		t.Errorf("after max seq 5 = %d, want 6", m.nextNotifyAnchorSeq)
	}
	// A later read with older entries must never lower the counter.
	m.adoptNotifyAnchorSeqs([]notifyAnchor{{Seq: 3}})
	if m.nextNotifyAnchorSeq != 6 {
		t.Errorf("after stale read = %d, want 6", m.nextNotifyAnchorSeq)
	}
}

func TestRecordNotifyAnchorPositionsAgainstTarget(t *testing.T) {
	dir := t.TempDir()
	agentMessages := []message.Message{{Role: message.RoleUser, Content: "agent one"}}
	backend := &notifyAnchorTestAgent{
		dir: dir,
		// Simulate watching agent-1: the legacy focused-GetMessages() route
		// would return this list, but anchors must ignore the focus.
		messagesByFocus: map[string][]message.Message{"agent-1": agentMessages},
		messages: []message.Message{
			{Role: message.RoleUser, Content: "main one"},
			{Role: message.RoleUser, Content: "main two"},
		},
		targetMessages: map[string][]message.Message{"agent-1": agentMessages},
	}
	m := NewModelWithSize(backend, 80, 30)

	m.recordNotifyAnchor(agent.AgentNotifyEvent{AgentID: "agent-9", ParentAgentID: "main", TargetAgentID: "main", Kind: "progress", Message: "working"})
	m.recordNotifyAnchor(agent.AgentNotifyEvent{AgentID: "agent-1", TaskID: "adhoc-1", TargetAgentID: "agent-1", Kind: "blocked", Message: "blocked note"})

	path := m.notifyAnchorPath()
	got, err := readNotifyAnchors(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].AnchorMsgCount != 2 || got[0].Kind != "progress" || got[0].Content != "working" {
		t.Errorf("main-targeted anchor = %+v, want count 2 against the main transcript", got[0])
	}
	if got[1].AnchorMsgCount != 1 || got[1].Kind != "blocked" || got[1].TaskID != "adhoc-1" {
		t.Errorf("agent-1-targeted anchor = %+v, want count 1 against agent-1's transcript", got[1])
	}
	if m.nextNotifyAnchorSeq != 2 {
		t.Errorf("nextNotifyAnchorSeq = %d, want 2", m.nextNotifyAnchorSeq)
	}
}

func TestClearCompactedNotifyAnchorsKeepsOtherViewAnchors(t *testing.T) {
	dir := t.TempDir()
	backend := &notifyAnchorTestAgent{dir: dir}
	m := NewModelWithSize(backend, 80, 30)
	path := m.notifyAnchorPath()
	in := []notifyAnchor{
		{Seq: 1, AnchorMsgCount: 4, Kind: "progress", Content: "stale", TargetAgentID: "main"},
		{Seq: 2, AnchorMsgCount: 2, Kind: "progress", Content: "stale", ParentAgentID: "main"},
		{Seq: 3, AnchorMsgCount: 1, Kind: "progress", Content: "agent work", TargetAgentID: "agent-1"},
	}
	if err := writeNotifyAnchors(path, in); err != nil {
		t.Fatal(err)
	}

	m.clearCompactedNotifyAnchors()

	got, err := readNotifyAnchors(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Seq != 3 || got[0].Content != "agent work" {
		t.Fatalf("after clear = %+v, want only the agent-1 anchor", got)
	}
	// Clearing again with no main-view anchors must not rewrite the log.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m.clearCompactedNotifyAnchors()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("clear with no main-view anchors rewrote the log")
	}
}

func TestReplayNotifyAnchorsForView(t *testing.T) {
	dir := t.TempDir()
	backend := &notifyAnchorTestAgent{
		dir: dir,
		messages: []message.Message{
			{Role: message.RoleUser, Content: "first"},
			{Role: "user", Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: "m1"}, Content: "progress snapshot"},
		},
		targetMessages: map[string][]message.Message{
			"agent-1": {{Role: message.RoleUser, Content: "agent work"}},
		},
	}
	m := NewModelWithSize(backend, 80, 30)
	if err := writeNotifyAnchors(m.notifyAnchorPath(), []notifyAnchor{
		{Seq: 1, AnchorMsgCount: 1, Kind: "progress", Content: "live-only", TargetAgentID: "main"},
		{Seq: 2, AnchorMsgCount: 1, Kind: "progress", MessageID: "m1", Content: "already persisted", TargetAgentID: "main"},
		{Seq: 3, AnchorMsgCount: 9, Kind: "completed", Content: "agent card", TargetAgentID: "agent-1"},
	}); err != nil {
		t.Fatal(err)
	}
	startID := m.nextBlockID

	base := []*Block{{ID: startID, Type: BlockUser, MsgIndex: 0}, {ID: startID + 1, Type: BlockUser, MsgIndex: 1}}
	// Register the base ids just like the real rebuild paths do before the
	// merge draws fresh ids for inserted cards.
	m.nextBlockID = startID + 2
	got := m.replayNotifyAnchorsForView(base, backend.messages, "")
	// Only the seq-1 main card merges: the delivered-id card is persisted and
	// the seq-3 card belongs to agent-1's view.
	if len(got) != 3 {
		t.Fatalf("main view merge = %v blocks, want one inserted card", blockIDs(got))
	}
	card := got[1]
	if card.ID != startID+2 || card.Type != BlockStatus || card.StatusKind != "progress" || card.Content != "live-only" {
		t.Errorf("replayed card = %v,%v,%v, want the live-only progress card with a fresh id", card.ID, card.Type, card.Content)
	}
	if m.nextNotifyAnchorSeq != 4 || m.nextBlockID != startID+3 {
		t.Errorf("seq/block adoption = %d/%d, want 4/%d", m.nextNotifyAnchorSeq, m.nextBlockID, startID+3)
	}

	agentBase := []*Block{{ID: m.nextBlockID, Type: BlockUser, MsgIndex: 0}}
	agentView := m.replayNotifyAnchorsForView(agentBase, backend.targetMessages["agent-1"], "agent-1")
	if len(agentView) != 2 || agentView[1].AgentID != "agent-1" || agentView[1].StatusKind != "completed" {
		t.Fatalf("agent-1 view = %+v, want the anchor card addressed to agent-1", agentView)
	}
}

func TestSessionRestoredRebuildReplaysNotifyAnchors(t *testing.T) {
	dir := t.TempDir()
	backend := &notifyAnchorTestAgent{
		dir: dir,
		messages: []message.Message{
			{Role: message.RoleUser, Content: "first"},
			{Role: message.RoleUser, Content: "second"},
		},
	}
	m := NewModelWithSize(backend, 80, 30)
	m.recordNotifyAnchor(agent.AgentNotifyEvent{AgentID: "agent-9", ParentAgentID: "main", TargetAgentID: "main", Kind: "progress", Message: "working"})

	m.rebuildViewportFromMessagesPreservingActivity("session_restored", false)

	replayed := findStatusBlock(m.viewport.blocks, "progress")
	if replayed == nil || replayed.Content != "working" {
		t.Fatalf("restore rebuild did not replay the notify card: %v", m.viewport.blocks)
	}
	if replayed.AgentID != "" {
		t.Errorf("replayed card AgentID = %q, want main view attribution", replayed.AgentID)
	}
}

func TestCompactedRestoreRebuildKeepsLiveOnlyCardsOutAndClearsAnchors(t *testing.T) {
	dir := t.TempDir()
	backend := &notifyAnchorTestAgent{
		dir: dir,
		messages: []message.Message{
			{Role: message.RoleUser, Content: "first"},
			{Role: message.RoleUser, Content: "second"},
		},
		targetMessages: map[string][]message.Message{
			"agent-1": {{Role: message.RoleUser, Content: "agent work"}},
		},
	}
	m := NewModelWithSize(backend, 80, 30)
	m.recordNotifyAnchor(agent.AgentNotifyEvent{AgentID: "agent-9", ParentAgentID: "main", TargetAgentID: "main", Kind: "progress", Message: "stale card"})
	m.recordNotifyAnchor(agent.AgentNotifyEvent{AgentID: "agent-1", TaskID: "adhoc-1", TargetAgentID: "agent-1", Kind: "progress", Message: "agent card"})

	m.rebuildViewportFromMessagesPreservingActivity("session_restored", true)

	if stale := findStatusBlock(m.viewport.blocks, "progress"); stale != nil && stale.Content == "stale card" {
		t.Fatalf("compacted rebuild replayed a stale anchor: %+v", stale)
	}
	got, err := readNotifyAnchors(m.notifyAnchorPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "adhoc-1" {
		t.Fatalf("anchors after compacted restore = %+v, want only the agent-1-targeted one", got)
	}
}

func TestFocusSwitchRebuildReplaysNotifyAnchors(t *testing.T) {
	dir := t.TempDir()
	backend := &notifyAnchorTestAgent{
		dir: dir,
		messages: []message.Message{
			{Role: message.RoleUser, Content: "main one"},
		},
		targetMessages: map[string][]message.Message{
			"agent-1": {{Role: message.RoleUser, Content: "agent work"}},
		},
	}
	m := NewModelWithSize(backend, 80, 30)
	m.recordNotifyAnchor(agent.AgentNotifyEvent{AgentID: "agent-1", TaskID: "adhoc-1", TargetAgentID: "agent-1", Kind: "blocked", Message: "blocked note"})

	m.rebuildFocusedViewport("agent-1", "agent-1")

	replayed := findStatusBlock(m.viewport.blocks, "blocked")
	if replayed == nil || replayed.AgentID != "agent-1" || replayed.Content != "blocked note" {
		t.Fatalf("focus rebuild did not replay the agent-1 blocked card: %v", m.viewport.blocks)
	}
}

func findStatusBlock(blocks []*Block, kind string) *Block {
	for _, b := range blocks {
		if b != nil && b.Type == BlockStatus && b.StatusKind == kind {
			return b
		}
	}
	return nil
}
