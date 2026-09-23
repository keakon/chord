package agent

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// pressureStage is the stage of the current pressure cycle. A cycle spans one
// compaction window — the same (session epoch, compaction window, model ref,
// budget epoch) identity the reminder-class overlay claims bind to — and it is
// the single authority behind what used to be two independent escalation
// ladders: the notice ladder (reminder → imminent countdown → externalization
// warning) and the usage-driven start ladder (armed → threshold grace →
// compacting). Both read from and write to this one stage, so a notice can no
// longer describe a pressure the trigger side has already left behind.
type pressureStage uint8

const (
	// pressureStageClear is the absence of an open cycle: usage sits below the
	// reminder line, or the externalization contract is off entirely.
	pressureStageClear pressureStage = iota
	// pressureStageReminded is the sticky floor: above the reminder line, the
	// model has been told to prepare.
	pressureStageReminded
	// pressureStageArmed is the threshold crossing: the usage-driven
	// compaction request is armed and its warning rides the request that
	// starts the compaction.
	pressureStageArmed
	// pressureStageGrace is a threshold crossing deferred by the threshold
	// grace period; the imminent countdown is attached to every deferred
	// request.
	pressureStageGrace
	// pressureStageCompacting advances an already-open cycle while a
	// compaction runs; it never opens one, so a manual or oversize-driven
	// compaction cannot invent a pressure cycle.
	pressureStageCompacting
)

func (s pressureStage) String() string {
	switch s {
	case pressureStageClear:
		return "clear"
	case pressureStageReminded:
		return "reminded"
	case pressureStageArmed:
		return "armed"
	case pressureStageGrace:
		return "grace"
	case pressureStageCompacting:
		return "compacting"
	}
	return "unknown"
}

// pressureEndReason is the terminal state of a pressure cycle. The reasons are
// deliberately distinct: a successful compaction, a withdrawal below the
// reminder line, a window reset and a disabled externalization contract are
// different outcomes, and a review that reads them as one "cycle ended" number
// cannot tell whether the pressure was resolved or merely abandoned.
type pressureEndReason string

const (
	// pressureEndApplied is the successful compaction that advanced the
	// window; the new window evaluates its own pressure from scratch.
	pressureEndApplied pressureEndReason = "applied"
	// pressureEndWithdrawn is usage falling back below the reminder line
	// before any compaction started.
	pressureEndWithdrawn pressureEndReason = "withdrawn"
	// pressureEndWindowClosed is a session switch, restore or model/budget
	// change that replaced the window identity the cycle was opened in.
	pressureEndWindowClosed pressureEndReason = "window_closed"
	// pressureEndDisabled is auto-compaction being switched off, a missing
	// input budget, or compact_context being hidden or denied: there is no
	// externalization contract left to notify the model about.
	pressureEndDisabled pressureEndReason = "disabled"
)

// pressureCycleAudit carries the preparation observability for a cycle: when
// the model first answered the nudge with a checkpoint request, first
// committed an agent-owned note or plan document, and first handed the turn
// back to the user. Audit only — none of these marks gates a notice, a stage
// or a compaction. Kept separate so the core cycle identity and stage stay
// readable without the observability weight.
type pressureCycleAudit struct {
	respondedAt      time.Time
	stateFileWriteAt time.Time
	awaitingUserAt   time.Time
}

