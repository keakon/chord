package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/agent"
)

// FileEdit records a single file-edit event (Write, Edit, or Delete tool call).
type FileEdit struct {
	Path    string // file path
	Added   int    // added lines
	Removed int    // removed lines
}

// SidebarEntry represents a single agent in the sidebar listing.
type SidebarEntry struct {
	ID                  string     // instance ID (e.g. "agent-1") or "main"
	AgentDefName        string     // SubAgent: agent config name (e.g. reviewer); empty for main
	TaskDesc            string     // task description (truncated for display)
	Status              string     // "running", "idle", "done", "error"
	Color               string     // optional ANSI color code for TUI display
	SelectedRef         string     // last known primary model ref (SubAgent only; may include @variant)
	RunningRef          string     // last known effective running ref (SubAgent only)
	EditedFiles         []FileEdit // recent file edits, in order
	Activity            string     // latest runtime activity/detail snapshot; AGENTS rows may choose not to render it
	LastSummary         string
	UrgentCount         int
	LastArtifactRelPath string
	LastArtifactType    string
}

// Sidebar displays active SubAgent status in a narrow panel to the left of
// the main viewport. It is only rendered when at least one SubAgent exists.
type Sidebar struct {
	agents       []SidebarEntry
	focusedID    string // instance ID of focused agent ("" or "main" = main agent)
	width        int
	theme        Theme
	pendingTasks int // Delegate tool calls that have started but whose SubAgent hasn't appeared yet
}

// DefaultSidebarWidth is the fixed width (in terminal columns) for the sidebar
// panel, including the border.
const DefaultSidebarWidth = 25

// NewSidebar creates a Sidebar with default settings.
func NewSidebar(theme Theme) Sidebar {
	return Sidebar{
		width: DefaultSidebarWidth,
		theme: theme,
	}
}

// Update refreshes the sidebar entries from the agent's SubAgent list and
// the currently focused agent ID. The main agent is always listed first.
// mainRole is the current role name (e.g. "builder", "planner") shown as
// the main agent's display label.
func (s *Sidebar) Update(subAgents []agent.SubAgentInfo, focusedID, mainRole string) {
	s.focusedID = focusedID
	existingByID := make(map[string]SidebarEntry, len(s.agents))
	for _, existing := range s.agents {
		existingByID[existing.ID] = existing
	}

	// Build a set of currently active agent IDs.
	activeIDs := make(map[string]bool, len(subAgents))
	for _, info := range subAgents {
		activeIDs[info.InstanceID] = true
	}

	entries := make([]SidebarEntry, 0, 1+len(subAgents))

	// Always include the main agent, labelled with its current role.
	mainLabel := mainRole
	if mainLabel == "" {
		mainLabel = "main"
	}
	mainEntry := SidebarEntry{
		ID:       "main",
		TaskDesc: mainLabel,
		Status:   "running",
		Activity: "Idle",
	}
	if existing, ok := existingByID["main"]; ok {
		mainEntry.EditedFiles = existing.EditedFiles
		mainEntry.Color = existing.Color
		if existing.Activity != "" {
			mainEntry.Activity = existing.Activity
		}
	}
	entries = append(entries, mainEntry)

	// Sort SubAgents by instance ID for stable ordering.
	sorted := make([]agent.SubAgentInfo, len(subAgents))
	copy(sorted, subAgents)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].InstanceID < sorted[j].InstanceID
	})

	for _, info := range sorted {
		status := strings.TrimSpace(info.State)
		if status == "" {
			status = "running"
		}
		// Look up the current status from the status cache if we have one.
		if cached, ok := s.findStatus(info.InstanceID); ok {
			status = cached
		}
		entry := SidebarEntry{
			ID:                  info.InstanceID,
			AgentDefName:        info.AgentDefName,
			TaskDesc:            info.TaskDesc,
			Status:              status,
			Color:               info.Color,
			SelectedRef:         info.SelectedRef,
			RunningRef:          info.RunningRef,
			LastSummary:         info.LastSummary,
			UrgentCount:         info.UrgentInboxCount,
			LastArtifactRelPath: info.LastArtifactRelPath,
			LastArtifactType:    info.LastArtifactType,
		}
		if existing, ok := existingByID[info.InstanceID]; ok {
			entry.EditedFiles = existing.EditedFiles
			entry.Activity = existing.Activity
		}
		entries = append(entries, entry)
	}

	// Keep main first, then prioritize more active sub-agents before quieter/finished ones.
	if len(entries) > 1 {
		subEntries := entries[1:]
		sort.SliceStable(subEntries, func(i, j int) bool {
			return sidebarStatusPriority(subEntries[i].Status) < sidebarStatusPriority(subEntries[j].Status)
		})
	}

	s.agents = entries
}

// sidebarStatusPriority returns a sort key for agent status strings.
// Lower values sort first (more active agents appear before quieter ones).
func sidebarStatusPriority(status string) int {
	switch status {
	case "streaming", "executing":
		return 0
	case "connecting", "waiting_headers", "waiting_token":
		return 1
	case "retrying", "retrying_key", "cooling":
		return 2
	case "waiting_primary", "waiting_descendant":
		return 3
	case "idle":
		return 4
	case "done", "completed":
		return 5
	case "cancelled":
		return 5
	default:
		return 6
	}
}

// UpdateStatus updates the status of a specific agent entry.
// If the agent is not found, a new entry is created so that status events
// arriving before or after refreshSidebar are not lost.
func (s *Sidebar) UpdateStatus(agentID, status string) {
	for i := range s.agents {
		if s.agents[i].ID == agentID {
			s.agents[i].Status = status
			switch status {
			case "done", "completed", "cancelled", "error", "waiting_primary", "waiting_descendant", "idle":
				s.agents[i].Activity = ""
			}
			return
		}
	}
	// Agent not in list yet (e.g. deleted from map before TUI processed the event).
	s.agents = append(s.agents, SidebarEntry{
		ID:     agentID,
		Status: status,
	})
}

