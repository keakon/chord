package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
)

// catalogAdvisoryPinLevel names one candidate write location on the pin
// expansion ladder for a value inherited through YAML aliases or merge keys.
type catalogAdvisoryPinLevel string

const (
	// catalogAdvisoryPinDeclaration replaces the declaring scalar in place.
	catalogAdvisoryPinDeclaration catalogAdvisoryPinLevel = "declaration"
	// catalogAdvisoryPinModelTemplate adds an explicit override to the model's
	// own mapping; every binding of that model inherits it.
	catalogAdvisoryPinModelTemplate catalogAdvisoryPinLevel = "model-template"
	// catalogAdvisoryPinBinding expands the selected provider's models entry
	// so only that binding overrides the inherited value.
	catalogAdvisoryPinBinding catalogAdvisoryPinLevel = "binding"
)

// catalogAdvisoryPinPlan is one level of the pin expansion ladder: the
// smallest edit first, then the model template that serves every binding of
// the model, then the binding's own models entry. Apply edits one freshly
// parsed document and reports what it changed.
type catalogAdvisoryPinPlan struct {
	Level catalogAdvisoryPinLevel
	Apply func(doc *yaml.Node, advisory config.CatalogConfigAdvisory, targetFile string) (string, error)
	// Guard rejects a level before its edit is tried, for conditions the
	// candidate validation cannot see (consumers with no recommendation).
	Guard func(doc *yaml.Node, advisory config.CatalogConfigAdvisory, baseline []config.CatalogConfigAdvisory) error
}

// catalogAdvisoryPinPlans returns the ordered pin expansion ladder.
func catalogAdvisoryPinPlans() []catalogAdvisoryPinPlan {
	return []catalogAdvisoryPinPlan{
		{Level: catalogAdvisoryPinDeclaration, Apply: applyCatalogAdvisoryDeclarationPin, Guard: guardCatalogAdvisoryDeclarationPin},
		{Level: catalogAdvisoryPinModelTemplate, Apply: applyCatalogAdvisoryModelTemplatePin, Guard: guardCatalogAdvisoryModelTemplatePin},
		{Level: catalogAdvisoryPinBinding, Apply: applyCatalogAdvisoryBindingPin},
	}
}

// applyCatalogAdvisoryPinExpansion resolves one recommendation inherited
// through YAML by trying each ladder level and returning the first edit whose
// candidate configuration resolves cleanly, resolves the reviewed value, and
// leaves every unrelated recommendation untouched.
func applyCatalogAdvisoryPinExpansion(current []byte, globalPath, projectPath, targetPath string, advisory config.CatalogConfigAdvisory, baseline []config.CatalogConfigAdvisory) ([]byte, string, error) {
	for _, plan := range catalogAdvisoryPinPlans() {
		edited, detail, err := applyCatalogAdvisoryPinPlan(current, plan, advisory, targetPath, baseline)
		if err != nil {
			continue
		}
		candidate, err := loadConfigCatalogAdvisoryCandidate(globalPath, projectPath, targetPath, edited)
		if err != nil {
			continue
		}
		if hasConfigErrors(candidate.Diagnostics) {
			continue
		}
		if err := validateCatalogAdvisoryPinCandidate(baseline, config.CatalogConfigAdvisories(candidate), advisory); err != nil {
			continue
		}
		return edited, detail, nil
	}
	return nil, "", fmt.Errorf("cannot pin %s automatically; %s", advisory.Path, catalogAdviceManualFixHint(advisory))
}

// applyCatalogAdvisoryPinPlan applies one plan to a fresh parse of the
// current config bytes and re-encodes the document.
func applyCatalogAdvisoryPinPlan(current []byte, plan catalogAdvisoryPinPlan, advisory config.CatalogConfigAdvisory, targetFile string, baseline []config.CatalogConfigAdvisory) ([]byte, string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(current, &doc); err != nil {
		return nil, "", fmt.Errorf("parse config: %w", err)
	}
	if plan.Guard != nil {
		if err := plan.Guard(&doc, advisory, baseline); err != nil {
			return nil, "", err
		}
	}
	detail, err := plan.Apply(&doc, advisory, targetFile)
	if err != nil {
		return nil, "", err
	}
	edited, err := encodeConfigYAMLDocument(&doc)
	if err != nil {
		return nil, "", err
	}
	return edited, detail, nil
}

