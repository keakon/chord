package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/permission"
)

// UpsertAgentPermissionRule inserts or updates a permission rule in an agent YAML file.
// It preserves existing YAML comments via yaml.Node, but the file is re-encoded with
// standard indentation. Markdown-frontmatter agents are intentionally not modified by
// this helper; use YAML agent files for runtime-persisted permission changes.
func UpsertAgentPermissionRule(path string, rule permission.Rule) (bool, error) {
	return UpsertAgentPermissionRuleForAgent(path, nil, rule)
}

// UpsertAgentPermissionRuleForAgent inserts or updates a permission rule in an
// agent YAML file. When the file does not exist, base is used to create a full
// agent document before adding the rule so built-in/default agent metadata and
// permissions are not lost.
func UpsertAgentPermissionRuleForAgent(path string, base *AgentConfig, rule permission.Rule) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, fmt.Errorf("agent config path is empty")
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".yaml" && ext != ".yml" {
		return false, fmt.Errorf("agent config %s is not a YAML file", path)
	}

	lock, err := LockConfigMutation(path)
	if err != nil {
		return false, err
	}
	defer lock.Close()

	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		root := newAgentPermissionDocumentFromBase(base, rule)
		out, encErr := encodeYAMLNode(root)
		if encErr != nil {
			return false, encErr
		}
		return true, writeConfigFileAtomicallyReplace(path, out, 0o600)
	case err != nil:
		return false, fmt.Errorf("read agent config %s: %w", path, err)
	}

	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&root); err != nil {
		return false, fmt.Errorf("decode agent config yaml %s: %w", path, err)
	}
	if root.Kind == 0 {
		root = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	mapping := documentMapping(&root)
	if mapping == nil {
		return false, fmt.Errorf("agent config %s must be a YAML mapping", path)
	}

	changed, err := upsertPermissionRuleNode(mapping, rule)
	if err != nil || !changed {
		return changed, err
	}
	out, err := encodeYAMLNode(&root)
	if err != nil {
		return false, err
	}
	return true, writeConfigFileAtomicallyReplace(path, out, 0o600)
}

// RemoveAgentPermissionRule removes an exact permission rule from an agent YAML file.
func RemoveAgentPermissionRule(path string, rule permission.Rule) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, fmt.Errorf("agent config path is empty")
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".yaml" && ext != ".yml" {
		return false, fmt.Errorf("agent config %s is not a YAML file", path)
	}

	lock, err := LockConfigMutation(path)
	if err != nil {
		return false, err
	}
	defer lock.Close()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read agent config %s: %w", path, err)
	}
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&root); err != nil {
		return false, fmt.Errorf("decode agent config yaml %s: %w", path, err)
	}
	mapping := documentMapping(&root)
	if mapping == nil {
		return false, fmt.Errorf("agent config %s must be a YAML mapping", path)
	}
	changed := removePermissionRuleNode(mapping, rule)
	if !changed {
		return false, nil
	}
	out, err := encodeYAMLNode(&root)
	if err != nil {
		return false, err
	}
	return true, writeConfigFileAtomicallyReplace(path, out, 0o600)
}

func removePermissionRuleNode(agentMapping *yaml.Node, rule permission.Rule) bool {
	permNode := mappingValueNode(agentMapping, "permission")
	if permNode == nil || permNode.Kind != yaml.MappingNode {
		return false
	}
	toolNode := mappingValueNode(permNode, rule.Permission)
	if toolNode == nil || toolNode.Kind != yaml.MappingNode {
		return false
	}
	for i := len(toolNode.Content) - 2; i >= 0; i -= 2 {
		keyNode := toolNode.Content[i]
		valueNode := toolNode.Content[i+1]
		if keyNode != nil && keyNode.Value == rule.Pattern && valueNode != nil && valueNode.Value == string(rule.Action) {
			toolNode.Content = append(toolNode.Content[:i], toolNode.Content[i+2:]...)
			return true
		}
	}
	return false
}

func newAgentPermissionDocument(rule permission.Rule) *yaml.Node {
	mapping := &yaml.Node{Kind: yaml.MappingNode}
	_, _ = upsertPermissionRuleNode(mapping, rule)
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{mapping}}
}