// UpdateActivity stores an agent activity label for AGENTS panel rendering.
func (s *Sidebar) UpdateActivity(agentID, activity string) {
	agentID = normalizeSidebarAgentID(agentID)
	for i := range s.agents {
		if s.agents[i].ID == agentID {
			s.agents[i].Activity = activity
			return
		}
	}
	s.agents = append(s.agents, SidebarEntry{
		ID:       agentID,
		Status:   "running",
		Activity: activity,
	})
}

// FindStatus returns the cached status for the given agent ID.
func (s *Sidebar) FindStatus(agentID string) (string, bool) {
	return s.findStatus(agentID)
}

func (s *Sidebar) findStatus(agentID string) (string, bool) {
	for _, entry := range s.agents {
		if entry.ID == agentID {
			return entry.Status, true
		}
	}
	return "", false
}

// SubAgentModelRefs returns cached provider/model refs for a SubAgent (including
// completed agents removed from GetSubAgents). ok is false when the ID is unknown
// or no refs were recorded yet.
func (s *Sidebar) SubAgentModelRefs(agentID string) (selected, running string, ok bool) {
	if agentID == "" || agentID == "main" {
		return "", "", false
	}
	for _, e := range s.agents {
		if e.ID != agentID {
			continue
		}
		if e.SelectedRef == "" && e.RunningRef == "" {
			return "", "", false
		}
		sel, run := e.SelectedRef, e.RunningRef
		if run == "" {
			run = sel
		}
		return sel, run, true
	}
	return "", "", false
}

// SubAgentLabels returns cached display fields for a SubAgent sidebar row (including
// completed agents). ok is false if agentID is not a known sub entry.
func (s *Sidebar) SubAgentLabels(agentID string) (agentDefName, taskDesc string, ok bool) {
	if agentID == "" || agentID == "main" {
		return "", "", false
	}
	for _, e := range s.agents {
		if e.ID == agentID {
			return e.AgentDefName, e.TaskDesc, true
		}
	}
	return "", "", false
}

// AddFileEdit records a file edit event for the given agent.
// If the file was already edited, the line counts are accumulated.
func (s *Sidebar) AddFileEdit(agentID, filePath string, added, removed int) {
	agentID = normalizeSidebarAgentID(agentID)
	for i := range s.agents {
		if s.agents[i].ID != agentID {
			continue
		}
		// Accumulate into existing entry for the same file.
		for j := range s.agents[i].EditedFiles {
			if s.agents[i].EditedFiles[j].Path == filePath {
				s.agents[i].EditedFiles[j].Added += added
				s.agents[i].EditedFiles[j].Removed += removed
				return
			}
		}
		// Skip empty new files (no visible diff stats to show).
		if added == 0 && removed == 0 {
			return
		}
		// New file entry (cap at 50 to avoid unbounded growth).
		const maxEditedFiles = 50
		s.agents[i].EditedFiles = append(s.agents[i].EditedFiles, FileEdit{
			Path:    filePath,
			Added:   added,
			Removed: removed,
		})
		if len(s.agents[i].EditedFiles) > maxEditedFiles {
			s.agents[i].EditedFiles = s.agents[i].EditedFiles[len(s.agents[i].EditedFiles)-maxEditedFiles:]
		}
		return
	}
}

func normalizeSidebarAgentID(agentID string) string {
	if agentID == "" {
		return "main"
	}
	return agentID
}

// ClearFileEdits resets the edited files list for the given agent.
func (s *Sidebar) ClearFileEdits(agentID string) {
	agentID = normalizeSidebarAgentID(agentID)
	for i := range s.agents {
		if s.agents[i].ID == agentID {
			s.agents[i].EditedFiles = nil
			return
		}
	}
}

// CurrentAgentFiles returns the edited files for the currently focused agent.
func (s *Sidebar) CurrentAgentFiles() []FileEdit {
	focusedID := s.focusedID
	if focusedID == "" {
		focusedID = "main"
	}
	for _, entry := range s.agents {
		if entry.ID == focusedID {
			return entry.EditedFiles
		}
	}
	return nil
}

// AddPendingTask increments the pending-task counter, causing the sidebar to
// become visible immediately when a Delegate tool call starts.
func (s *Sidebar) AddPendingTask() {
	s.pendingTasks++
}

// ResolvePendingTask decrements the pending-task counter when a SubAgent
// transitions to "running" (meaning the placeholder is no longer needed).
func (s *Sidebar) ResolvePendingTask() {
	if s.pendingTasks > 0 {
		s.pendingTasks--
	}
}

// PendingTasks returns the current pending-task counter.
func (s *Sidebar) PendingTasks() int {
	return s.pendingTasks
}

// Agents returns the raw sidebar entries (for fingerprinting without rendering).
func (s *Sidebar) Agents() []SidebarEntry {
	return s.agents
}

// AgentsSummary returns a compact AGENTS header summary in done/total form.
// The main agent is excluded; pending task placeholders count toward total.
func (s Sidebar) AgentsSummary() string {
	total := s.pendingTasks
	done := 0
	for _, entry := range s.agents {
		if entry.ID == "main" {
			continue
		}
		total++
		switch entry.Status {
		case "done", "completed", "error", "cancelled":
			done++
		}
	}
	if total == 0 {
		return "0/0"
	}
	return fmt.Sprintf("%d/%d", done, total)
}