// validateCatalogAdvisoryPinCandidate accepts a candidate configuration only
// when it resolves the reviewed recommendation, introduces no new or changed
// recommendation, and resolves no recommendation outside the reviewed
// declaration's shared group.
func validateCatalogAdvisoryPinCandidate(baseline, candidate []config.CatalogConfigAdvisory, target config.CatalogConfigAdvisory) error {
	baselineSet := make(map[catalogAdvisoryIdentity]bool, len(baseline))
	for _, advisory := range baseline {
		baselineSet[catalogAdvisoryIdentityOf(advisory)] = true
	}
	candidateSet := make(map[catalogAdvisoryIdentity]bool, len(candidate))
	for _, advisory := range candidate {
		identity := catalogAdvisoryIdentityOf(advisory)
		candidateSet[identity] = true
		if !baselineSet[identity] {
			return fmt.Errorf("candidate introduces recommendation %s", advisory.Path)
		}
	}
	if candidateSet[catalogAdvisoryIdentityOf(target)] {
		return fmt.Errorf("candidate does not resolve %s", target.Path)
	}
	groupSet := make(map[catalogAdvisoryIdentity]bool)
	for _, advisory := range config.CatalogConfigAdvisoryGroup(baseline, target) {
		groupSet[catalogAdvisoryIdentityOf(advisory)] = true
	}
	for _, advisory := range baseline {
		identity := catalogAdvisoryIdentityOf(advisory)
		if candidateSet[identity] || groupSet[identity] {
			continue
		}
		return fmt.Errorf("candidate changes recommendation %s", advisory.Path)
	}
	return nil
}

// catalogAdvisoryIdentity is the complete fingerprint of one active
// recommendation; two loads only agree when every field matches.
type catalogAdvisoryIdentity struct {
	Provider               string
	Model                  string
	Field                  string
	CatalogID              string
	CatalogVersion         string
	CurrentFingerprint     string
	RecommendedFingerprint string
}

func catalogAdvisoryIdentityOf(advisory config.CatalogConfigAdvisory) catalogAdvisoryIdentity {
	return catalogAdvisoryIdentity{
		Provider:               advisory.Provider,
		Model:                  advisory.Model,
		Field:                  advisory.Field,
		CatalogID:              advisory.CatalogID,
		CatalogVersion:         advisory.CatalogVersion,
		CurrentFingerprint:     advisory.CurrentFingerprint,
		RecommendedFingerprint: advisory.RecommendedFingerprint,
	}
}

// applyCatalogAdvisoryDeclarationPin replaces the scalar declared at the
// advisory's recorded origin. It is the smallest edit, so it is tried first;
// the caller rejects it when other values that inherit the declaration would
// get a new recommendation.
func applyCatalogAdvisoryDeclarationPin(doc *yaml.Node, advisory config.CatalogConfigAdvisory, _ string) (string, error) {
	declared, err := catalogAdvisoryDeclarationValue(doc, advisory)
	if err != nil {
		return "", err
	}
	current, err := catalogAdvisoryNodeValue(declared)
	if err != nil {
		return "", err
	}
	if formatCatalogCLIValue(current) != formatCatalogCLIValue(advisory.Current) {
		return "", fmt.Errorf("the declaring value no longer matches the reviewed recommendation")
	}
	replacement, err := catalogAdvisoryYAMLScalar(advisory.Recommended)
	if err != nil {
		return "", err
	}
	replacement.Anchor = declared.Anchor
	replacement.HeadComment = declared.HeadComment
	replacement.LineComment = declared.LineComment
	replacement.FootComment = declared.FootComment
	*declared = *replacement
	return "updated the declaring value at " + advisory.OriginRef(), nil
}

// guardCatalogAdvisoryDeclarationPin rejects a declaration-level change when
// some binding that inherits the declaring value is outside the reviewed
// group. Editing the shared declaration would change those consumers, which
// the candidate validation cannot see when they produce no advisory at all;
// the ladder then writes the override next to the selected model. A
// declaration consumed only by the reviewed group stays editable.
func guardCatalogAdvisoryDeclarationPin(doc *yaml.Node, advisory config.CatalogConfigAdvisory, baseline []config.CatalogConfigAdvisory) error {
	declared, err := catalogAdvisoryDeclarationValue(doc, advisory)
	if err != nil {
		return err
	}
	affected, err := catalogAdvisoryDeclarationBindings(doc, advisory, declared)
	if err != nil {
		return err
	}
	covered := catalogAdvisoryCoveredBindings(baseline, advisory)
	for _, binding := range affected {
		if !covered[binding.provider+"/"+binding.model+"\x00"+advisory.Field] {
			return fmt.Errorf("the value for %s is also inherited by %s/%s; give that binding its own explicit value in config.yaml before applying this recommendation", advisory.Field, binding.provider, binding.model)
		}
	}
	return nil
}

