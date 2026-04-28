package permission

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// AppendRoleOverlayRule appends a rule to a role overlay file.
// Creates the file and parent directories if they don't exist.
// Deduplicates: if the same (permission, pattern, action) already exists, no-op.
// Uses atomic write: writes to a temp file then renames.
func AppendRoleOverlayRule(path string, rule Rule) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Read existing file if it exists
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var root yaml.Node
	if len(data) > 0 {
		if unmarshalErr := yaml.Unmarshal(data, &root); unmarshalErr != nil {
			return unmarshalErr
		}
	}

	// Ensure we have a document node with a mapping node
	ensureOverlayYAMLStructure(&root)
	if err := validateOverlayPermissionRoot(&root); err != nil {
		return err
	}
	permNode := overlayPermissionMappingNode(&root, true)
	if permNode == nil {
		return fmt.Errorf("invalid overlay yaml structure")
	}

	// Find or create the tool mapping
	toolNode := findOrCreateMappingKey(permNode, rule.Permission)

	// Check for duplicate
	if ruleExistsInNode(toolNode, rule.Pattern, rule.Action) {
		return nil // already exists
	}

	// Add the rule
	toolNode.Content = append(toolNode.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: rule.Pattern},
		&yaml.Node{Kind: yaml.ScalarNode, Value: string(rule.Action)},
	)

	// Atomic write
	tmpPath := path + ".tmp"
	out, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmpPath, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// RemoveRoleOverlayRule removes the last matching rule from a role overlay file.
// Uses atomic write.
func RemoveRoleOverlayRule(path string, rule Rule) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to remove
		}
		return err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}

	// Find permission mapping node.
	permNode := overlayPermissionMappingNode(&root, false)
	if permNode == nil || permNode.Kind != yaml.MappingNode {
		return nil
	}

	// Find the tool key
	for i := 0; i < len(permNode.Content); i += 2 {
		key := permNode.Content[i]
		val := permNode.Content[i+1]
		if key.Value != rule.Permission {
			continue
		}
		if val.Kind != yaml.MappingNode {
			continue
		}
		// Search in reverse for the rule (last-match-wins)
		for j := len(val.Content) - 2; j >= 0; j -= 2 {
			patternNode := val.Content[j]
			actionNode := val.Content[j+1]
			if patternNode.Value == rule.Pattern && actionNode.Value == string(rule.Action) {
				// Remove this pair
				val.Content = append(val.Content[:j], val.Content[j+2:]...)
				// If tool mapping is now empty, remove the tool key too
				if len(val.Content) == 0 {
					permNode.Content = append(permNode.Content[:i], permNode.Content[i+2:]...)
				}
				// Atomic write
				tmpPath := path + ".tmp"
				out, err := yaml.Marshal(&root)
				if err != nil {
					return err
				}
				if err := os.WriteFile(tmpPath, out, 0o644); err != nil {
					return err
				}
				return os.Rename(tmpPath, path)
			}
		}
	}
	return nil
}

// ensureOverlayYAMLStructure ensures the yaml.Node has a document → mapping structure.
func ensureOverlayYAMLStructure(root *yaml.Node) {
	if root.Kind == 0 {
		root.Kind = yaml.DocumentNode
	}
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			root.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
		}
		if root.Content[0].Kind == 0 {
			root.Content[0].Kind = yaml.MappingNode
		}
	}
}

func validateOverlayPermissionRoot(root *yaml.Node) error {
	if root == nil {
		return nil
	}
	var mapping *yaml.Node
	switch {
	case root.Kind == yaml.DocumentNode && len(root.Content) > 0:
		mapping = root.Content[0]
	case root.Kind == yaml.MappingNode:
		mapping = root
	default:
		return nil
	}
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	if len(mapping.Content) == 0 {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key != nil && key.Value == "permission" {
			return nil
		}
	}
	return fmt.Errorf("overlay file must use root \"permission\" mapping")
}

// overlayPermissionMappingNode returns the mapping that holds tool permissions.
// Required schema is:
// permission:
//
//	Bash:
//	  "git *": allow
func overlayPermissionMappingNode(root *yaml.Node, create bool) *yaml.Node {
	if root == nil {
		return nil
	}
	var mapping *yaml.Node
	switch {
	case root.Kind == yaml.DocumentNode && len(root.Content) > 0:
		mapping = root.Content[0]
	case root.Kind == yaml.MappingNode:
		mapping = root
	default:
		return nil
	}
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		v := mapping.Content[i+1]
		if k.Value != "permission" {
			continue
		}
		if v.Kind == yaml.MappingNode {
			return v
		}
		if create {
			v.Kind = yaml.MappingNode
			v.Content = nil
			return v
		}
		return nil
	}

	if !create {
		return nil
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "permission"}
	valNode := &yaml.Node{Kind: yaml.MappingNode}
	mapping.Content = append(mapping.Content, keyNode, valNode)
	return valNode
}

// findOrCreateMappingKey finds a key in a mapping node, or creates it.
func findOrCreateMappingKey(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}

	// Create new key-value pair
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
	valNode := &yaml.Node{Kind: yaml.MappingNode}
	mapping.Content = append(mapping.Content, keyNode, valNode)
	return valNode
}

// ruleExistsInNode checks if a (pattern, action) pair exists in a mapping node.
func ruleExistsInNode(node *yaml.Node, pattern string, action Action) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == pattern && node.Content[i+1].Value == string(action) {
			return true
		}
	}
	return false
}
