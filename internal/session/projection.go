// Package session analysis projection.
//
// Project turns an ExportedSession into one JSONL line per turn: turn
// boundaries, tool outcomes, tool-attributed file changes, and compaction
// boundaries. It promises only observable facts; turn causes are downgraded
// to user_message | inferred | unknown because MessageProvenance carries no
// trigger, continue starts a turn without appending a marker message, and
// wake persists only interrupt-style terminal results.
package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// Turn triggers. Only user_message is directly observed (a user-authored user
// message starts the turn). inferred covers synthetic user starters
// (compaction summaries, background results, mailbox messages) that carry new
// work without an observable user cause. unknown covers turns with no leading
// user message at all (e.g. a continue that called newTurn without appending
// anything, or leading assistant/tool records before the first user message).
const (
	TriggerUserMessage = "user_message"
	TriggerInferred    = "inferred"
	TriggerUnknown     = "unknown"
)

// File-change attribution. exact means the recorded paths are the committed
// set (successful tool with FileState). partial means some subset committed
// while the call ended unsuccessfully, or the incomplete flag says the list
// may miss paths. unknown means no committed set is observable. A projection
// is tool-attributed fact, never a full workspace diff: "no record" does not
// mean "no change", especially for shell results without FileState.
const (
	AttributionExact   = "exact"
	AttributionPartial = "partial"
	AttributionUnknown = "unknown"
)

// File-change ops.
const (
	FileOpWrite   = "write"
	FileOpDelete  = "delete"
	FileOpMove    = "move"
	FileOpUnknown = "unknown"
)

// Rejection sources. Only recovery_barrier is structurally definitive
// (ToolRecoveryState not_started: the tool never reached its execution body).
// User denies, permission denies, and declined compact_context calls persist
// as ordinary error results with no structural deny marker (ResolveConfirm's
// denyReason only enters the rendered error text), so the projection refuses
// to guess them from text: they surface as status error with rejected false.
// See the Rejected field comment.
const (
	RejectionSourceNone            = "none"
	RejectionSourceRecoveryBarrier = "recovery_barrier"
)

// Normalized tool statuses. Empty/legacy statuses become unknown.
const (
	ProjectedStatusSuccess   = "success"
	ProjectedStatusError     = "error"
	ProjectedStatusCancelled = "cancelled"
	ProjectedStatusUnknown   = "unknown"
)

// ProjectionLimits bounds every text field in runes and the whole JSONL
// output in bytes. Over-limit text is cut with Truncated set and Ref pointing
// back at the source message; over-limit total output is an error, never a
// silent drop.
type ProjectionLimits struct {
	MaxUserTextRunes      int
	MaxAssistantTextRunes int
	MaxArgsRunes          int
	MaxTotalBytes         int
}

// DefaultProjectionLimits returns the standard budget.
func DefaultProjectionLimits() ProjectionLimits {
	return ProjectionLimits{
		MaxUserTextRunes:      2000,
		MaxAssistantTextRunes: 4000,
		MaxArgsRunes:          2000,
		MaxTotalBytes:         256 * 1024,
	}
}

// BoundedText is one length-capped text field. Truncated is explicit: cut
// text always carries Ref (session_id#message_index:field) to the full
// source, uncut text never does.
type BoundedText struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
	Ref       string `json:"ref,omitempty"`
}

// ProjectedToolCall is one declared tool call joined with its persisted
// result. Ref points at the result message when one exists, else at the
// declaration. ResultDigest is the hex SHA256 of the exported result Content
// (empty digest with no result); it recomputes from the export file and from
// main.jsonl Content, which Export copies verbatim. Refs may expire after the
// session is deleted or compacted; digests detect that.
//
// Rejected is narrow: true only for the structurally definitive
// non-execution case (ToolRecoveryState not_started, source
// recovery_barrier). Other error results keep rejected false even when the
// error text says "rejected by user" or "denied by permission policy":
// without a persistent decline marker those are observationally identical to
// execution failures, and guessing from text would mislabel real failures as
// declines. ResultUnknown marks outcome_unknown results: the call started but
// its outcome was never persisted, so consumers must check ResultUnknown
// before reading Status as success or failure.
type ProjectedToolCall struct {
	CallID          string      `json:"call_id"`
	Name            string      `json:"name"`
	Status          string      `json:"status"`
	RecoveryState   string      `json:"recovery_state,omitempty"`
	DurationMs      int64       `json:"duration_ms,omitempty"`
	Args            BoundedText `json:"args"`
	ResultDigest    string      `json:"result_digest,omitempty"`
	Ref             string      `json:"ref"`
	Rejected        bool        `json:"rejected"`
	RejectionSource string      `json:"rejection_source"`
	ResultUnknown   bool        `json:"result_unknown,omitempty"`
	// Origin names who triggered the call when that is observable and not the
	// model itself. A user-triggered /skill load persists a synthesized call
	// carrying OriginUser, so consumers can tell it apart from a model call
	// that produced the identical shape; model calls leave it empty.
	Origin string `json:"origin,omitempty"`
}