// guardCatalogAdvisoryModelTemplatePin rejects a model-template change when
// the model mapping is also resolved by a binding outside the reviewed group.
// Adding the override to the shared mapping would change those consumers, and
// the candidate validation cannot see them when they produce no advisory at
// all; the ladder then writes the override into the selected binding instead.
func guardCatalogAdvisoryModelTemplatePin(doc *yaml.Node, advisory config.CatalogConfigAdvisory, baseline []config.CatalogConfigAdvisory) error {
	model, err := resolveCatalogAdvisoryModelMapping(doc, advisory)
	if err != nil {
		return err
	}
	affected, err := catalogAdvisoryModelTemplateBindings(doc, model)
	if err != nil {
		return err
	}
	covered := catalogAdvisoryCoveredBindings(baseline, advisory)
	for _, binding := range affected {
		if !covered[binding.provider+"/"+binding.model+"\x00"+advisory.Field] {
			return fmt.Errorf("the model mapping for %s is also used by %s/%s; give that binding its own explicit value in config.yaml before applying this recommendation", advisory.Model, binding.provider, binding.model)
		}
	}
	return nil
}

// catalogAdvisoryCoveredBindings keys the reviewed group by
// provider/model/field so shared-value guards can compare affected bindings
// against the consumers the candidate validation already accepts.
func catalogAdvisoryCoveredBindings(baseline []config.CatalogConfigAdvisory, advisory config.CatalogConfigAdvisory) map[string]bool {
	covered := make(map[string]bool)
	for _, member := range config.CatalogConfigAdvisoryGroup(baseline, advisory) {
		covered[member.Provider+"/"+member.Model+"\x00"+member.Field] = true
	}
	return covered
}

// guardCatalogAdvisoryDirectDeclaration applies the same shared-declaration
// guard to an in-place edit of a directly writable value. Such a value can
// still be inherited through an anchor and a merge key, and unlike an
// inherited target there is no narrower override to fall back to, so the
// edit must be rejected rather than change those consumers silently.
func guardCatalogAdvisoryDirectDeclaration(current []byte, advisory config.CatalogConfigAdvisory, baseline []config.CatalogConfigAdvisory) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(current, &doc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	return guardCatalogAdvisoryDeclarationPin(&doc, advisory, baseline)
}

type catalogAdvisoryBinding struct {
	provider string
	model    string
}

