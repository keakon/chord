package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
)

func skillDiscoveryID(record *message.SkillDiscoveryRecord) string {
	refs := slices.Clone(record.Skills)
	slices.SortFunc(refs, func(a, b message.SkillActivationRef) int {
		return strings.Compare(skillRefKey(a)+a.Name, skillRefKey(b)+b.Name)
	})
	data, _ := json.Marshal(struct {
		Task, Root string
		Epoch      uint64
		Mode       string
		Refs       []message.SkillActivationRef
	}{record.TaskID, record.CheckoutRoot, record.ContextEpoch, record.Mode, refs})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func validSkillDiscoveryMessage(msg message.Message, view skillActivationView) bool {
	n := msg.SkillDiscovery
	if msg.Role != message.RoleUser || msg.Kind != message.KindSkillDiscovery || n == nil || n.TaskID != view.TaskID || n.CheckoutRoot != view.CheckoutRoot || len(n.Skills) == 0 || n.ID != skillDiscoveryID(n) || (n.Mode != message.SkillDiscoveryModeRead && n.Mode != message.SkillDiscoveryModeRestore) {
		return false
	}
	for _, ref := range n.Skills {
		if !view.validRef(ref) {
			return false
		}
	}
	return true
}
func projectSkillDiscoveryMessages(history []message.Message, view skillActivationView) []message.Message {
	// Copy-on-write: most histories carry no skill discovery rows, so probe
	// before allocating the dedup state and the full request-surface copy.
	hasDiscovery := false
	for i := range history {
		if history[i].Kind == message.KindSkillDiscovery || history[i].SkillDiscovery != nil {
			hasDiscovery = true
			break
		}
	}
	if !hasDiscovery {
		return history
	}
	out := make([]message.Message, 0, len(history))
	seen := make(map[string]string)
	conflicts := make(map[string]bool)
	for _, msg := range history {
		if n := msg.SkillDiscovery; n != nil {
			if text, ok := seen[n.ID]; ok && text != msg.Content {
				conflicts[n.ID] = true
			}
			seen[n.ID] = msg.Content
		}
	}
	clear(seen)
	for _, msg := range history {
		if msg.Kind == message.KindSkillDiscovery || msg.SkillDiscovery != nil {
			if !validSkillDiscoveryMessage(msg, view) || conflicts[msg.SkillDiscovery.ID] {
				continue
			}
			if _, ok := seen[msg.SkillDiscovery.ID]; ok {
				continue
			}
			seen[msg.SkillDiscovery.ID] = msg.Content
		}
		out = append(out, msg)
	}
	return out
}
