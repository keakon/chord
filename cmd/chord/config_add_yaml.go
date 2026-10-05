package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
)

// configAddYAML edits one path without mutating settings shared through YAML
// aliases or merge keys. Untouched anchors retain their location and spelling.
type configAddYAML struct {
	doc *yaml.Node
}

func directConfigValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func effectiveConfigValue(n *yaml.Node, key string) (*yaml.Node, error) {
	entries, err := config.YAMLMappingEntries(n)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Key == key {
			return entry.Val, nil
		}
	}
	return nil, nil
}

func cloneConfigValue(n *yaml.Node, visiting map[*yaml.Node]bool) (*yaml.Node, error) {
	if n == nil || visiting[n] {
		return nil, fmt.Errorf("cannot copy recursive YAML aliases")
	}
	visiting[n] = true
	defer delete(visiting, n)
	if n.Kind == yaml.AliasNode {
		copy, err := cloneConfigValue(n.Alias, visiting)
		if err != nil {
			return nil, err
		}
		if n.HeadComment != "" {
			copy.HeadComment = n.HeadComment
		}
		if n.LineComment != "" {
			copy.LineComment = n.LineComment
		}
		if n.FootComment != "" {
			copy.FootComment = n.FootComment
		}
		return copy, nil
	}
	copy := *n
	copy.Anchor, copy.Alias, copy.Content = "", nil, nil
	for _, child := range n.Content {
		cloned, err := cloneConfigValue(child, visiting)
		if err != nil {
			return nil, err
		}
		copy.Content = append(copy.Content, cloned)
	}
	return &copy, nil
}

// isolate preserves every other alias user's current value before its anchor
// is edited. Alias uses become independent values; unedited templates stay shared.
func (e configAddYAML) isolate(target *yaml.Node) error {
	if target.Anchor == "" {
		return nil
	}
	var walk func(*yaml.Node) error
	walk = func(n *yaml.Node) error {
		if n.Kind == yaml.AliasNode && n.Alias == target {
			copy, err := cloneConfigValue(n, make(map[*yaml.Node]bool))
			if err != nil {
				return err
			}
			*n = *copy
			return nil
		}
		for _, child := range n.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(e.doc)
}

func (e configAddYAML) child(mapping *yaml.Node, key string, kind yaml.Kind, tag string) (*yaml.Node, error) {
	if err := e.isolate(mapping); err != nil {
		return nil, err
	}
	existing := directConfigValue(mapping, key)
	if existing == nil {
		inherited, err := effectiveConfigValue(mapping, key)
		if err != nil {
			return nil, err
		}
		if inherited != nil {
			existing, err = cloneConfigValue(inherited, make(map[*yaml.Node]bool))
			if err != nil {
				return nil, err
			}
		} else {
			existing = &yaml.Node{Kind: kind, Tag: tag}
		}
		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, existing)
	} else if existing.Kind == yaml.AliasNode {
		copy, err := cloneConfigValue(existing, make(map[*yaml.Node]bool))
		if err != nil {
			return nil, err
		}
		*existing = *copy
	}
	if err := e.isolate(existing); err != nil {
		return nil, err
	}
	if existing.Tag == "!!null" {
		*existing = yaml.Node{Kind: kind, Tag: tag, HeadComment: existing.HeadComment, LineComment: existing.LineComment}
	}
	if existing.Kind != kind {
		return nil, fmt.Errorf("config key %q at line %d must be %s", key, existing.Line, strings.TrimPrefix(tag, "!!"))
	}
	return existing, nil
}

func (e configAddYAML) scalar(mapping *yaml.Node, key, value string) error {
	if err := e.isolate(mapping); err != nil {
		return err
	}
	if existing := directConfigValue(mapping, key); existing != nil {
		if existing.Kind != yaml.ScalarNode && existing.Kind != yaml.AliasNode {
			return fmt.Errorf("config key %q at line %d must be a scalar", key, existing.Line)
		}
		if err := e.isolate(existing); err != nil {
			return err
		}
		*existing = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, HeadComment: existing.HeadComment, LineComment: existing.LineComment, FootComment: existing.FootComment}
		return nil
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	return nil
}
