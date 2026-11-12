package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// collectMaintenanceCleanupIssues validates maintenance.cleanup constraints
// YAML decoding cannot express and resets offending policies to disabled, so
// an invalid value never rides through to a destructive sweep.
func collectMaintenanceCleanupIssues(cfg *Config) []string {
	c := &cfg.Maintenance.Cleanup
	var issues []string
	if issue := normalizeRetainPolicy(&c.Sessions, "sessions"); issue != "" {
		issues = append(issues, issue)
	}
	if issue := normalizeRetainPolicy(&c.LLMDumps, "llm_dumps"); issue != "" {
		issues = append(issues, issue)
	}
	return issues
}

func normalizeRetainPolicy(p *MaintenanceRetainConfig, name string) string {
	if !p.Enabled && p.RetainDays == 0 {
		return ""
	}
	if p.RetainDays < 1 || p.RetainDays > MaxMaintenanceRetainDays {
		issue := fmt.Sprintf("maintenance.cleanup.%s.retain_days must be within [1, %d]; disabling the policy", name, MaxMaintenanceRetainDays)
		*p = MaintenanceRetainConfig{}
		return issue
	}
	return ""
}

// cleanupDecodeIssues validates the effective maintenance.cleanup mapping,
// including YAML aliases and merges. The main decode is partial by design —
// a type error or unknown key inside cleanup leaves the surrounding fields at
// their defaults, which for an explicit `enabled: true` would mean deleting
// with a retention period the user never configured. Any error in one policy's
// subtree disables that policy; an error at the cleanup level disables both.
func cleanupDecodeIssues(data []byte) (invalidSessions, invalidDumps bool, messages []string) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		// Malformed YAML is a fatal error elsewhere; nothing to add here.
		return false, false, nil
	}
	if len(root.Content) == 0 {
		return false, false, nil
	}
	cleanupNode, err := cleanupMappingValue(root.Content[0], "maintenance", "cleanup")
	if err != nil {
		return true, true, []string{fmt.Sprintf("maintenance.cleanup: %v", err)}
	}
	if cleanupNode == nil {
		return false, false, nil
	}
	entries, err := cleanupMappingEntries(cleanupNode)
	if err != nil {
		return true, true, []string{fmt.Sprintf("maintenance.cleanup: %v", err)}
	}
	for _, policy := range entries {
		var invalid *bool
		switch policy.Key {
		case "sessions":
			invalid = &invalidSessions
		case "llm_dumps":
			invalid = &invalidDumps
		default:
			return true, true, []string{fmt.Sprintf("maintenance.cleanup: unknown field %q", policy.Key)}
		}
		fields, err := cleanupMappingEntries(policy.Val)
		if err == nil {
			for _, field := range fields {
				switch field.Key {
				case "enabled":
					var value bool
					err = field.Val.Decode(&value)
				case "retain_days":
					var value int
					err = field.Val.Decode(&value)
					if err == nil && (value < 1 || value > MaxMaintenanceRetainDays) {
						err = fmt.Errorf("retain_days must be within [1, %d]", MaxMaintenanceRetainDays)
					}
				default:
					err = fmt.Errorf("unknown field %q", field.Key)
				}
				if err != nil {
					break
				}
			}
		}
		if err != nil {
			*invalid = true
			messages = append(messages, fmt.Sprintf("maintenance.cleanup.%s: %v", policy.Key, err))
		}
	}
	return invalidSessions, invalidDumps, messages
}

func cleanupMappingValue(node *yaml.Node, keys ...string) (*yaml.Node, error) {
	for _, key := range keys {
		entries, err := cleanupMappingEntries(node)
		if err != nil {
			return nil, err
		}
		var next *yaml.Node
		for _, entry := range entries {
			if entry.Key == key {
				next = entry.Val
				break
			}
		}
		if next == nil {
			return nil, nil
		}
		node = next
	}
	return node, nil
}

func cleanupMappingEntries(node *yaml.Node) ([]YAMLMappingEntry, error) {
	entries, err := YAMLMappingEntries(node)
	if err != nil {
		return nil, err
	}
	for node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected a mapping")
	}
	return entries, nil
}
