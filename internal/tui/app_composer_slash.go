package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

// queuedDraft holds a user message submitted while the agent was busy.
// It is stored locally in the TUI and sent to the agent after IdleEvent.
type queuedDraft struct {
	ID             string
	AgentID        string // "" = main agent; non-empty = focused subagent at submit time
	Content        string
	DisplayContent string
	Parts          []message.ContentPart // non-nil for multi-part (text+images)
	FileRefs       []string              // @-mentioned file paths (deduped, display only)
	Mirrored       bool                  // true when also mirrored into MainAgent pending queue
	QueuedAt       time.Time             // local enqueue/submission time for status-bar timing
	LoopAnchor     bool                  // UI-only marker for the user prompt that started loop mode
}

type agentComposerState struct {
	draft                inputDraftSnapshot
	historyBrowsing      bool
	historyIndex         int
	historyDraft         inputDraftSnapshot
	attachments          []Attachment
	editingQueuedDraftID string
}

type composerRuntimeState struct {
	queuedDrafts                []queuedDraft
	agentComposerStates         map[string]agentComposerState
	editingQueuedDraftID        string
	inflightDraft               *queuedDraft
	clipboardAttachmentPending  bool
	clipboardAttachmentAgentID  string
	clipboardAttachmentSeq      uint64
	clipboardPasteSuppressUntil time.Time
	queueSyncEnabled            bool
	pauseQueuedDraftDrainOnce   bool
	nextQueuedDraftID           int
}

func fileRefsFromParts(parts []message.ContentPart) []string {
	var refs []string
	seen := make(map[string]bool)
	for _, p := range parts {
		if p.Type != message.ContentPartText {
			continue
		}
		for _, ref := range message.FileRefs(p.Text) {
			if ref.Path == "" {
				continue
			}
			display := ref.Path
			if ref.Lines != "" {
				display += ":" + ref.Lines
			}
			if seen[display] {
				continue
			}
			seen[display] = true
			refs = append(refs, display)
		}
	}
	return refs
}

func queuedDraftTextAndImageCount(draft queuedDraft) (string, int) {
	text := draft.DisplayContent
	if text == "" {
		text = displayTextFromParts(draft.Parts, draft.Content)
	}
	imageCount := 0
	for _, part := range draft.Parts {
		if part.Type == message.ContentPartImage {
			imageCount++
		}
	}
	return text, imageCount
}

func queuedDraftFromParts(parts []message.ContentPart) queuedDraft {
	copied := make([]message.ContentPart, len(parts))
	copy(copied, parts)
	display, _ := displayTextAndInlinePastes(copied, "")
	return queuedDraft{
		Parts:          copied,
		DisplayContent: display,
		Content:        display,
	}
}

func (d queuedDraft) contentParts() []message.ContentPart {
	if len(d.Parts) > 0 {
		copied := make([]message.ContentPart, len(d.Parts))
		copy(copied, d.Parts)
		return copied
	}
	if d.Content == "" {
		return nil
	}
	return []message.ContentPart{{Type: message.ContentPartText, Text: d.Content}}
}

func normalizeDraftAgentID(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return identity.MainAgentID
	}
	return agentID
}

// Attachment holds a pending binary attachment to be sent with the next user message.
type Attachment struct {
	FileName               string
	MimeType               string
	Data                   []byte
	SizeBytes              int
	ImagePath              string
	Unsupported            bool
	Encrypted              bool
	InlineImagePlaceholder bool
}

func attachmentExtForMimeType(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	default:
		return ".png"
	}
}

func attachmentsFromParts(parts []message.ContentPart) []Attachment {
	attachments := make([]Attachment, 0, len(parts))
	imageIndex := 0
	for _, part := range parts {
		if !part.IsBinary() {
			continue
		}
		imageIndex++
		fileName := imagePartDisplayName(part.FileName, part.ImagePath, part.MimeType, imageIndex)
		attachments = append(attachments, Attachment{
			FileName:               fileName,
			MimeType:               part.MimeType,
			Data:                   part.Data,
			ImagePath:              part.ImagePath,
			InlineImagePlaceholder: part.Type == message.ContentPartImage,
		})
	}
	return attachments
}

// slashCommand describes one slash command for autocomplete.
type slashCommand struct {
	Cmd   string // e.g. "/resume"
	Desc  string // short description for the dropdown
	Scope string // custom command scope, e.g. "project" or "global"; empty for built-ins
}

// CustomCommand is the exported DTO used to inject custom slash commands into
// the TUI for autocomplete display only. The expansion is handled by the agent.
type CustomCommand struct {
	Cmd   string // must start with "/"
	Desc  string // short description shown in the dropdown; may be empty
	Scope string // command scope shown on its own row; may be empty
}

// slashCommands is the list of available slash commands (alphabetical by Cmd).
var slashCommands = []slashCommand{
	{Cmd: "/compact", Desc: "compact old context"},
	{Cmd: "/export", Desc: "export session (Markdown/JSON)"},
	{Cmd: "/help", Desc: "show keyboard help"},
	{Cmd: "/models", Desc: "switch current view pool"},
	{Cmd: "/new", Desc: "start a fresh session"},
	{Cmd: "/resume", Desc: "resume previous session"},
	{Cmd: "/rules", Desc: "manage permission rules"},
	{Cmd: "/stats", Desc: "usage statistics"},
}

// slashCompletionLine formats a single completion row. Custom command scope stays
// inline so each command consumes exactly one menu row.
func slashCompletionLine(c slashCommand) string {
	if c.Scope == "" {
		return fmt.Sprintf("%s  %s", c.Cmd, c.Desc)
	}
	return fmt.Sprintf("%s  [%s] %s", c.Cmd, c.Scope, c.Desc)
}