// ProjectedFileChange is one tool-attributed file mutation.
type ProjectedFileChange struct {
	Path        string `json:"path"`
	TargetPath  string `json:"target_path,omitempty"`
	Op          string `json:"op"`
	Added       int    `json:"added,omitempty"`
	Removed     int    `json:"removed,omitempty"`
	Attribution string `json:"attribution"`
}

// ProjectedProvenance locates the turn inside its source session.
type ProjectedProvenance struct {
	MessageIndexRange [2]int `json:"message_index_range"`
	SessionID         string `json:"session_id,omitempty"`
	InstanceID        string `json:"instance_id,omitempty"`
}

// ProjectedTurn is one JSONL line of a session projection. UserText is empty
// for trigger unknown turns and for compaction-boundary turns (the summary is
// a boundary marker, not a user request; its source stays reachable through
// the provenance range). ArchiveRef is always empty when projecting from an
// export alone: archive files live beside main.jsonl in the session directory
// and are unresolvable from ExportedSession.
type ProjectedTurn struct {
	TurnIndex          int                   `json:"turn_index"`
	Trigger            string                `json:"trigger"`
	UserText           BoundedText           `json:"user_text"`
	AssistantFinalText BoundedText           `json:"assistant_final_text"`
	ToolCalls          []ProjectedToolCall   `json:"tool_calls,omitempty"`
	FileChanges        []ProjectedFileChange `json:"file_changes,omitempty"`
	CompactionBoundary bool                  `json:"compaction_boundary,omitempty"`
	ArchiveRef         string                `json:"archive_ref,omitempty"`
	Provenance         ProjectedProvenance   `json:"provenance"`
}

// Project projects an exported session with the default budget. It is a pure
// function of its input: the same session always yields byte-identical JSONL.
func Project(exported *ExportedSession) ([]ProjectedTurn, error) {
	return ProjectWithLimits(exported, DefaultProjectionLimits())
}

// ProjectWithLimits projects with an explicit budget. It returns the turns and
// discards the encoding enforceProjectionBudget produced to measure them.
func ProjectWithLimits(exported *ExportedSession, limits ProjectionLimits) ([]ProjectedTurn, error) {
	if exported == nil {
		return nil, fmt.Errorf("session is nil")
	}
	turns := buildProjectedTurns(exported, limits)
	if _, err := enforceProjectionBudget(turns, limits); err != nil {
		return nil, err
	}
	return turns, nil
}

// ProjectJSONLWithLimits projects a session and returns the encoded JSONL
// bytes. The budget is enforced against the very bytes the caller receives, so
// a large session is encoded once rather than once to measure it and again to
// write it out.
func ProjectJSONLWithLimits(exported *ExportedSession, limits ProjectionLimits) ([]byte, error) {
	if exported == nil {
		return nil, fmt.Errorf("session is nil")
	}
	return enforceProjectionBudget(buildProjectedTurns(exported, limits), limits)
}

// enforceProjectionBudget encodes the turns and rejects a projection over its
// byte budget. Encoding is deterministic (see MarshalProjectedTurnsJSONL), so
// the measured size is the delivered size.
func enforceProjectionBudget(turns []ProjectedTurn, limits ProjectionLimits) ([]byte, error) {
	data, err := MarshalProjectedTurnsJSONL(turns)
	if err != nil {
		return nil, err
	}
	if limits.MaxTotalBytes > 0 && len(data) > limits.MaxTotalBytes {
		return nil, fmt.Errorf("projection exceeds budget: %d bytes over %d-byte limit (%d turns)", len(data), limits.MaxTotalBytes, len(turns))
	}
	return data, nil
}