// pressureCycle is the single runtime authority for the current pressure
// cycle. It carries the identity the runtime allocates when a cycle opens, the
// window it belongs to, the stage reached, whether the model answered the nudge
// (audit only — it never proves the context is safe), and how the cycle ended.
//
// A cycle owns at most one durable notice row (message.PressureCycleID stamps
// it): the first notice that reaches the transcript records the cycle, and
// later escalations are suppressed, so the transcript cannot accumulate
// several cards that all describe the same pressure. Escalation delivery is
// recorded through the analytics diagnostics emitted with the overlay claims.
// A restored transcript adopts the rows and cycle IDs already on disk instead
// of opening a duplicate cycle for the same pressure.
//
// The state is guarded by overlayClaims.mu because the durable-row reservation
// runs on the main LLM goroutine at the dispatch confirmation point while the
// stage transitions run on the event loop.
type pressureCycle struct {
	id        uint64 // runtime-allocated cycle identity; 0 = no open cycle
	nextID    uint64 // monotonic counter; seeded from restored rows
	window    overlayWindowKey
	stage     pressureStage
	recorded  bool // this cycle already wrote its one durable notice row
	responded bool // the model called compact_context in this cycle (audit)
	audit     pressureCycleAudit
	// adopted marks that the transcript this cycle opens against already
	// carries a durable notice row for it (restore), and adoptedID is that
	// row's cycle. The revived cycle keeps that identity instead of allocating
	// a new one, so a restore neither duplicates the record nor renumbers the
	// cycle the transcript already documents.
	adopted   bool
	adoptedID uint64
}

// pressureCycleEnd carries the facts of a closed cycle out of the locked
// transition so the journal write never runs under overlayClaims.mu.
type pressureCycleEnd struct {
	id        uint64
	stage     pressureStage
	responded bool
	recorded  bool
	reason    pressureEndReason
	audit     pressureCycleAudit
}

// notePressureStage advances the cycle to stage. Stages up to grace open a
// cycle when none is open (allocating the identity and, after a restore,
// adopting the row already on disk); compacting only advances an open cycle.
// Pass the live window key: a key the open cycle does not belong to closes it
// and opens a fresh one.
//
// The event loop drives every stage up to grace, but compacting is noted from
// the compaction worker, which runs in parallel with the main request and can
// outlive the turn that scheduled it. That is deliberate and needs no
// hand-off: the whole cycle lives under overlayClaims.mu, so an off-loop
// caller advances the same state with the same ordering guarantees, and the
// journal write still happens outside the lock.
func (a *MainAgent) notePressureStage(stage pressureStage, key overlayWindowKey) {
	if a == nil {
		return
	}
	a.overlayClaims.mu.Lock()
	c := &a.overlayClaims.pressure
	if stage == pressureStageCompacting && c.id == 0 {
		a.overlayClaims.mu.Unlock()
		return
	}
	var end pressureCycleEnd
	var openedID uint64
	var openedWindow overlayWindowKey
	var openedRecorded bool
	var stageLogged bool
	var loggedID uint64
	var loggedPrevious, loggedStage pressureStage
	if c.id == 0 || c.window != key {
		// A window the cycle does not belong to: close the stale one and open
		// against the live identity. The explicit close sites
		// (clearCompactionGrace) normally get here first; this is the safety
		// net for a stage transition that reaches the cycle first.
		// Compacting never opens a cycle on its own: a window change that
		// arrives as compacting only closes the stale cycle.
		if c.id != 0 {
			end = a.closePressureCycleLocked(pressureEndWindowClosed)
		}
		if stage == pressureStageCompacting {
			a.overlayClaims.mu.Unlock()
			a.recordPressureCycleEnd(end)
			return
		}
		a.openPressureCycleLocked(key)
		openedID = c.id
		openedWindow = key
		openedRecorded = c.recorded
	}
	if c.stage != stage {
		previous := c.stage
		c.stage = stage
		stageLogged = true
		loggedID = c.id
		loggedPrevious = previous
		loggedStage = stage
	}
	a.overlayClaims.mu.Unlock()
	if openedID != 0 {
		log.Debugf("pressure cycle %v opened window_epoch=%v window_index=%v recorded=%v", openedID, openedWindow.windowEpoch, openedWindow.windowIndex, openedRecorded)
	}
	if stageLogged {
		log.Debugf("pressure cycle %v stage %v -> %v", loggedID, loggedPrevious, loggedStage)
	}
	a.recordPressureCycleEnd(end)
}

