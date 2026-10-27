package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const (
	skillDiscoveryMaxBytes          = 8 * 1024
	skillDiscoveryMaxEntries        = 32
	skillDiscoveryEpochMaxBytes     = 16 * 1024
	skillDiscoveryEpochMaxEntries   = 64
	skillDiscoveryRestoreMaxBytes   = 2 * 1024
	skillDiscoveryRestoreMaxEntries = 8
	skillDiscoveryDescriptionRunes  = 512
)
const skillDiscoveryPreamble = "The files read match these optional skills. Load a skill with skill(name=...) when its workflow is relevant. This notice does not load instructions or grant permissions.\n"

func buildSkillDiscoveryMessage(history []message.Message, view skillActivationView) *message.Message {
	// Never insert synthetic user content inside a local tool round.
	open := make(map[string]bool)
	for _, msg := range history {
		if msg.Role == message.RoleAssistant {
			for _, call := range msg.ToolCalls {
				open[call.ID] = true
			}
		}
		if msg.Role == message.RoleTool {
			delete(open, msg.ToolCallID)
		}
	}
	if len(open) > 0 {
		return nil
	}
	epoch := skillActivationEpoch(history, view.TaskID)
	usedBytes, usedEntries := 0, 0
	recorded := make(map[string]bool)
	ids := make(map[string]bool)
	restoreRecorded := false
	for _, msg := range history {
		n := msg.SkillDiscovery
		if n == nil || n.TaskID != view.TaskID || n.CheckoutRoot != view.CheckoutRoot || ids[n.ID] {
			continue
		}
		ids[n.ID] = true
		usedBytes += len(msg.Content)
		usedEntries += len(n.Skills)
		if n.ContextEpoch == epoch && n.Mode == message.SkillDiscoveryModeRestore {
			restoreRecorded = true
		}
		if validSkillDiscoveryMessage(msg, view) {
			for _, ref := range n.Skills {
				recorded[skillRefKey(ref)] = true
			}
		}
	}
	remainingBytes := skillDiscoveryEpochMaxBytes - usedBytes
	remainingEntries := skillDiscoveryEpochMaxEntries - usedEntries
	if remainingBytes <= len(skillDiscoveryPreamble) || remainingEntries <= 0 {
		return nil
	}
	bodyNames := make(map[string]bool)
	for _, meta := range rebuildInvokedSkillsFromMessages(history, view.Catalog) {
		if meta != nil {
			bodyNames[meta.Name] = true
		}
	}
	facts := collectSkillActivationFacts(history, view)
	var reads, restores []message.SkillActivationRef
	seen := make(map[string]bool)
	for _, f := range slices.Backward(facts) {
		for _, ref := range f.Skills {
			key := skillRefKey(ref)
			if seen[key] || recorded[key] || bodyNames[ref.Name] {
				continue
			}
			seen[key] = true
			if f.ContextEpoch == epoch && f.SourceCallID != "" {
				reads = append(reads, ref)
			} else {
				restores = append(restores, ref)
			}
		}
	}
	mode := message.SkillDiscoveryModeRead
	candidates := reads
	maxBytes := min(skillDiscoveryMaxBytes, remainingBytes)
	maxEntries := min(skillDiscoveryMaxEntries, remainingEntries)
	if len(candidates) == 0 {
		if restoreRecorded || epoch == 0 {
			return nil
		}
		candidates = restores
		mode = message.SkillDiscoveryModeRestore
		maxBytes = min(maxBytes, skillDiscoveryRestoreMaxBytes)
		maxEntries = min(maxEntries, skillDiscoveryRestoreMaxEntries)
	}
	if len(candidates) == 0 {
		return nil
	}
	// The durable content carries the <system-reminder> wrapper; reserve its
	// fixed length so the wrapped message stays within the same byte budget the
	// bare body was measured against.
	maxBytes -= len(systemReminderOverlayText(""))
	descriptions := make(map[string]string)
	for _, meta := range view.Catalog {
		if meta != nil {
			descriptions[meta.Name] = meta.Description
		}
	}
	var content strings.Builder
	content.WriteString(skillDiscoveryPreamble)
	reserve := len(fmt.Sprintf("%d additional matched skills are omitted by the discovery budget.\n", len(candidates)))
	var refs []message.SkillActivationRef
	for _, ref := range candidates {
		name, _ := json.Marshal(ref.Name)
		description, _ := json.Marshal(tools.TruncateSkillDescription(descriptions[ref.Name], skillDiscoveryDescriptionRunes))
		line := fmt.Sprintf("- name: %s; description: %s\n", name, description)
		if len(refs) >= maxEntries || content.Len()+len(line)+reserve > maxBytes {
			continue
		}
		content.WriteString(line)
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil
	}
	omitted := len(candidates) - len(refs)
	if omitted > 0 {
		fmt.Fprintf(&content, "%d additional matched skills are omitted by the discovery budget.\n", omitted)
	}
	record := &message.SkillDiscoveryRecord{TaskID: view.TaskID, CheckoutRoot: view.CheckoutRoot, ContextEpoch: epoch, Mode: mode, Skills: refs, Omitted: omitted}
	record.ID = skillDiscoveryID(record)
	// The notice is harness-generated durable user content, so it carries the
	// <system-reminder> wrapper the content-trust prompt defines; otherwise a
	// replayed row would read as user speech. The body is trimmed first: the
	// listing lines end in newlines, and a doubled newline at the wrapper seam
	// would defeat the blank-line escape guard below it.
	return &message.Message{Role: message.RoleUser, Kind: message.KindSkillDiscovery, Content: systemReminderOverlayText(strings.TrimSpace(content.String())), SkillDiscovery: record}
}
