package main

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
)

// materializeConfigAdd freezes the selected model after resolution, inside the
// config write lock. Provider settings stay at provider scope; only fields whose
// source is the catalog move into the model alongside its existing overrides.
func materializeConfigAdd(raw []byte, resolved *config.ResolvedConfig, edit configAddEdit) ([]byte, error) {
	provider := resolved.Config.Providers[edit.providerName]
	model := provider.Models[edit.wireModel]
	var values yaml.Node
	if err := values.Encode(model); err != nil {
		return nil, fmt.Errorf("encode resolved model: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse model config: %w", err)
	}
	editor := configAddYAML{doc: &doc}
	root, err := documentRootMapping(&doc)
	if err != nil {
		return nil, err
	}
	target := root
	for _, key := range []string{"providers", edit.providerName, "models", edit.wireModel} {
		target, err = editor.child(target, key, yaml.MappingNode, "!!map")
		if err != nil {
			return nil, err
		}
	}
	// Name is derived from the map key. Catalog is replaced only after the
	// resolved settings have been copied, never persisted as a runtime lookup.
	for i := 0; i+1 < len(values.Content); i += 2 {
		key := values.Content[i].Value
		if key == "name" || key == "catalog" {
			continue
		}
		value := values.Content[i+1]
		if key == "thinking" || key == "reasoning" {
			// Effort is a user choice, not a capability. Existing declarations
			// remain in target; catalog recommendations belong in variants.
			fields := value.Content[:0]
			for j := 0; j+1 < len(value.Content); j += 2 {
				if field := value.Content[j].Value; field != "effort" && field != "level" {
					fields = append(fields, value.Content[j], value.Content[j+1])
				}
			}
			value.Content = fields
			if len(fields) == 0 {
				continue
			}
		}
		if err := editor.fill(target, key, value); err != nil {
			return nil, err
		}
	}
	// Preset Responses gates are also read at request construction time, beyond
	// ordinary model materialization. Preserve only catalog-sourced leaves so
	// provider overrides keep their scope and future edits still take effect.
	response, sources := config.ResolveResponsesCompat(provider.Preset, edit.wireModel, model, provider.Compat)
	if response != nil {
		var fields yaml.Node
		if err := fields.Encode(response); err != nil {
			return nil, fmt.Errorf("encode Responses fields: %w", err)
		}
		for i := 0; i+1 < len(fields.Content); i += 2 {
			key := fields.Content[i].Value
			if sources[key] != "catalog" {
				continue
			}
			compat, err := editor.child(target, "compat", yaml.MappingNode, "!!map")
			if err != nil {
				return nil, err
			}
			responses, err := editor.child(compat, "responses", yaml.MappingNode, "!!map")
			if err != nil {
				return nil, err
			}
			if err := editor.set(responses, key, fields.Content[i+1]); err != nil {
				return nil, err
			}
		}
	}
	if err := editor.set(target, "catalog", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}); err != nil {
		return nil, err
	}
	return encodeConfigYAMLDocument(&doc)
}

func (e configAddYAML) addReasoningVariants(providerNode *yaml.Node, wireModel string) error {
	var provider config.ProviderConfig
	if err := providerNode.Decode(&provider); err != nil {
		return fmt.Errorf("decode model provider: %w", err)
	}
	variants, err := config.CatalogReasoningVariants(provider, wireModel, provider.Models[wireModel])
	if err != nil {
		return err
	}
	if len(variants) == 0 {
		return nil
	}
	models, err := e.child(providerNode, "models", yaml.MappingNode, "!!map")
	if err != nil {
		return err
	}
	model, err := e.child(models, wireModel, yaml.MappingNode, "!!map")
	if err != nil {
		return err
	}
	var value yaml.Node
	if err := value.Encode(variants); err != nil {
		return fmt.Errorf("encode reasoning variants: %w", err)
	}
	existing, err := effectiveConfigValue(model, "variants")
	if err != nil {
		return err
	}
	if existing != nil && existing.ShortTag() == "!!null" {
		return nil
	}
	target, err := e.child(model, "variants", yaml.MappingNode, "!!map")
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		name := value.Content[i].Value
		declared, err := effectiveConfigValue(target, name)
		if err != nil {
			return err
		}
		// A named variant is an explicit choice, including fields it inherits
		// from the base model. Adding a type could change that inheritance.
		if declared == nil {
			if err := e.set(target, name, value.Content[i+1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// fill preserves declarations, including nulls and values inherited through
// YAML merges, while adding the fields supplied by model resolution.
func (e configAddYAML) fill(mapping *yaml.Node, key string, value *yaml.Node) error {
	existing, err := effectiveConfigValue(mapping, key)
	if err != nil {
		return err
	}
	if existing == nil {
		return e.set(mapping, key, value)
	}
	if value.Kind != yaml.MappingNode || existing.Tag == "!!null" {
		return nil
	}
	child, err := e.child(mapping, key, yaml.MappingNode, "!!map")
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		if err := e.fill(child, value.Content[i].Value, value.Content[i+1]); err != nil {
			return err
		}
	}
	return nil
}

func (e configAddYAML) set(mapping *yaml.Node, key string, value *yaml.Node) error {
	if err := e.isolate(mapping); err != nil {
		return err
	}
	if existing := directConfigValue(mapping, key); existing != nil {
		if err := e.isolate(existing); err != nil {
			return err
		}
		value.HeadComment, value.LineComment, value.FootComment = existing.HeadComment, existing.LineComment, existing.FootComment
		*existing = *value
		return nil
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	return nil
}