// MarshalProjectedTurnsJSONL encodes one turn per line. Encoding is
// deterministic: struct field order is fixed and map keys sort, so the same
// turns always produce identical bytes for diffing and archiving.
func MarshalProjectedTurnsJSONL(turns []ProjectedTurn) ([]byte, error) {
	var buf bytes.Buffer
	for _, turn := range turns {
		data, err := json.Marshal(turn)
		if err != nil {
			return nil, fmt.Errorf("marshal projected turn %d: %w", turn.TurnIndex, err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

type projectedTurnBuilder struct {
	turnIndex   int
	trigger     string
	userMsgIdx  int
	hasUserText bool
	userContent string
	assistant   []int
	toolDecls   []toolDeclRef
	toolResults map[string]int
	fileMsgs    []int
	boundary    bool
	firstIdx    int
	lastIdx     int
}

type toolDeclRef struct {
	msgIdx int
	callID string
	name   string
	args   string
	origin string
}

func buildProjectedTurns(exported *ExportedSession, limits ProjectionLimits) []ProjectedTurn {
	sessionID := exported.Metadata[MetadataKeySessionID]
	instanceID := exported.Metadata[MetadataKeyInstanceID]
	var builders []*projectedTurnBuilder
	var current *projectedTurnBuilder

	startTurn := func(trigger string, firstIdx int) *projectedTurnBuilder {
		b := &projectedTurnBuilder{
			turnIndex:   len(builders),
			trigger:     trigger,
			userMsgIdx:  -1,
			toolResults: make(map[string]int),
			firstIdx:    firstIdx,
			lastIdx:     firstIdx,
		}
		builders = append(builders, b)
		return b
	}

	for i, em := range exported.Messages {
		if em.Role == message.RoleSystem {
			continue
		}
		if em.Role == message.RoleUser {
			if startsNewTurn(em) {
				current = startTurn(triggerForStarter(em), i)
				current.userMsgIdx = i
				if em.IsCompactionSummary {
					current.boundary = true
				} else if isUserAuthoredExported(em) || isInferredStarterWithContent(em) {
					current.hasUserText = true
					current.userContent = em.Content
				}
				continue
			}
			// Synthetic in-turn signal (loop/hook/continue/notice): keep it
			// inside the current turn's index range without touching the
			// turn's fact fields.
			if current == nil {
				current = startTurn(TriggerUnknown, i)
			} else {
				current.lastIdx = i
			}
			if em.IsCompactionSummary {
				current.boundary = true
			}
			continue
		}
		if current == nil {
			current = startTurn(TriggerUnknown, i)
		} else {
			current.lastIdx = i
		}
		switch em.Role {
		case message.RoleAssistant:
			if em.Content != "" {
				current.assistant = append(current.assistant, i)
			}
			for _, tc := range em.ToolCalls {
				current.toolDecls = append(current.toolDecls, toolDeclRef{msgIdx: i, callID: tc.ID, name: tc.Name, args: tc.Args, origin: exportedOrigin(em)})
			}
			if em.IsCompactionSummary {
				current.boundary = true
			}
		case message.RoleTool:
			if em.ToolCallID != "" {
				if _, ok := current.toolResults[em.ToolCallID]; !ok {
					current.toolResults[em.ToolCallID] = i
				}
			}
			if em.IsCompactionSummary {
				current.boundary = true
			}
			// Tool messages with FileState always contribute file changes,
			// whether or not their call ID matched a declaration. An
			// unattributed result joins the same list, so each message lands
			// in fileMsgs at most once.
			if em.ToolCallID == "" || em.FileState != nil || len(em.ToolChangedPaths) > 0 {
				current.fileMsgs = append(current.fileMsgs, i)
			}
		default:
			if em.IsCompactionSummary {
				current.boundary = true
			}
		}
	}

	turns := make([]ProjectedTurn, 0, len(builders))
	for _, b := range builders {
		turns = append(turns, renderProjectedTurn(exported, b, sessionID, instanceID, limits))
	}
	return turns
}

func startsNewTurn(em ExportedMessage) bool {
	if em.Role != message.RoleUser {
		return false
	}
	if em.IsCompactionSummary {
		return true
	}
	if isUserAuthoredExported(em) {
		return true
	}
	// Mailbox and background results deliver new work from the system; they
	// open an inferred turn. All other synthetic user kinds are in-turn
	// automation signals.
	switch em.Kind {
	case message.KindBackgroundResult, message.KindSubAgentMailbox:
		return true
	default:
		return false
	}
}

func triggerForStarter(em ExportedMessage) string {
	if isUserAuthoredExported(em) {
		return TriggerUserMessage
	}
	return TriggerInferred
}

func isUserAuthoredExported(em ExportedMessage) bool {
	if em.Role != message.RoleUser || em.IsCompactionSummary {
		return false
	}
	switch em.Kind {
	case "":
		return true
	case message.KindSubAgentMailbox,
		message.KindLoopNotice,
		message.KindBackgroundResult,
		message.KindHookFeedback,
		message.KindStreamContinue,
		message.KindContextNotice:
		return false
	default:
		// Unknown future kinds are synthetic until proven otherwise.
		return false
	}
}

func isInferredStarterWithContent(em ExportedMessage) bool {
	switch em.Kind {
	case message.KindBackgroundResult, message.KindSubAgentMailbox:
		return true
	default:
		return false
	}
}

// exportedOrigin reads the producer origin a message carries, empty when the
// message has no provenance. It is what marks a synthesized user-triggered
// skill call apart from a model call of the identical shape.
func exportedOrigin(em ExportedMessage) string {
	if em.Provenance == nil {
		return ""
	}
	return em.Provenance.Origin
}

func renderProjectedTurn(exported *ExportedSession, b *projectedTurnBuilder, sessionID, instanceID string, limits ProjectionLimits) ProjectedTurn {
	turn := ProjectedTurn{
		TurnIndex:          b.turnIndex,
		Trigger:            b.trigger,
		CompactionBoundary: b.boundary,
		Provenance: ProjectedProvenance{
			MessageIndexRange: [2]int{b.firstIdx, b.lastIdx},
			SessionID:         sessionID,
			InstanceID:        instanceID,
		},
	}
	if b.hasUserText {
		turn.UserText = boundText(b.userContent, limits.MaxUserTextRunes, textRef(sessionID, b.userMsgIdx, "user"))
	} else if b.userMsgIdx >= 0 {
		// Boundary/synthetic starter without user fact text: keep the source
		// reachable without letting the marker read as a user request.
		turn.UserText = BoundedText{Text: "", Ref: textRef(sessionID, b.userMsgIdx, "summary")}
	}
	assistantText := ""
	assistantIdx := -1
	for _, idx := range b.assistant {
		if exported.Messages[idx].Content != "" {
			assistantText = exported.Messages[idx].Content
			assistantIdx = idx
		}
	}
	if assistantIdx >= 0 {
		turn.AssistantFinalText = boundText(assistantText, limits.MaxAssistantTextRunes, textRef(sessionID, assistantIdx, "assistant"))
	}

	seen := make(map[string]bool, len(b.toolDecls))
	for _, decl := range b.toolDecls {
		if decl.callID == "" || seen[decl.callID] {
			continue
		}
		seen[decl.callID] = true
		turn.ToolCalls = append(turn.ToolCalls, renderProjectedToolCall(exported, decl, b.toolResults[decl.callID], sessionID, limits))
	}
	// Orphan results with no declaration stay visible under unknown, ordered
	// by message index so the same session always yields identical bytes.
	orphans := make([]toolDeclRef, 0, len(b.toolResults))
	for callID, msgIdx := range b.toolResults {
		if seen[callID] {
			continue
		}
		orphans = append(orphans, toolDeclRef{msgIdx: msgIdx, callID: callID, name: "unknown", origin: exportedOrigin(exported.Messages[msgIdx])})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].msgIdx < orphans[j].msgIdx })
	for _, orphan := range orphans {
		turn.ToolCalls = append(turn.ToolCalls, renderProjectedToolCall(exported, orphan, orphan.msgIdx, sessionID, limits))
	}

	seenFileMsg := make(map[int]bool, len(b.fileMsgs))
	for _, msgIdx := range b.fileMsgs {
		if seenFileMsg[msgIdx] {
			continue
		}
		seenFileMsg[msgIdx] = true
		em := exported.Messages[msgIdx]
		turn.FileChanges = append(turn.FileChanges, projectFileChanges(em)...)
	}
	return turn
}

func renderProjectedToolCall(exported *ExportedSession, decl toolDeclRef, resultIdx int, sessionID string, limits ProjectionLimits) ProjectedToolCall {
	name := decl.name
	if name == "" {
		name = "unknown"
	}
	call := ProjectedToolCall{
		CallID:          decl.callID,
		Name:            name,
		Status:          ProjectedStatusUnknown,
		Args:            boundText(decl.args, limits.MaxArgsRunes, textRef(sessionID, decl.msgIdx, decl.callID+":args")),
		Ref:             textRef(sessionID, decl.msgIdx, decl.callID),
		RejectionSource: RejectionSourceNone,
		Origin:          decl.origin,
	}
	if resultIdx >= 0 && resultIdx < len(exported.Messages) {
		if rm := exported.Messages[resultIdx]; rm.Role == message.RoleTool && (rm.ToolCallID == decl.callID || decl.callID == "") {
			call.Status = normalizeToolStatus(rm.ToolStatus)
			call.RecoveryState = rm.ToolRecoveryState
			call.DurationMs = rm.ToolDurationMs
			call.ResultDigest = resultDigest(rm.Content)
			call.Ref = textRef(sessionID, resultIdx, decl.callID)
			if rm.ToolRecoveryState == message.ToolRecoveryStateOutcomeUnknown {
				call.ResultUnknown = true
			}
			if rm.ToolRecoveryState == message.ToolRecoveryStateNotStarted {
				call.Rejected = true
				call.RejectionSource = RejectionSourceRecoveryBarrier
			}
		}
	}
	return call
}

func projectFileChanges(em ExportedMessage) []ProjectedFileChange {
	status := normalizeToolStatus(em.ToolStatus)
	attributionForRecorded := func() string {
		if em.FileAttributionIncomplete {
			return AttributionPartial
		}
		switch status {
		case ProjectedStatusSuccess:
			return AttributionExact
		case ProjectedStatusError, ProjectedStatusCancelled:
			return AttributionPartial
		default:
			return AttributionUnknown
		}
	}
	var out []ProjectedFileChange
	if em.FileState != nil {
		covered := make(map[string]bool)
		for _, change := range em.FileState.Changes {
			if strings.TrimSpace(change.Path) == "" {
				continue
			}
			op := FileOpWrite
			switch {
			case change.Deleted:
				op = FileOpDelete
			case strings.TrimSpace(change.TargetPath) != "":
				op = FileOpMove
			}
			out = append(out, ProjectedFileChange{
				Path:        change.Path,
				TargetPath:  change.TargetPath,
				Op:          op,
				Added:       change.Added,
				Removed:     change.Removed,
				Attribution: attributionForRecorded(),
			})
			covered[change.Path] = true
			if change.TargetPath != "" {
				covered[change.TargetPath] = true
			}
		}
		for _, state := range em.FileState.Writes {
			if strings.TrimSpace(state.Path) == "" || covered[state.Path] {
				continue
			}
			out = append(out, ProjectedFileChange{
				Path:        state.Path,
				Op:          FileOpWrite,
				Attribution: attributionForRecorded(),
			})
			covered[state.Path] = true
		}
		for _, state := range em.FileState.Deletes {
			if strings.TrimSpace(state.Path) == "" || covered[state.Path] {
				continue
			}
			out = append(out, ProjectedFileChange{
				Path:        state.Path,
				Op:          FileOpDelete,
				Attribution: attributionForRecorded(),
			})
			covered[state.Path] = true
		}
		if len(out) > 0 {
			return out
		}
	}
	// No FileState: only the SubAgent legacy path list may exist. Its op is
	// unobservable, so say so instead of guessing write.
	for _, path := range em.ToolChangedPaths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		out = append(out, ProjectedFileChange{Path: path, Op: FileOpUnknown, Attribution: AttributionUnknown})
	}
	return out
}

func normalizeToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case ProjectedStatusSuccess:
		return ProjectedStatusSuccess
	case ProjectedStatusError:
		return ProjectedStatusError
	case ProjectedStatusCancelled:
		return ProjectedStatusCancelled
	default:
		return ProjectedStatusUnknown
	}
}

func resultDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func boundText(content string, maxRunes int, ref string) BoundedText {
	if maxRunes <= 0 {
		return BoundedText{Text: content}
	}
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return BoundedText{Text: content}
	}
	return BoundedText{Text: string(runes[:maxRunes]), Truncated: true, Ref: ref}
}

func textRef(sessionID string, msgIdx int, field string) string {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Sprintf("#%d:%s", msgIdx, field)
	}
	return fmt.Sprintf("%s#%d:%s", sessionID, msgIdx, field)
}