// catalogAdvisoryDeclarationBindings lists every provider/model binding whose
// effective value for the advisory's field is the reviewed declaration, i.e.
// every binding a declaration-level edit would change. Aliases and merge keys
// are resolved, so a shared model template reports each provider that uses it.
func catalogAdvisoryDeclarationBindings(doc *yaml.Node, advisory config.CatalogConfigAdvisory, declared *yaml.Node) ([]catalogAdvisoryBinding, error) {
	parts := splitCatalogAdvisoryField(advisory.Field)
	if len(parts) == 0 {
		return nil, fmt.Errorf("advisory field is empty")
	}
	root, err := documentRootMapping(doc)
	if err != nil {
		return nil, err
	}
	var bindings []catalogAdvisoryBinding
	err = catalogAdvisoryModelBindings(root, func(provider, model string, mapping *yaml.Node) error {
		leaf := mapping
		for _, part := range parts {
			value, err := effectiveConfigValue(leaf, part)
			if err != nil {
				return err
			}
			if value == nil {
				return nil
			}
			leaf = resolveCatalogAdvisoryNode(value)
		}
		if leaf != nil && leaf == declared {
			bindings = append(bindings, catalogAdvisoryBinding{provider: provider, model: model})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bindings, nil
}

// catalogAdvisoryModelTemplateBindings lists every provider/model binding whose
// resolved model mapping is the given node, i.e. every binding a model-template
// edit would change.
func catalogAdvisoryModelTemplateBindings(doc *yaml.Node, model *yaml.Node) ([]catalogAdvisoryBinding, error) {
	root, err := documentRootMapping(doc)
	if err != nil {
		return nil, err
	}
	var bindings []catalogAdvisoryBinding
	err = catalogAdvisoryModelBindings(root, func(provider, name string, mapping *yaml.Node) error {
		if mapping == model {
			bindings = append(bindings, catalogAdvisoryBinding{provider: provider, model: name})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bindings, nil
}

// catalogAdvisoryModelBindings visits every provider/model mapping in the
// document in declaration order, resolving aliases and merge keys.
func catalogAdvisoryModelBindings(root *yaml.Node, visit func(provider, model string, mapping *yaml.Node) error) error {
	providers, err := catalogAdvisoryMappingPath(root, "providers")
	if err != nil {
		return err
	}
	providerEntries, err := config.YAMLMappingEntries(providers)
	if err != nil {
		return err
	}
	for _, providerEntry := range providerEntries {
		provider := resolveCatalogAdvisoryNode(providerEntry.Val)
		if provider == nil || provider.Kind != yaml.MappingNode {
			continue
		}
		models, err := catalogAdvisoryMappingValue(provider, "models")
		if err != nil || models == nil || models.Kind != yaml.MappingNode {
			continue
		}
		modelEntries, err := config.YAMLMappingEntries(models)
		if err != nil {
			return err
		}
		for _, modelEntry := range modelEntries {
			mapping := resolveCatalogAdvisoryNode(modelEntry.Val)
			if mapping == nil || mapping.Kind != yaml.MappingNode {
				continue
			}
			if err := visit(providerEntry.Key, modelEntry.Key, mapping); err != nil {
				return err
			}
		}
	}
	return nil
}

// catalogAdvisoryDeclarationValue finds the value whose key sits at the
// advisory's recorded origin position. Aliases are not resolved: the returned
// node is the declaration itself, so replacing an alias use cannot mutate a
// template shared with other values.
func catalogAdvisoryDeclarationValue(doc *yaml.Node, advisory config.CatalogConfigAdvisory) (*yaml.Node, error) {
	origin := advisory.CurrentOrigin
	field := advisory.Field
	if dot := strings.LastIndexByte(field, '.'); dot >= 0 {
		field = field[dot+1:]
	}
	if origin.Line <= 0 || field == "" {
		return nil, fmt.Errorf("the declaring position is unknown")
	}
	var found *yaml.Node
	var walk func(*yaml.Node) bool
	walk = func(node *yaml.Node) bool {
		if node == nil {
			return true
		}
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				key := node.Content[i]
				if key.Line != origin.Line || (origin.Col > 0 && key.Column != origin.Col) || key.Value != field {
					continue
				}
				found = node.Content[i+1]
				return false
			}
		}
		for _, child := range node.Content {
			if !walk(child) {
				return false
			}
		}
		return true
	}
	walk(doc)
	if found == nil {
		return nil, fmt.Errorf("no declaration for %q at line %d", advisory.Field, origin.Line)
	}
	if found.Kind != yaml.ScalarNode && found.Kind != yaml.AliasNode {
		return nil, fmt.Errorf("the declaring value for %q is not a scalar", advisory.Field)
	}
	return found, nil
}

func catalogAdvisoryNodeValue(node *yaml.Node) (any, error) {
	var value any
	if err := node.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode declared value: %w", err)
	}
	return value, nil
}

// applyCatalogAdvisoryModelTemplatePin writes an explicit override into the
// model's own mapping, which resolves every binding of that model at once.
func applyCatalogAdvisoryModelTemplatePin(doc *yaml.Node, advisory config.CatalogConfigAdvisory, targetFile string) (string, error) {
	model, err := resolveCatalogAdvisoryModelMapping(doc, advisory)
	if err != nil {
		return "", err
	}
	if !catalogAdvisoryModelMappingOwned(doc, model, advisory.Model) {
		return "", fmt.Errorf("the model mapping is a shared template")
	}
	if err := writeCatalogAdvisoryOverride(model, advisory); err != nil {
		return "", err
	}
	return fmt.Sprintf("added an explicit override under the model template at %s:%d", targetFile, model.Line), nil
}

// resolveCatalogAdvisoryModelMapping walks providers.<provider>.models.<model>
// through merge keys and aliases and returns the resolved model mapping.
func resolveCatalogAdvisoryModelMapping(doc *yaml.Node, advisory config.CatalogConfigAdvisory) (*yaml.Node, error) {
	root, err := documentRootMapping(doc)
	if err != nil {
		return nil, err
	}
	models, err := catalogAdvisoryMappingPath(root, "providers", advisory.Provider, "models")
	if err != nil {
		return nil, err
	}
	model, err := catalogAdvisoryMappingValue(models, advisory.Model)
	if err != nil {
		return nil, err
	}
	if model.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("model %q is not a YAML mapping", advisory.Model)
	}
	return model, nil
}

func catalogAdvisoryMappingPath(root *yaml.Node, path ...string) (*yaml.Node, error) {
	current := root
	for _, key := range path {
		next, err := catalogAdvisoryMappingValue(current, key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		current = next
	}
	return current, nil
}

func catalogAdvisoryMappingValue(mapping *yaml.Node, key string) (*yaml.Node, error) {
	value, err := effectiveConfigValue(resolveCatalogAdvisoryNode(mapping), key)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("config key %q is not declared", key)
	}
	return resolveCatalogAdvisoryNode(value), nil
}

func resolveCatalogAdvisoryNode(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

// catalogAdvisoryModelMappingOwned reports whether every alias that reuses the
// resolved model mapping belongs to a model entry with the same name. A
// mapping reused under another key is a shared base template, so editing it
// could change models the recommendation does not cover; the binding level
// then keeps the edit local instead.
func catalogAdvisoryModelMappingOwned(doc *yaml.Node, model *yaml.Node, name string) bool {
	if model.Anchor == "" {
		return true
	}
	owned := true
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		if node == nil || !owned {
			return
		}
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				value := node.Content[i+1]
				if value.Kind == yaml.AliasNode && value.Alias == model && node.Content[i].Value != name {
					owned = false
					return
				}
			}
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	walk(doc)
	return owned
}

// applyCatalogAdvisoryBindingPin expands the selected provider's models entry
// into a merge of the original entry plus one explicit model override, so the
// edit stays local to this binding.
func applyCatalogAdvisoryBindingPin(doc *yaml.Node, advisory config.CatalogConfigAdvisory, _ string) (string, error) {
	root, err := documentRootMapping(doc)
	if err != nil {
		return "", err
	}
	providers := directConfigValue(root, "providers")
	if providers == nil || providers.Kind != yaml.MappingNode {
		return "", fmt.Errorf("providers is not a directly declared mapping")
	}
	provider := directConfigValue(providers, advisory.Provider)
	if provider == nil || provider.Kind != yaml.MappingNode {
		return "", fmt.Errorf("provider %q is not a directly declared mapping", advisory.Provider)
	}
	if provider.Anchor != "" {
		return "", fmt.Errorf("provider %q is shared through a YAML anchor", advisory.Provider)
	}
	models := directConfigValue(provider, "models")
	if models == nil {
		return "", fmt.Errorf("provider %q does not declare models directly", advisory.Provider)
	}
	if models.Kind != yaml.AliasNode && models.Kind != yaml.MappingNode {
		return "", fmt.Errorf("provider %q models is not a YAML mapping", advisory.Provider)
	}
	if models.Anchor != "" {
		return "", fmt.Errorf("provider %q models is shared through a YAML anchor", advisory.Provider)
	}
	entry, err := catalogAdvisoryMappingValue(resolveCatalogAdvisoryNode(models), advisory.Model)
	if err != nil {
		return "", err
	}
	if entry.Kind != yaml.MappingNode {
		return "", fmt.Errorf("model %q is not a YAML mapping", advisory.Model)
	}
	expanded := &yaml.Node{
		Kind:        yaml.MappingNode,
		Tag:         "!!map",
		HeadComment: models.HeadComment,
		LineComment: models.LineComment,
		FootComment: models.FootComment,
	}
	modelsRef, err := catalogAdvisoryNodeReference(models)
	if err != nil {
		return "", err
	}
	expanded.Content = append(expanded.Content, catalogAdvisoryMergeKey(), modelsRef)
	override := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	entryRef, err := catalogAdvisoryNodeReference(entry)
	if err != nil {
		return "", err
	}
	override.Content = append(override.Content, catalogAdvisoryMergeKey(), entryRef)
	if err := writeCatalogAdvisoryOverride(override, advisory); err != nil {
		return "", err
	}
	expanded.Content = append(expanded.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: advisory.Model}, override)
	*models = *expanded
	return fmt.Sprintf("expanded providers.%s.models to add an explicit %s entry", advisory.Provider, advisory.Model), nil
}

func catalogAdvisoryMergeKey() *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"}
}