func newAgentPermissionDocumentFromBase(base *AgentConfig, rule permission.Rule) *yaml.Node {
	if base == nil {
		return newAgentPermissionDocument(rule)
	}
	mapping := &yaml.Node{Kind: yaml.MappingNode}
	appendScalarField(mapping, "name", strings.TrimSpace(base.Name))
	appendScalarField(mapping, "description", strings.TrimSpace(base.Description))
	appendScalarField(mapping, "mode", strings.TrimSpace(base.Mode))
	appendStringSequenceField(mapping, "model_pools", base.ModelPools)
	appendScalarField(mapping, "variant", strings.TrimSpace(base.Variant))
	appendScalarField(mapping, "color", strings.TrimSpace(base.Color))
	appendStringSequenceField(mapping, "capabilities", base.Capabilities)
	appendStringSequenceField(mapping, "preferred_tasks", base.PreferredTasks)
	appendScalarField(mapping, "write_mode", strings.TrimSpace(base.WriteMode))
	appendScalarField(mapping, "delegation_policy", strings.TrimSpace(base.DelegationPolicy))
	if base.Permission.Kind != 0 {
		mapping.Content = append(mapping.Content, scalarNode("permission"), cloneYAMLNode(&base.Permission))
	}
	appendScalarField(mapping, "prompt", strings.TrimSpace(base.Prompt))
	appendScalarField(mapping, "system_prompt", strings.TrimSpace(base.PromptAlt))
	_, _ = upsertPermissionRuleNode(mapping, rule)
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{mapping}}
}

func appendScalarField(mapping *yaml.Node, key, value string) {
	if mapping == nil || strings.TrimSpace(value) == "" {
		return
	}
	mapping.Content = append(mapping.Content, scalarNode(key), scalarNode(value))
}

func appendStringSequenceField(mapping *yaml.Node, key string, values []string) {
	if mapping == nil || len(values) == 0 {
		return
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		seq.Content = append(seq.Content, scalarNode(value))
	}
	if len(seq.Content) == 0 {
		return
	}
	mapping.Content = append(mapping.Content, scalarNode(key), seq)
}

func cloneYAMLNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	clone := *n
	if len(n.Content) > 0 {
		clone.Content = make([]*yaml.Node, len(n.Content))
		for i, child := range n.Content {
			clone.Content[i] = cloneYAMLNode(child)
		}
	}
	return &clone
}

func documentMapping(root *yaml.Node) *yaml.Node {
	if root == nil {
		return nil
	}
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 || root.Content[0] == nil {
			root.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
		}
		if root.Content[0].Kind != yaml.MappingNode {
			return nil
		}
		return root.Content[0]
	}
	if root.Kind == yaml.MappingNode {
		return root
	}
	return nil
}

func upsertPermissionRuleNode(agentMapping *yaml.Node, rule permission.Rule) (bool, error) {
	if strings.TrimSpace(rule.Permission) == "" || strings.TrimSpace(rule.Pattern) == "" || strings.TrimSpace(string(rule.Action)) == "" {
		return false, fmt.Errorf("permission rule is incomplete")
	}
	permNode := mappingValueNode(agentMapping, "permission")
	changed := false
	if permNode == nil {
		permNode = &yaml.Node{Kind: yaml.MappingNode}
		agentMapping.Content = append(agentMapping.Content, scalarNode("permission"), permNode)
		changed = true
	} else if permNode.Kind == yaml.ScalarNode {
		permNode.Kind = yaml.MappingNode
		permNode.Tag = "!!map"
		permNode.Content = []*yaml.Node{scalarNode("*"), scalarNode(permNode.Value)}
		permNode.Value = ""
		changed = true
	} else if permNode.Kind != yaml.MappingNode {
		return false, fmt.Errorf("permission must be a scalar or mapping")
	}

	toolNode := mappingValueNode(permNode, rule.Permission)
	if toolNode == nil {
		permNode.Content = append(permNode.Content, scalarNode(rule.Permission), &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalarNode(rule.Pattern), scalarNode(string(rule.Action))}})
		return true, nil
	}
	if toolNode.Kind == yaml.ScalarNode {
		oldAction := toolNode.Value
		toolNode.Kind = yaml.MappingNode
		toolNode.Tag = "!!map"
		toolNode.Content = []*yaml.Node{scalarNode("*"), scalarNode(oldAction)}
		toolNode.Value = ""
		changed = true
	} else if toolNode.Kind != yaml.MappingNode {
		return false, fmt.Errorf("permission.%s must be a scalar or mapping", rule.Permission)
	}

	for i := 0; i+1 < len(toolNode.Content); i += 2 {
		keyNode := toolNode.Content[i]
		valueNode := toolNode.Content[i+1]
		if keyNode != nil && keyNode.Value == rule.Pattern {
			if valueNode != nil && valueNode.Kind == yaml.ScalarNode && valueNode.Value == string(rule.Action) {
				return changed, nil
			}
			if valueNode == nil {
				toolNode.Content[i+1] = scalarNode(string(rule.Action))
			} else {
				valueNode.Kind = yaml.ScalarNode
				valueNode.Tag = "!!str"
				valueNode.Value = string(rule.Action)
				valueNode.Content = nil
			}
			return true, nil
		}
	}
	toolNode.Content = append(toolNode.Content, scalarNode(rule.Pattern), scalarNode(string(rule.Action)))
	return true, nil
}

func mappingValueNode(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i] != nil && mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func encodeYAMLNode(root *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		_ = enc.Close()
		return nil, fmt.Errorf("encode agent config yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close yaml encoder: %w", err)
	}
	return buf.Bytes(), nil
}
