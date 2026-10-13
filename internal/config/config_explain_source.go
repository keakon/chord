package config

import (
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"
)

func newConfigExplainEvidence(globalPath, projectPath string) *ConfigExplainEvidence {
	return &ConfigExplainEvidence{Providers: make(map[string]map[string]*modelExplainEvidence), globalPath: globalPath, projectPath: projectPath}
}
func (e *ConfigExplainEvidence) model(provider, model string) *modelExplainEvidence {
	if e.Providers[provider] == nil {
		e.Providers[provider] = make(map[string]*modelExplainEvidence)
	}
	if e.Providers[provider][model] == nil {
		e.Providers[provider][model] = &modelExplainEvidence{Fields: make(map[ExplainField][]explainRawDeclaration), Catalog: make(map[ExplainField]explainCatalogAssignment)}
	}
	return e.Providers[provider][model]
}
func explainPath(provider, model string, d explainFieldDescriptor) []string {
	return []string{"providers", provider, "models", model, d.block, d.leaf}
}
func pathAncestor(ancestor, path []string) bool {
	return len(ancestor) > 0 && len(ancestor) <= len(path) && slices.Equal(ancestor, path[:len(ancestor)])
}

// collect projects the original bytes using the loader's YAML merge semantics.
// Scalar decoding happens only after the sanitizer has accepted the logical path.
func (e *ConfigExplainEvidence) collect(layer ConfigLayer, cleaned []byte, drops []configDroppedPath) {
	l := explainLayerEvidence{layer: layer.Layer, file: layer.File, complete: true}
	if layer.Layer == OriginLayerProject {
		e.projectExists = true
	}
	var accepted yaml.Node
	if yaml.Unmarshal(cleaned, &accepted) != nil {
		l.complete = false
		l.limitation = "Sanitized YAML could not be projected."
	}
	var root yaml.Node
	if err := yaml.Unmarshal(layer.Data, &root); err != nil {
		l.complete = false
		l.limitation = "Original YAML could not be parsed."
		e.layers = append(e.layers, l)
		return
	}
	var walk func(*yaml.Node, *yaml.Node, []string, bool)
	walk = func(node, acceptedNode *yaml.Node, prefix []string, inherited bool) {
		node = resolveYAMLNode(node)
		if node == nil {
			return
		}
		entries, err := explainMappingEntries(node)
		if err != nil {
			l.complete = false
			l.limitation = "Original YAML contains ambiguous or recursive declarations."
			return
		}
		acceptedEntries, err := explainMappingEntries(resolveYAMLNode(acceptedNode))
		if err != nil {
			l.complete = false
			l.limitation = "Sanitized YAML contains ambiguous or recursive declarations."
		}
		acceptedChildren := make(map[string]*yaml.Node, len(acceptedEntries))
		for _, entry := range acceptedEntries {
			acceptedChildren[entry.Key] = resolveYAMLNode(entry.Val)
		}
		for _, entry := range entries {
			path := append(slices.Clone(prefix), entry.Key)
			depth := len(path)
			if depth == 1 && entry.Key != "providers" || depth == 3 && entry.Key != "models" {
				continue
			}
			if depth == 5 && entry.Key != "limit" && entry.Key != "reasoning" && entry.Key != "thinking" {
				continue
			}
			if depth == 6 {
				relevant := false
				for _, d := range explainFields {
					if d.block == path[4] && d.leaf == path[5] {
						relevant = true
					}
				}
				if !relevant {
					continue
				}
			}
			val := resolveYAMLNode(entry.Val)
			if val == nil {
				l.complete = false
				l.limitation = "A YAML alias could not be resolved."
				continue
			}
			shared := inherited || entry.ViaMerge || val != entry.Val
			raw := explainRawDeclaration{source: ExplainSource{Kind: ExplainSourceKind(layer.Layer), File: layer.File, PathSegments: path, Line: entry.KeyNode.Line, Col: entry.KeyNode.Column, Inherited: shared}, editable: !shared, kind: ExplainDeclarationScalar}
			switch {
			case val.Tag == "!!null":
				raw.kind = ExplainDeclarationNull
			case val.Kind == yaml.MappingNode || val.Kind == yaml.SequenceNode:
				raw.kind = ExplainDeclarationMapping
			}
			for _, drop := range drops {
				if pathAncestor(drop.Segments, path) {
					raw.ignored = true
				}
			}
			if !raw.ignored && acceptedChildren[entry.Key] == nil {
				raw.ignored = true
				// A removed shared anchor cannot prove a unique editable location.
				if shared {
					raw.source.Line = 0
					raw.source.Col = 0
				}
			}
			if depth == 6 && !raw.ignored && raw.kind == ExplainDeclarationScalar {
				scalar := ExplainScalar{}
				if path[4] == "limit" {
					scalar.Kind = ExplainScalarInt
					if val.Decode(&scalar.IntValue) != nil {
						l.complete = false
						l.limitation = "An accepted scalar could not be decoded from its original declaration."
					} else {
						raw.scalar = &scalar
					}
				} else {
					scalar.Kind = ExplainScalarString
					if val.Decode(&scalar.StringValue) != nil {
						l.complete = false
						l.limitation = "An accepted scalar could not be decoded from its original declaration."
					} else {
						raw.scalar = &scalar
					}
				}
			}
			if depth < 4 {
				l.ancestors = append(l.ancestors, raw)
			} else {
				m := e.model(path[1], path[3])
				for _, d := range explainFields {
					if pathAncestor(path, explainPath(path[1], path[3], d)) {
						m.Fields[d.field] = append(m.Fields[d.field], raw)
					}
				}
			}
			if depth < 6 && val.Kind == yaml.MappingNode && !raw.ignored {
				walk(val, acceptedChildren[entry.Key], path, shared)
			}
		}
	}
	if len(root.Content) > 0 {
		var acceptedRoot *yaml.Node
		if len(accepted.Content) > 0 {
			acceptedRoot = accepted.Content[0]
		}
		walk(root.Content[0], acceptedRoot, nil, false)
	}
	e.layers = append(e.layers, l)
}

