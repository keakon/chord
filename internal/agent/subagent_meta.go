package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/tools"
)

type subAgentMeta struct {
	InstanceID              string              `json:"instance_id"`
	TaskID                  string              `json:"task_id"`
	AgentDefName            string              `json:"agent_def_name,omitempty"`
	TaskDesc                string              `json:"task_desc,omitempty"`
	OwnerAgentID            string              `json:"owner_agent_id,omitempty"`
	OwnerTaskID             string              `json:"owner_task_id,omitempty"`
	Depth                   int                 `json:"depth,omitempty"`
	State                   string              `json:"state,omitempty"`
	LastSummary             string              `json:"last_summary,omitempty"`
	PendingCompleteIntent   bool                `json:"pending_complete_intent,omitempty"`
	PendingCompleteSummary  string              `json:"pending_complete_summary,omitempty"`
	PendingCompleteEnvelope *CompletionEnvelope `json:"pending_complete_envelope,omitempty"`
	LastMailboxID           string              `json:"last_mailbox_id,omitempty"`
	LastReplyMessageID      string              `json:"last_reply_message_id,omitempty"`
	LastReplyToMailboxID    string              `json:"last_reply_to_mailbox_id,omitempty"`
	LastReplyKind           string              `json:"last_reply_kind,omitempty"`
	LastReplySummary        string              `json:"last_reply_summary,omitempty"`
	LastArtifact            tools.ArtifactRef   `json:"last_artifact,omitempty"`
	UpdatedAt               time.Time           `json:"updated_at"`
}

func subAgentMetaPath(sessionDir, instanceID string) string {
	sessionDir = strings.TrimSpace(sessionDir)
	instanceID = strings.TrimSpace(instanceID)
	if sessionDir == "" || instanceID == "" {
		return ""
	}
	return filepath.Join(sessionDir, "subagents", instanceID+".meta.json")
}

func (a *MainAgent) persistSubAgentMeta(sub *SubAgent) {
	if a == nil || sub == nil {
		return
	}
	path := subAgentMetaPath(a.sessionDir, sub.instanceID)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	state := sub.State()
	summary := sub.LastSummary()
	lastMailboxID := sub.LastMailboxID()
	lastReplyMessageID, lastReplyToMailboxID, lastReplyKind, lastReplySummary := sub.LastReplyThread()
	lastArtifact := sub.LastArtifact()
	pendingComplete := sub.PendingCompleteIntent()
	meta := subAgentMeta{
		InstanceID:            sub.instanceID,
		TaskID:                sub.taskID,
		AgentDefName:          sub.agentDefName,
		TaskDesc:              sub.taskDesc,
		OwnerAgentID:          sub.ownerAgentID,
		OwnerTaskID:           sub.ownerTaskID,
		Depth:                 sub.depth,
		State:                 string(state),
		LastSummary:           summary,
		PendingCompleteIntent: pendingComplete != nil,
		LastMailboxID:         lastMailboxID,
		LastReplyMessageID:    lastReplyMessageID,
		LastReplyToMailboxID:  lastReplyToMailboxID,
		LastReplyKind:         lastReplyKind,
		LastReplySummary:      lastReplySummary,
		LastArtifact:          lastArtifact,
		UpdatedAt:             time.Now(),
	}
	if pendingComplete != nil {
		meta.PendingCompleteSummary = pendingComplete.Summary
		meta.PendingCompleteEnvelope = normalizeCompletionEnvelope(pendingComplete.Envelope)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	_ = os.WriteFile(path, data, 0o644)
}

func loadSubAgentMeta(sessionDir, instanceID string) (*subAgentMeta, error) {
	path := subAgentMetaPath(sessionDir, instanceID)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var meta subAgentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func listSubAgentMetaIDs(sessionDir string) []string {
	dir := filepath.Join(strings.TrimSpace(sessionDir), "subagents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if !strings.HasSuffix(name, ".meta.json") {
			continue
		}
		id := strings.TrimSuffix(name, ".meta.json")
		if strings.TrimSpace(id) == "" {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
