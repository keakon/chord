package main

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
)

func editConfigYAMLForCatalogAdvisory(current []byte, advisory config.CatalogConfigAdvisory, accept string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(current, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	root, err := documentRootMapping(&doc)
	if err != nil {
		return nil, err
	}
	model, err := directYAMLPath(root, []string{
		"providers", advisory.Provider, "models", advisory.Model,
	})
	if err != nil {
		return nil, err
	}
	if model.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("model %q is not a YAML mapping", advisory.Provider+"/"+advisory.Model)
	}
	fieldParts := splitCatalogAdvisoryField(advisory.Field)
	if len(fieldParts) == 0 {
		return nil, fmt.Errorf("advisory field is empty")
	}
	editor := configAddYAML{doc: &doc}
	if accept == "pin" {
		parent, key, err := directYAMLParent(model, fieldParts)
		if err != nil {
			return nil, err
		}
		value, err := catalogAdvisoryYAMLScalar(advisory.Recommended)
		if err != nil {
			return nil, err
		}
		if err := setDirectYAMLValue(editor, parent, key, value); err != nil {
			return nil, err
		}
	} else if accept == "follow-catalog" {
		if err := removeDirectYAMLPath(editor, model, fieldParts); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("unknown catalog advisory action %q", accept)
	}

	return encodeConfigYAMLDocument(&doc)
}

// encodeConfigYAMLDocument serializes a parsed config document with the
// shared indentation. Parsed merge keys carry an explicit `!!merge` tag that
// yaml.v3 would print as `!!merge <<:`; clearing it keeps the canonical `<<:`
// form, which the parser tags again on the next read.
func encodeConfigYAMLDocument(doc *yaml.Node) ([]byte, error) {
	normalizeConfigYAMLMergeTags(doc)
	var edited bytes.Buffer
	enc := yaml.NewEncoder(&edited)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return edited.Bytes(), nil
}

func normalizeConfigYAMLMergeTags(node *yaml.Node) {
	if node == nil {
		return
	}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!merge" && node.Value == "<<" {
		node.Tag = ""
	}
	for _, child := range node.Content {
		normalizeConfigYAMLMergeTags(child)
	}
}

func splitCatalogAdvisoryField(field string) []string {
	return strings.Split(field, ".")
}

func directYAMLPath(root *yaml.Node, path []string) (*yaml.Node, error) {
	current := root
	for _, key := range path {
		if current == nil || current.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config path %q is not a YAML mapping", key)
		}
		value := directConfigValue(current, key)
		if value == nil {
			return nil, fmt.Errorf("config path %q is not declared directly", key)
		}
		if value.Kind == yaml.AliasNode {
			return nil, fmt.Errorf("config path %q is inherited through a YAML alias", key)
		}
		current = value
	}
	return current, nil
}

func directYAMLParent(root *yaml.Node, path []string) (*yaml.Node, string, error) {
	if len(path) == 0 {
		return nil, "", fmt.Errorf("config path is empty")
	}
	parent, err := directYAMLPath(root, path[:len(path)-1])
	if err != nil {
		return nil, "", err
	}
	if parent.Kind != yaml.MappingNode {
		return nil, "", fmt.Errorf("config path parent is not a YAML mapping")
	}
	if directConfigValue(parent, path[len(path)-1]) == nil {
		return nil, "", fmt.Errorf("config path %q is not declared directly", path[len(path)-1])
	}
	return parent, path[len(path)-1], nil
}

func catalogAdvisoryYAMLScalar(value any) (*yaml.Node, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode advisory value: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse advisory value: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("advisory value must be a YAML scalar")
	}
	return doc.Content[0], nil
}

func setDirectYAMLValue(editor configAddYAML, parent *yaml.Node, key string, value *yaml.Node) error {
	existing := directConfigValue(parent, key)
	if existing == nil {
		return fmt.Errorf("config path %q is not declared directly", key)
	}
	if err := editor.isolate(parent); err != nil {
		return err
	}
	existing = directConfigValue(parent, key)
	if existing == nil {
		return fmt.Errorf("config path %q disappeared while editing", key)
	}
	value.HeadComment = existing.HeadComment
	value.LineComment = existing.LineComment
	value.FootComment = existing.FootComment
	*existing = *value
	return nil
}

func removeDirectYAMLPath(editor configAddYAML, root *yaml.Node, path []string) error {
	if len(path) == 0 {
		return fmt.Errorf("config path is empty")
	}
	parents := make([]*yaml.Node, 0, len(path))
	current := root
	for _, key := range path[:len(path)-1] {
		if current.Kind != yaml.MappingNode {
			return fmt.Errorf("config path %q is not a YAML mapping", key)
		}
		value := directConfigValue(current, key)
		if value == nil {
			return fmt.Errorf("config path %q is not declared directly", key)
		}
		if value.Kind == yaml.AliasNode {
			return fmt.Errorf("config path %q is inherited through a YAML alias", key)
		}
		parents = append(parents, current)
		current = value
	}
	if current.Kind != yaml.MappingNode {
		return fmt.Errorf("config path parent is not a YAML mapping")
	}
	if directConfigValue(current, path[len(path)-1]) == nil {
		return fmt.Errorf("config path %q is not declared directly", path[len(path)-1])
	}
	if err := editor.isolate(current); err != nil {
		return err
	}
	if !removeDirectYAMLKey(current, path[len(path)-1]) {
		return fmt.Errorf("config path %q disappeared while editing", path[len(path)-1])
	}
	for i, parent := range slices.Backward(parents) {
		if current.Kind != yaml.MappingNode || len(current.Content) != 0 {
			break
		}
		if err := editor.isolate(parent); err != nil {
			return err
		}
		if !removeDirectYAMLKey(parent, path[i]) {
			break
		}
		current = parent
	}
	return nil
}

func removeDirectYAMLKey(mapping *yaml.Node, key string) bool {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != key {
			continue
		}
		mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
		return true
	}
	return false
}