// openPressureCycleLocked allocates the identity for key, adopting the durable
// row a restore already has on disk. Callers must hold overlayClaims.mu and
// must have closed any previous cycle. It never logs: the caller reports the
// open after releasing the lock.
func (a *MainAgent) openPressureCycleLocked(key overlayWindowKey) {
	c := &a.overlayClaims.pressure
	if c.adopted {
		// A restored transcript already documents this pressure with a durable
		// row: revive that cycle identity — or allocate one when the row
		// predates cycle stamps — and adopt the row as its record.
		c.id = c.adoptedID
		if c.id == 0 {
			c.nextID++
			c.id = c.nextID
		}
		if c.nextID < c.id {
			c.nextID = c.id
		}
		c.adopted = false
		c.adoptedID = 0
		c.recorded = true
		// The restored transcript still carries the full notice text, so the
		// reminder claim starts answered: later requests re-attach the short
		// form and never write a second row for this cycle.
		a.overlayClaims.reminder.bindTo(key)
		a.overlayClaims.reminder.delivered = true
	} else {
		c.nextID++
		c.id = c.nextID
	}
	c.window = key
	c.stage = pressureStageClear
	c.responded = false
	c.audit = pressureCycleAudit{}
}

// closePressureCycleLocked records the terminal state and clears the open
// cycle. Callers must hold overlayClaims.mu and must write the journal record
// through recordPressureCycleEnd afterwards.
func (a *MainAgent) closePressureCycleLocked(reason pressureEndReason) pressureCycleEnd {
	c := &a.overlayClaims.pressure
	if c.id == 0 {
		return pressureCycleEnd{}
	}
	end := pressureCycleEnd{
		id:        c.id,
		stage:     c.stage,
		responded: c.responded,
		recorded:  c.recorded,
		reason:    reason,
		audit:     c.audit,
	}
	c.id = 0
	c.stage = pressureStageClear
	c.recorded = false
	c.responded = false
	c.audit = pressureCycleAudit{}
	c.adopted = false
	c.adoptedID = 0
	return end
}

// recordPressureCycleEnd writes the terminal state to the journal: the stage
// reached, whether the model answered the nudge, and whether the cycle managed
// to leave a durable record. The preparation-audit instants ride along — when
// the model answered with a checkpoint request, committed an agent-owned note
// or plan document, and handed the turn back to the user — and an action that
// never happened leaves its key out rather than reporting an empty instant.
// Runs outside overlayClaims.mu.
func (a *MainAgent) recordPressureCycleEnd(end pressureCycleEnd) {
	if a == nil || end.id == 0 {
		return
	}
	log.Debugf("pressure cycle %v ended reason=%v stage=%v responded=%v recorded=%v responded_at=%v state_file_write_at=%v awaiting_user_at=%v", end.id, end.reason, end.stage, end.responded, end.recorded, end.audit.respondedAt, end.audit.stateFileWriteAt, end.audit.awaitingUserAt)
	diagnostic := map[string]string{
		"cycle":     strconv.FormatUint(end.id, 10),
		"stage":     end.stage.String(),
		"reason":    string(end.reason),
		"responded": strconv.FormatBool(end.responded),
		"recorded":  strconv.FormatBool(end.recorded),
	}
	// Preparation audit: date the actions instead of only reporting that the
	// nudge went unanswered, so a review can tell a model that prepared too
	// late from one that never prepared at all.
	if at := formatPressureAuditTime(end.audit.respondedAt); at != "" {
		diagnostic["responded_at"] = at
	}
	if at := formatPressureAuditTime(end.audit.stateFileWriteAt); at != "" {
		diagnostic["state_file_write_at"] = at
	}
	if at := formatPressureAuditTime(end.audit.awaitingUserAt); at != "" {
		diagnostic["awaiting_user_at"] = at
	}
	a.recordContextDiagnosticEvent(analytics.UsagePurposeContextPressureCycle, diagnostic)
}