func relatedExplainDiagnostic(d Diagnostic, path []string) bool {
	if pathAncestor(d.PathSegments, path) {
		return true
	}
	return slices.Equal(d.PathSegments, []string{"providers", path[1], "preset"}) ||
		slices.Equal(d.PathSegments, []string{"providers", path[1], "models", path[3], "catalog"})
}

func (e *ConfigExplainEvidence) finalize(diagnostics []Diagnostic) {
	finalize := func(raw *explainRawDeclaration) {
		for _, d := range diagnostics {
			if d.File == raw.source.File && pathAncestor(d.PathSegments, raw.source.PathSegments) && d.Fallback != "" {
				raw.ignored = true
				raw.scalar = nil
				raw.diagnostics = append(raw.diagnostics, d)
			}
		}
	}
	for i := range e.layers {
		for j := range e.layers[i].ancestors {
			finalize(&e.layers[i].ancestors[j])
		}
	}
	for _, models := range e.Providers {
		for _, m := range models {
			for f, raws := range m.Fields {
				for i := range raws {
					finalize(&raws[i])
				}
				m.Fields[f] = raws
			}
		}
	}
}

// typeIssueLocator parses once per load. Traversal is read-only and returns a
// path only when one logical location matches the decoder's single issue.
type typeIssueLocator struct{ root *yaml.Node }

func newTypeIssueLocator(data []byte, enabled bool) typeIssueLocator {
	if !enabled {
		return typeIssueLocator{}
	}
	var root yaml.Node
	if yaml.Unmarshal(data, &root) != nil || len(root.Content) == 0 {
		return typeIssueLocator{}
	}
	return typeIssueLocator{root: root.Content[0]}
}
func (l typeIssueLocator) path(issue string) []string {
	if l.root == nil {
		return nil
	}
	failures := parseTypeErrorFailures([]string{issue})
	var matches [][]string
	var walk func(*yaml.Node, []string, map[*yaml.Node]bool)
	walk = func(node *yaml.Node, prefix []string, seen map[*yaml.Node]bool) {
		node = resolveYAMLNode(node)
		if node == nil || seen[node] {
			return
		}
		seen[node] = true
		defer delete(seen, node)
		entries, err := YAMLMappingEntries(node)
		if err != nil {
			return
		}
		for _, entry := range entries {
			path := append(slices.Clone(prefix), entry.Key)
			val := resolveYAMLNode(entry.Val)
			if failures.matchesKey(entry.KeyNode) || failures.matchesValue(val) {
				matches = append(matches, path)
			} else if val != nil && val.Kind == yaml.MappingNode {
				walk(val, path, seen)
			}
		}
	}
	walk(l.root, nil, make(map[*yaml.Node]bool))
	if len(matches) == 1 {
		return matches[0]
	}
	return nil
}

func (e *ConfigExplainEvidence) recordCatalog(provider, model string, field ExplainField, scalar ExplainScalar, identity ExplainSource) {
	if e == nil {
		return
	}
	d, _ := explainDescriptor(field)
	identity.PathSegments = explainPath(provider, model, d)
	e.model(provider, model).Catalog[field] = explainCatalogAssignment{source: identity, scalar: scalar}
}

// Reject duplicate keys before projection: YAMLMappingEntries intentionally
// serves existing editors too, but explanation must not choose among duplicates.
func explainMappingEntries(node *yaml.Node) ([]YAMLMappingEntry, error) {
	if err := explainUniqueMapping(node, make(map[*yaml.Node]bool)); err != nil {
		return nil, err
	}
	return YAMLMappingEntries(node)
}
func explainUniqueMapping(node *yaml.Node, seen map[*yaml.Node]bool) error {
	if node == nil {
		return nil
	}
	if seen[node] {
		return fmt.Errorf("recursive YAML declaration")
	}
	seen[node] = true
	defer delete(seen, node)
	if node.Kind == yaml.AliasNode {
		return explainUniqueMapping(node.Alias, seen)
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if err := explainUniqueMapping(child, seen); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	keys := make(map[string]bool)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if keys[key.Value] {
			return fmt.Errorf("duplicate YAML key")
		}
		keys[key.Value] = true
		if key.Tag == "!!merge" {
			if err := explainUniqueMapping(node.Content[i+1], seen); err != nil {
				return err
			}
		}
	}
	return nil
}