// catalogAdvisoryNodeReference returns a node that reproduces n where it is
// placed: an alias when n carries an anchor, otherwise an explicit deep copy.
func catalogAdvisoryNodeReference(node *yaml.Node) (*yaml.Node, error) {
	if node.Anchor != "" {
		return &yaml.Node{Kind: yaml.AliasNode, Value: node.Anchor, Alias: node}, nil
	}
	if node.Kind == yaml.AliasNode {
		copy := *node
		return &copy, nil
	}
	return cloneConfigValue(node, make(map[*yaml.Node]bool))
}

// writeCatalogAdvisoryOverride writes an explicit override for the advisory's
// field into mapping. A dotted field redeclares its first block with every
// effective sibling leaf copied in, because YAML merge is shallow: without the
// siblings, the lower-priority block would fall back to catalog defaults.
func writeCatalogAdvisoryOverride(mapping *yaml.Node, advisory config.CatalogConfigAdvisory) error {
	parts := splitCatalogAdvisoryField(advisory.Field)
	if len(parts) == 0 {
		return fmt.Errorf("advisory field is empty")
	}
	value, err := catalogAdvisoryYAMLScalar(advisory.Recommended)
	if err != nil {
		return err
	}
	if len(parts) == 1 {
		return setCatalogAdvisoryMappingValue(mapping, parts[0], value)
	}
	override := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	block, err := effectiveConfigValue(mapping, parts[0])
	if err != nil {
		return err
	}
	block = resolveCatalogAdvisoryNode(block)
	if block != nil {
		if block.Kind != yaml.MappingNode {
			return fmt.Errorf("config block %q is not a YAML mapping", parts[0])
		}
		if override, err = cloneConfigValue(block, make(map[*yaml.Node]bool)); err != nil {
			return err
		}
		stripCatalogAdvisoryComments(override)
	}
	if err := setCatalogAdvisoryNestedValue(override, parts[1:], value); err != nil {
		return err
	}
	return setCatalogAdvisoryMappingValue(mapping, parts[0], override)
}