// formatPressureAuditTime renders an audit instant in the same shape
// json.Marshal gives a time.Time, so journal readers see one format. A zero
// instant means the action never happened inside the cycle and renders empty.
func formatPressureAuditTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339Nano)
}

// endPressureCycle closes the open cycle with reason. Idempotent: ending a
// closed or never-opened cycle is a no-op, so the reset sites may call it
// unconditionally and a retried close cannot double-record the terminal state.
func (a *MainAgent) endPressureCycle(reason pressureEndReason) {
	if a == nil {
		return
	}
	a.overlayClaims.mu.Lock()
	end := a.closePressureCycleLocked(reason)
	a.overlayClaims.mu.Unlock()
	a.recordPressureCycleEnd(end)
}

// pressurePreparation names one observable context-preparation action the
// model can take while a pressure cycle is live. These marks are audit facts
// only: none of them gates a notice, a stage or a compaction.
type pressurePreparation uint8

const (
	pressurePreparationCompactContext pressurePreparation = iota
	pressurePreparationStateFileWrite
	pressurePreparationAwaitingUser
)

// notePressurePreparation stamps the first time the model performed an
// observable preparation action in the live cycle, at the instant the action
// was observed. Without an open cycle the mark is dropped: preparation outside
// a nudge window is not this audit's subject. Callers run on the compact_context
// acceptance path and on the event loop (tool results).
func (a *MainAgent) notePressurePreparation(action pressurePreparation, at time.Time) {
	if a == nil {
		return
	}
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	c := &a.overlayClaims.pressure
	if c.id == 0 {
		return
	}
	switch action {
	case pressurePreparationCompactContext:
		c.responded = true
		if c.audit.respondedAt.IsZero() {
			c.audit.respondedAt = at
		}
	case pressurePreparationStateFileWrite:
		if c.audit.stateFileWriteAt.IsZero() {
			c.audit.stateFileWriteAt = at
		}
	case pressurePreparationAwaitingUser:
		if c.audit.awaitingUserAt.IsZero() {
			c.audit.awaitingUserAt = at
		}
	}
}

// notePressurePreparationFromToolResult classifies one completed tool result.
// A question call is the model handing the turn back to the user, dated at the
// ask rather than at the answer that released it: the recorded duration is the
// span the tool spent waiting for the user. A committed write of an agent-owned
// note or plan document is the model externalizing state by hand.
func (a *MainAgent) notePressurePreparationFromToolResult(payload *ToolResultPayload) {
	if a == nil || payload == nil {
		return
	}
	if tools.NormalizeName(payload.Name) == tools.NameQuestion {
		askedAt := time.Now()
		if d := payload.Duration; d > 0 {
			askedAt = askedAt.Add(-d)
		}
		a.notePressurePreparation(pressurePreparationAwaitingUser, askedAt)
		return
	}
	if wrotePressureStateFile(payload.FileState) {
		a.notePressurePreparation(pressurePreparationStateFileWrite, time.Now())
	}
}

// wrotePressureStateFile reports whether a committed set of mutations touched an
// agent-owned note or plan document. Paths are matched in the spelling the tool
// recorded them; see isPressureStateFilePath.
func wrotePressureStateFile(state *message.ToolFileState) bool {
	if state == nil {
		return false
	}
	for _, written := range state.Writes {
		if isPressureStateFilePath(written.Path) {
			return true
		}
	}
	for _, change := range state.Changes {
		if change.Deleted {
			continue
		}
		if isPressureStateFilePath(change.Path) || isPressureStateFilePath(change.TargetPath) {
			return true
		}
	}
	return false
}