// getSlashCompletions returns slash commands that match the current input.
// input should start with "/"; the part after "/" is used as prefix filter.
// It merges built-in commands with any custom commands on the model.
func (m *Model) getSlashCompletions(input string) []slashCommand {
	if input == "" || input[0] != '/' {
		return nil
	}
	prefix := strings.ToLower(input)
	var out []slashCommand
	loopCommands := []slashCommand{}
	if m.focusedAgentID == "" && strings.HasPrefix("/loop", prefix) && (m.agent == nil || m.agent.CanUseLoopMode()) {
		if m.agent == nil || m.agent.CurrentLoopState() == "" {
			loopCommands = append(loopCommands, slashCommand{Cmd: "/loop on", Desc: "enable loop mode"})
		} else {
			loopCommands = append(loopCommands, slashCommand{Cmd: "/loop off", Desc: "disable loop mode"})
		}
	}
	out = append(out, loopCommands...)
	yoloCommands := []slashCommand{}
	if m.focusedAgentID == "" && strings.HasPrefix("/yolo", prefix) {
		if m.yoloEnabled() {
			yoloCommands = append(yoloCommands, slashCommand{Cmd: "/yolo off", Desc: "disable YOLO permission bypass"})
		} else {
			yoloCommands = append(yoloCommands, slashCommand{Cmd: "/yolo on", Desc: "enable YOLO permission bypass"})
		}
	}
	out = append(out, yoloCommands...)
	tierCommands := []slashCommand{}
	if strings.HasPrefix("/tier", prefix) {
		if next, ok := nextServiceTier(m.serviceTier(), m.supportedServiceTiers()); ok {
			tierCommands = append(tierCommands, slashCommand{Cmd: "/tier " + string(next), Desc: "switch to " + string(next) + " tier"})
		}
	}
	out = append(out, tierCommands...)
	for _, c := range slashCommands {
		if strings.HasPrefix(strings.ToLower(c.Cmd), prefix) {
			out = append(out, c)
		}
	}
	for _, c := range m.customCommands {
		if strings.HasPrefix(strings.ToLower(c.Cmd), prefix) {
			out = append(out, c)
		}
	}
	return out
}

// renderSlashCompletionDropdown returns a small dropdown list when input starts
// with "/" and there are matching commands. Empty string otherwise.
func (m *Model) renderSlashCompletionDropdown(value string) string {
	matches := m.getSlashCompletions(value)
	if len(matches) == 0 {
		return ""
	}
	maxVisible := min(8, len(matches))

	sel := m.slashCompleteSelected
	if sel >= len(matches) {
		sel = len(matches) - 1
	}
	if sel < 0 {
		sel = 0
	}
	start := 0
	if maxVisible > 0 && sel >= maxVisible {
		start = sel - maxVisible + 1
	}
	end := min(len(matches), start+maxVisible)
	if m.slashCache.text != "" &&
		m.slashCache.width == m.width &&
		m.slashCache.theme == m.theme.Name &&
		m.slashCache.value == value &&
		m.slashCache.sel == sel {
		return m.slashCache.text
	}

	help := DimStyle.Render("Tab/Enter complete  ↑/↓ select  Esc close")

	// Calculate dynamic width based on content
	contentWidth := runewidth.StringWidth("Tab/Enter complete  ↑/↓ select  Esc close")
	for i := start; i < end; i++ {
		c := matches[i]
		w := runewidth.StringWidth(fmt.Sprintf(" ▸ %s", slashCompletionLine(c)))
		if w > contentWidth {
			contentWidth = w
		}
	}

	// Limit width but keep it reasonable
	maxWidth := min(80, m.width-10)
	if contentWidth > maxWidth {
		contentWidth = maxWidth
	}
	if contentWidth < 30 {
		contentWidth = 30
	}

	lines := make([]string, 0, maxVisible+2)
	lines = append(lines, help, "")

	for i := start; i < end; i++ {
		c := matches[i]
		line := slashCompletionLine(c)
		lineLimit := contentWidth
		if i == sel {
			lineLimit -= runewidth.StringWidth(" ▸ ")
		} else {
			lineLimit -= runewidth.StringWidth("   ")
		}
		if lineLimit < 1 {
			lineLimit = 1
		}
		if runewidth.StringWidth(line) > lineLimit {
			line = runewidth.Truncate(line, lineLimit, "…")
		}

		if i == sel {
			styledLine := SelectedStyle.Width(contentWidth).Render(" ▸ " + line)
			lines = append(lines, styledLine)
		} else {
			lines = append(lines, "   "+line)
		}
	}

	frameWidth := DirectoryBorderStyle.GetHorizontalPadding() + DirectoryBorderStyle.GetHorizontalBorderSize()
	body := strings.Join(lines, "\n")
	out := DirectoryBorderStyle.Width(contentWidth + frameWidth).Render(body)
	m.slashCache = slashRenderCache{
		width: m.width,
		theme: m.theme.Name,
		value: value,
		sel:   sel,
		text:  out,
	}
	return out
}

// SetCustomCommands registers extra slash commands for autocomplete display.
// Expansion of these commands is handled entirely by the agent, not the TUI.
func (m *Model) SetCustomCommands(cmds []CustomCommand) {
	m.customCommands = make([]slashCommand, 0, len(cmds))
	for _, c := range cmds {
		cmd := c.Cmd
		if !strings.HasPrefix(cmd, "/") {
			cmd = "/" + cmd
		}
		m.customCommands = append(m.customCommands, slashCommand{Cmd: cmd, Desc: c.Desc, Scope: c.Scope})
	}
	sort.Slice(m.customCommands, func(i, j int) bool {
		return m.customCommands[i].Cmd < m.customCommands[j].Cmd
	})
}