// stripCatalogAdvisoryComments drops comments copied from the inherited block:
// they describe the source values, which the override replaces, and the source
// block keeps them on its own copy.
func stripCatalogAdvisoryComments(node *yaml.Node) {
	if node == nil {
		return
	}
	node.HeadComment, node.LineComment, node.FootComment = "", "", ""
	for _, child := range node.Content {
		stripCatalogAdvisoryComments(child)
	}
}

// setCatalogAdvisoryNestedValue sets one leaf inside a private override copy,
// materializing an explicit parent mapping when the path only exists through
// a merge key.
func setCatalogAdvisoryNestedValue(mapping *yaml.Node, parts []string, value *yaml.Node) error {
	if len(parts) == 0 {
		return fmt.Errorf("advisory field path is empty")
	}
	if len(parts) == 1 {
		return setCatalogAdvisoryMappingValue(mapping, parts[0], value)
	}
	child, err := effectiveConfigValue(mapping, parts[0])
	if err != nil {
		return err
	}
	child = resolveCatalogAdvisoryNode(child)
	switch {
	case child == nil:
		child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if err := setCatalogAdvisoryMappingValue(mapping, parts[0], child); err != nil {
			return err
		}
	case child.Kind != yaml.MappingNode:
		return fmt.Errorf("config block %q is not a YAML mapping", parts[0])
	case directConfigValue(mapping, parts[0]) == nil:
		copy, err := cloneConfigValue(child, make(map[*yaml.Node]bool))
		if err != nil {
			return err
		}
		if err := setCatalogAdvisoryMappingValue(mapping, parts[0], copy); err != nil {
			return err
		}
		child = copy
	}
	return setCatalogAdvisoryNestedValue(child, parts[1:], value)
}

// setCatalogAdvisoryMappingValue replaces or appends one mapping entry. The
// existing value's anchor is not carried over: if other aliases reuse it, the
// candidate configuration stops resolving and the caller rejects the level.
func setCatalogAdvisoryMappingValue(mapping *yaml.Node, key string, value *yaml.Node) error {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return fmt.Errorf("config parent for %q is not a YAML mapping", key)
	}
	if existing := directConfigValue(mapping, key); existing != nil {
		value.HeadComment = existing.HeadComment
		value.LineComment = existing.LineComment
		value.FootComment = existing.FootComment
		*existing = *value
		return nil
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
	return nil
}