// isPressureStateFilePath reports whether a recorded path names a document
// under an agent-owned notes or plans root. Recorded paths are either
// repository-relative (./.chord/notes/task.md) or absolute, and both spellings
// are matched by shape without resolving the session working directory: an
// absolute path counts when it contains /.chord/notes/ or /.chord/plans/,
// because this mark is audit-only and the project root is not known here.
func isPressureStateFilePath(path string) bool {
	cleaned := strings.TrimPrefix(strings.TrimSpace(filepath.ToSlash(path)), "./")
	if cleaned == "" {
		return false
	}
	if isCompactionNotesPath(cleaned) {
		return true
	}
	return strings.Contains(cleaned, "/.chord/notes/") || strings.Contains(cleaned, "/.chord/plans/")
}

// reservePressureCycleRecord returns the cycle identity to stamp on a durable
// notice row, or the reason no row may be written: "no_cycle" when no cycle is
// open (escalations only ever ride an open cycle — the queue paths note the
// stage before staging the notice text), "already_recorded" when this cycle
// already wrote its one row. Runs on the main LLM goroutine at the dispatch
// confirmation point; the state is mutex-guarded.
func (a *MainAgent) reservePressureCycleRecord() (uint64, string, bool) {
	if a == nil {
		return 0, "no_agent", false
	}
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	c := &a.overlayClaims.pressure
	if c.id == 0 {
		return 0, "no_cycle", false
	}
	if c.recorded {
		return 0, "already_recorded", false
	}
	c.recorded = true
	return c.id, "", true
}

// releasePressureCycleRecord returns the one-row reservation when the row that
// carried it was withdrawn from the transcript: the cycle may record again if
// its pressure line is crossed once more, while a withdrawn row from an older
// cycle cannot reopen the current one's reservation.
func (a *MainAgent) releasePressureCycleRecord(cycleID uint64) {
	if a == nil || cycleID == 0 {
		return
	}
	a.overlayClaims.mu.Lock()
	if c := &a.overlayClaims.pressure; c.id == cycleID {
		c.recorded = false
	}
	a.overlayClaims.mu.Unlock()
}

// adoptPressureCyclesFromTranscript re-establishes the cycle identity from a
// freshly loaded transcript: the ID counter continues past the IDs already on
// disk, and a transcript that still carries a durable notice row marks the next
// cycle as already recorded, so a restore cannot duplicate the record or open a
// cycle the transcript already documents. Rows without an ID (written before
// cycles carried one) are still recorded as adopted: the row is replayed to the
// model either way, and treating it as the cycle's record is what prevents a
// duplicate.
func (a *MainAgent) adoptPressureCyclesFromTranscript(messages []message.Message) {
	if a == nil {
		return
	}
	a.endPressureCycle(pressureEndWindowClosed)
	a.derivePressureCycleAdoption(messages)
}

// derivePressureCycleAdoption points the adoption at the durable notice rows a
// transcript still carries: the highest cycle ID on a row continues the ID
// counter, and any row marks the next cycle as already recorded. Rows without
// an ID (written before cycles carried one) still count — the row is replayed to
// the model either way, and treating it as the cycle's record is what prevents a
// duplicate.
//
// The transcript is the authority here, so every rewrite of the durable table
// re-derives: a row withdrawn before the next cycle opens would otherwise leave
// the runtime adopting a row that is gone, and that cycle would never write its
// one card. Call on the event loop, after the rewrite.
func (a *MainAgent) derivePressureCycleAdoption(messages []message.Message) {
	if a == nil {
		return
	}
	maxID, hasRow := adoptedPressureRow(messages)
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	c := &a.overlayClaims.pressure
	if maxID > c.nextID {
		c.nextID = maxID
	}
	c.adopted = hasRow
	c.adoptedID = maxID
}

// adoptedPressureRow returns the highest cycle identity stamped on a durable
// notice row and whether the transcript carries one at all.
func adoptedPressureRow(messages []message.Message) (uint64, bool) {
	maxID := uint64(0)
	hasRow := false
	for i := range messages {
		if messages[i].Kind != message.KindContextNotice {
			continue
		}
		hasRow = true
		if messages[i].PressureCycleID > maxID {
			maxID = messages[i].PressureCycleID
		}
	}
	return maxID, hasRow
}
