package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

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
	queuedDrafts              []queuedDraft
	agentComposerStates       map[string]agentComposerState
	editingQueuedDraftID      string
	inflightDraft             *queuedDraft
	queueSyncEnabled          bool
	pauseQueuedDraftDrainOnce bool
	nextQueuedDraftID         int
}

func fileRefsFromParts(parts []message.ContentPart) []string {
	var refs []string
	seen := make(map[string]bool)
	for _, p := range parts {
		if p.Type != "text" {
			continue
		}
		// Match <file path="..."> or <file path='...'>
		rest := p.Text
		for {
			start := strings.Index(rest, "<file path=")
			if start < 0 {
				break
			}
			rest = rest[start+len("<file path="):]
			if len(rest) == 0 {
				break
			}
			quote := rest[0]
			if quote != '"' && quote != '\'' {
				break
			}
			end := strings.IndexByte(rest[1:], quote)
			if end < 0 {
				break
			}
			path := rest[1 : end+1]
			rest = rest[end+2:]
			if path != "" && !seen[path] {
				seen[path] = true
				refs = append(refs, path)
			}
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
		if part.Type == "image" {
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
	return []message.ContentPart{{Type: "text", Text: d.Content}}
}

func normalizeDraftAgentID(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return "main"
	}
	return agentID
}

// Attachment holds a pending image to be sent with the next user message.
type Attachment struct {
	FileName  string
	MimeType  string
	Data      []byte
	ImagePath string
}

func attachmentExtForMimeType(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func attachmentsFromParts(parts []message.ContentPart) []Attachment {
	attachments := make([]Attachment, 0, len(parts))
	imageIndex := 0
	for _, part := range parts {
		if part.Type != "image" {
			continue
		}
		imageIndex++
		fileName := imagePartDisplayName(part.FileName, part.ImagePath, part.MimeType, imageIndex)
		attachments = append(attachments, Attachment{
			FileName:  fileName,
			MimeType:  part.MimeType,
			Data:      part.Data,
			ImagePath: part.ImagePath,
		})
	}
	return attachments
}

// slashCommand describes one slash command for autocomplete.
type slashCommand struct {
	Cmd  string // e.g. "/resume"
	Desc string // short description for the dropdown
}

// CustomCommand is the exported DTO used to inject custom slash commands into
// the TUI for autocomplete display only. The expansion is handled by the agent.
type CustomCommand struct {
	Cmd  string // must start with "/"
	Desc string // short description shown in the dropdown; may be empty
}

// slashCommands is the list of available slash commands (alphabetical by Cmd).
var slashCommands = []slashCommand{
	{Cmd: "/compact", Desc: "compact old context"},
	{Cmd: "/diagnostics", Desc: "export a diagnostics bundle"},
	{Cmd: "/export", Desc: "export session (Markdown/JSON)"},
	{Cmd: "/help", Desc: "show keyboard help"},
	{Cmd: "/model", Desc: "switch model"},
	{Cmd: "/new", Desc: "start a fresh session"},
	{Cmd: "/resume", Desc: "resume previous session"},
	{Cmd: "/rules", Desc: "manage permission rules"},
	{Cmd: "/stats", Desc: "usage statistics"},
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
	if m.focusedAgentID == "" && strings.HasPrefix("/loop", prefix) {
		if m.agent == nil || m.agent.CurrentLoopState() == "" {
			loopCommands = append(loopCommands, slashCommand{Cmd: "/loop on", Desc: "enable loop mode"})
		} else {
			loopCommands = append(loopCommands, slashCommand{Cmd: "/loop off", Desc: "disable loop mode"})
		}
	}
	out = append(out, loopCommands...)
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
	if m.slashCache.text != "" &&
		m.slashCache.width == m.width &&
		m.slashCache.theme == m.theme.Name &&
		m.slashCache.value == value &&
		m.slashCache.sel == sel {
		return m.slashCache.text
	}

	help := DimStyle.Render("Tab complete  ↑/↓ select  Esc close")

	// Calculate dynamic width based on content
	contentWidth := runewidth.StringWidth("Tab complete  ↑/↓ select  Esc close")
	for i := range maxVisible {
		c := matches[i]
		w := runewidth.StringWidth(fmt.Sprintf(" ▸ %s  %s", c.Cmd, c.Desc))
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

	for i := range maxVisible {
		c := matches[i]
		line := fmt.Sprintf("%s  %s", c.Cmd, c.Desc)
		if runewidth.StringWidth(line) > contentWidth-4 {
			line = runewidth.Truncate(line, contentWidth-4, "…")
		}

		if i == sel {
			styledLine := SelectedStyle.Width(contentWidth).Render(" ▸ " + line)
			lines = append(lines, styledLine)
		} else {
			lines = append(lines, "   "+line)
		}
	}

	body := strings.Join(lines, "\n")
	out := DirectoryBorderStyle.Width(contentWidth + 2).Render(body)
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
		m.customCommands = append(m.customCommands, slashCommand{Cmd: cmd, Desc: c.Desc})
	}
	sort.Slice(m.customCommands, func(i, j int) bool {
		return m.customCommands[i].Cmd < m.customCommands[j].Cmd
	})
}
