package config

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/keakon/golog/log"
	"gopkg.in/yaml.v3"
)

// OriginLayer identifies the config file layer a raw declaration came from.
// Layers are listed lowest priority first everywhere in this file, matching
// the effective precedence project > global.
type OriginLayer string

const (
	OriginLayerGlobal  OriginLayer = "global"
	OriginLayerProject OriginLayer = "project"
	// OriginLayerCatalog marks values the built-in model catalog filled in.
	// It is the lowest-priority source, below every user layer.
	OriginLayerCatalog OriginLayer = "catalog"
)

// Origin locates one raw YAML declaration in its source layer.
type Origin struct {
	Layer OriginLayer
	File  string
	Line  int
	Col   int
}

// BlockOrigin records one layer's declaration of a nullable model capability
// block. Null marks an explicit `block: null`, which clears every
// lower-priority contribution to that block (including a future model catalog).
type BlockOrigin struct {
	Origin
	Null bool
}

// LimitOrigin records which limit leaves the indexed layers declared. An
// explicitly declared input leaf is an independent input contract; an absent
// one derives from context/output at budget time. Slices are ordered lowest
// priority layer first.
type LimitOrigin struct {
	Context []Origin
	Input   []Origin
	Output  []Origin
}

// ModelOrigin aggregates the tracked raw-layer declarations for one model.
type ModelOrigin struct {
	// Defined lists the layers declaring the model key itself, lowest
	// priority first.
	Defined []Origin
	// Catalog records explicit model-level catalog bindings.
	Catalog []Origin
	// Blocks holds the layered declarations of the nullable capability blocks
	// this index tracks, lowest priority first.
	Blocks map[string][]BlockOrigin
	Limit  LimitOrigin
}

// ProviderOrigin aggregates the tracked declarations for one provider.
type ProviderOrigin struct {
	// Preset lists the layers declaring the provider's preset selection field,
	// lowest priority first. Empty means no layer declared it.
	Preset []Origin
	Models map[string]ModelOrigin
}

// PoolRefOrigin locates one model reference inside a model_pools entry.
type PoolRefOrigin struct {
	Origin
	Ref string
}

// SourceIndex is the sparse origin index over the raw config layers. It tracks
// only the paths resolution needs: provider preset selection, model presence,
// nullable capability blocks, limit leaves, and model pool references. It is
// built from sanitized layer bytes (invalid overrides are stripped before
// indexing, so the index records only effective declarations) and never enters
// request hot paths.
type SourceIndex struct {
	Providers  map[string]ProviderOrigin
	ModelPools map[string][]PoolRefOrigin
	Compaction map[string][]Origin
}

// nullableModelBlocks lists the model blocks whose explicit null clears the
// block and blocks lower-priority sources from refilling it. Field contracts
// on each block decide whether individual leaves accept false/0/empty.
var nullableModelBlocks = []string{
	"reasoning",
	"thinking",
	"text",
	"prompt_cache",
	"cost",
	"modalities",
	"compat",
	"variants",
	"compaction",
	"parallel_tool_calls",
	"store",
}

func isNullableModelBlock(key string) bool {
	for _, block := range nullableModelBlocks {
		if key == block {
			return true
		}
	}
	return false
}

// ConfigLayer is one sanitized config layer for BuildSourceIndex.
type ConfigLayer struct {
	Layer OriginLayer
	File  string
	// Data holds sanitized layer bytes: invalid overrides must be stripped
	// before indexing so the index only records effective declarations. The
	// loader's cleaned override data qualifies; raw file bytes do not.
	Data []byte
}

// BuildSourceIndex builds the sparse origin index from config layers ordered
// lowest priority first (global, then project). Malformed YAML is an error;
// callers index sanitized bytes, so this only fires on loader bugs.
func BuildSourceIndex(layers ...ConfigLayer) (*SourceIndex, error) {
	idx := &SourceIndex{
		Providers:  make(map[string]ProviderOrigin),
		ModelPools: make(map[string][]PoolRefOrigin),
		Compaction: make(map[string][]Origin),
	}
	for _, layer := range layers {
		if len(layer.Data) == 0 {
			continue
		}
		var root yaml.Node
		if err := yaml.Unmarshal(layer.Data, &root); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", layer.File, err)
		}
		b := &sourceIndexBuilder{idx: idx, layer: layer}
		b.walkDocument(&root)
	}
	return idx, nil
}

// PresetOrigin returns the highest-priority declaration of a provider's
// preset selection field.
func (idx *SourceIndex) PresetOrigin(provider string) (Origin, bool) {
	po, ok := idx.Providers[provider]
	if !ok || len(po.Preset) == 0 {
		return Origin{}, false
	}
	return po.Preset[len(po.Preset)-1], true
}

// Model returns the tracked declarations for one configured model.
func (idx *SourceIndex) Model(provider, model string) (ModelOrigin, bool) {
	po, ok := idx.Providers[provider]
	if !ok {
		return ModelOrigin{}, false
	}
	mo, ok := po.Models[model]
	return mo, ok
}

// ModelBlock returns the layered declarations of a nullable model block,
// lowest priority first. False when no indexed layer declared the block.
func (idx *SourceIndex) ModelBlock(provider, model, block string) ([]BlockOrigin, bool) {
	mo, ok := idx.Model(provider, model)
	if !ok {
		return nil, false
	}
	decls, ok := mo.Blocks[block]
	return decls, ok
}

// PoolRefs returns the tracked references of one model pool with their
// positions.
func (idx *SourceIndex) PoolRefs(pool string) ([]PoolRefOrigin, bool) {
	refs, ok := idx.ModelPools[pool]
	return refs, ok
}

// BlockState is the effective cross-layer state of one nullable model block.
type BlockState struct {
	// Present reports whether any indexed layer declared the block.
	Present bool
	// Active reports whether the block carries values after clearing: the
	// highest-priority declaration is a mapping.
	Active bool
	// Cleared reports whether the highest-priority declaration is an explicit
	// null, which removes every lower-priority contribution and blocks lower
	// sources (such as the model catalog) from refilling the block.
	Cleared bool
	// LowerCleared reports that a lower-priority user layer explicitly cleared
	// the block before a higher layer redeclared it. A higher mapping rebuilds
	// only its own fields; catalog defaults must not cross that clear boundary.
	LowerCleared bool
	// Origin is the highest-priority declaration.
	Origin Origin
}

// BlockClearing resolves the cross-layer clear state of one nullable model
// block from its layered declarations, lowest priority first. With two user
// layers the highest-priority declaration alone decides: a mapping is active
// (fields merge over never-cleared lower layers), a null clears everything
// below it. Lower sources that a null cleared must not be restored through
// higher-layer redeclarations: only fields the higher layer explicitly writes
// exist afterwards.
func BlockClearing(decls []BlockOrigin) BlockState {
	if len(decls) == 0 {
		return BlockState{}
	}
	topIndex := len(decls) - 1
	top := decls[topIndex]
	lowerCleared := false
	for _, decl := range decls[:topIndex] {
		if decl.Null {
			lowerCleared = true
			break
		}
	}
	return BlockState{
		Present:      true,
		Active:       !top.Null,
		Cleared:      top.Null,
		LowerCleared: lowerCleared,
		Origin:       top.Origin,
	}
}

// ExplicitInputContract returns the highest-priority declaration of a model's
// limit.input leaf. The declaration is raw presence: callers must pair it with
// the effective limit value, since an explicit zero behaves as unset.
func (idx *SourceIndex) ExplicitInputContract(provider, model string) (Origin, bool) {
	mo, ok := idx.Model(provider, model)
	if !ok || len(mo.Limit.Input) == 0 {
		return Origin{}, false
	}
	return mo.Limit.Input[len(mo.Limit.Input)-1], true
}

// sourceIndexBuilder walks one sanitized config layer into the index.
type sourceIndexBuilder struct {
	idx   *SourceIndex
	layer ConfigLayer
}

func (b *sourceIndexBuilder) origin(keyNode *yaml.Node) Origin {
	return Origin{
		Layer: b.layer.Layer,
		File:  b.layer.File,
		Line:  keyNode.Line,
		Col:   keyNode.Column,
	}
}

func (b *sourceIndexBuilder) walkDocument(root *yaml.Node) {
	if root == nil || len(root.Content) == 0 {
		return
	}
	// A parsed document wraps the top-level mapping in a DocumentNode;
	// mappingEntries resolves an alias root to the anchored mapping.
	top := root.Content[0]
	for _, e := range mappingEntries(top) {
		switch e.Key {
		case "context":
			for _, block := range mappingEntries(e.Val) {
				if block.Key != "compaction" {
					continue
				}
				for _, leaf := range mappingEntries(block.Val) {
					// Invalid fractions are discarded by config loading. They must
					// also be absent here so they cannot suppress catalog defaults.
					var value float64
					if err := leaf.Val.Decode(&value); err != nil {
						continue
					}
					if leaf.Key == "threshold" && !validCompactionFraction(value) ||
						leaf.Key == "reminder" && !validCompactionReminder(value) {
						continue
					}
					b.idx.Compaction[leaf.Key] = append(b.idx.Compaction[leaf.Key], b.origin(leaf.KeyNode))
				}
			}
		case "providers":
			b.walkProviders(e.Val)
		case "model_pools":
			b.walkModelPools(e.Val)
		}
	}
}

func (b *sourceIndexBuilder) walkProviders(node *yaml.Node) {
	for _, e := range mappingEntries(node) {
		po, ok := b.idx.Providers[e.Key]
		if !ok {
			po = ProviderOrigin{Models: make(map[string]ModelOrigin)}
			b.idx.Providers[e.Key] = po
		}
		for _, fe := range mappingEntries(e.Val) {
			switch fe.Key {
			case "preset":
				if fe.Val != nil && fe.Val.Kind == yaml.ScalarNode && fe.Val.Tag != "!!null" {
					po.Preset = append(po.Preset, b.origin(fe.KeyNode))
				}
			case "models":
				b.walkModels(po, fe.Val)
			}
		}
		b.idx.Providers[e.Key] = po
	}
}

func (b *sourceIndexBuilder) walkModels(po ProviderOrigin, node *yaml.Node) {
	for _, e := range mappingEntries(node) {
		mo, ok := po.Models[e.Key]
		if !ok {
			mo = ModelOrigin{Blocks: make(map[string][]BlockOrigin)}
		}
		mo.Defined = append(mo.Defined, b.origin(e.KeyNode))
		for _, fe := range mappingEntries(e.Val) {
			switch {
			case fe.Key == "catalog":
				if fe.Val != nil {
					mo.Catalog = append(mo.Catalog, b.origin(fe.KeyNode))
				}
			case isNullableModelBlock(fe.Key):
				null := fe.Val != nil && fe.Val.Kind == yaml.ScalarNode && fe.Val.Tag == "!!null"
				if fe.Val != nil && fe.Val.Kind != yaml.ScalarNode {
					// A block mapping (or a wrongly typed value): record the
					// declaration; wrongly typed values are the loader's
					// diagnostics to report.
					null = false
				}
				mo.Blocks[fe.Key] = append(mo.Blocks[fe.Key], BlockOrigin{Origin: b.origin(fe.KeyNode), Null: null})
			case fe.Key == "limit":
				b.walkLimit(&mo.Limit, fe.Val)
			}
		}
		po.Models[e.Key] = mo
	}
}

func (b *sourceIndexBuilder) walkLimit(limit *LimitOrigin, node *yaml.Node) {
	for _, e := range mappingEntries(node) {
		if e.Val == nil || e.Val.Kind != yaml.ScalarNode || e.Val.Tag == "!!null" {
			continue
		}
		origin := b.origin(e.KeyNode)
		switch e.Key {
		case "context":
			limit.Context = append(limit.Context, origin)
		case "input":
			limit.Input = append(limit.Input, origin)
		case "output":
			limit.Output = append(limit.Output, origin)
		}
	}
}

func (b *sourceIndexBuilder) walkModelPools(node *yaml.Node) {
	for _, e := range mappingEntries(node) {
		if e.Val == nil || e.Val.Kind != yaml.SequenceNode {
			continue
		}
		refs := b.idx.ModelPools[e.Key]
		for _, item := range e.Val.Content {
			resolved := resolveYAMLNode(item)
			if resolved == nil || resolved.Kind != yaml.ScalarNode || resolved.Tag == "!!null" {
				continue
			}
			refs = append(refs, PoolRefOrigin{Origin: b.origin(item), Ref: resolved.Value})
		}
		b.idx.ModelPools[e.Key] = refs
	}
}

// resolveYAMLNode follows alias nodes to their anchor. YAML forbids alias
// cycles, so one dereference is enough.
func resolveYAMLNode(n *yaml.Node) *yaml.Node {
	for n != nil && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// prependCatalogOrigin records a catalog-sourced declaration as the
// lowest-priority layer of a model's tracked limit leaf.
func (idx *SourceIndex) prependCatalogLimitOrigin(provider, model, leaf string) {
	mo, ok := idx.modelOriginForWrite(provider, model)
	if !ok {
		return
	}
	origin := catalogOrigin()
	switch leaf {
	case "context":
		mo.Limit.Context = append([]Origin{origin}, mo.Limit.Context...)
	case "input":
		mo.Limit.Input = append([]Origin{origin}, mo.Limit.Input...)
	case "output":
		mo.Limit.Output = append([]Origin{origin}, mo.Limit.Output...)
	}
	idx.writeModelOrigin(provider, model, mo)
}

// prependCatalogBlockOrigin records a catalog-sourced declaration as the
// lowest-priority layer of a nullable model block.
func (idx *SourceIndex) prependCatalogBlockOrigin(provider, model, block string) {
	mo, ok := idx.modelOriginForWrite(provider, model)
	if !ok {
		return
	}
	mo.Blocks[block] = append([]BlockOrigin{{Origin: catalogOrigin()}}, mo.Blocks[block]...)
	idx.writeModelOrigin(provider, model, mo)
}

func (idx *SourceIndex) modelOriginForWrite(provider, model string) (ModelOrigin, bool) {
	if idx.Providers == nil {
		return ModelOrigin{}, false
	}
	po, ok := idx.Providers[provider]
	if !ok {
		return ModelOrigin{}, false
	}
	mo, ok := po.Models[model]
	if !ok {
		return ModelOrigin{}, false
	}
	return mo, true
}

func (idx *SourceIndex) writeModelOrigin(provider, model string, mo ModelOrigin) {
	po := idx.Providers[provider]
	if po.Models == nil {
		po.Models = make(map[string]ModelOrigin)
	}
	po.Models[model] = mo
	idx.Providers[provider] = po
}

func (idx *SourceIndex) ensureModelOrigin(provider, model string) {
	if idx == nil {
		return
	}
	po := idx.Providers[provider]
	if po.Models == nil {
		po.Models = make(map[string]ModelOrigin)
	}
	if _, ok := po.Models[model]; !ok {
		po.Models[model] = ModelOrigin{Blocks: make(map[string][]BlockOrigin)}
	}
	idx.Providers[provider] = po
}

func catalogOrigin() Origin {
	return Origin{Layer: OriginLayerCatalog, File: "modelcatalog://" + modelcatalogVersion()}
}

// yamlEntry is one mapping entry with alias-resolved value. ViaMerge marks
// entries pulled in through a `<<` merge key; explicit keys win over merged
// ones, mirroring YAML merge semantics.
type yamlEntry struct {
	Key      string
	KeyNode  *yaml.Node
	Val      *yaml.Node
	ViaMerge bool
}

// mappingEntries projects validated YAML declarations into the origin index.
func mappingEntries(node *yaml.Node) []yamlEntry {
	entries, err := YAMLMappingEntries(node)
	if err != nil {
		return nil
	}
	out := make([]yamlEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, yamlEntry{
			Key: entry.Key, KeyNode: entry.KeyNode,
			Val: resolveYAMLNode(entry.Val), ViaMerge: entry.ViaMerge,
		})
	}
	return out
}

// ResolvedConfig couples the effective runtime config with the sparse origin
// index and structured diagnostics gathered while loading. It is the single
// resolution result every offline surface (show, doctor, wizard previews)
// consumes; the runtime config inside is exactly what LoadConfig plus
// MergeProjectConfig would produce.
type ResolvedConfig struct {
	// Config is the effective project-merged config.
	Config *Config
	// Global and Project are the per-layer configs as loaded; nil when the
	// respective file is missing.
	Global  *Config
	Project *Config
	// Index is the sparse origin index over both layers.
	Index *SourceIndex
	// CatalogVersion is the version of the built-in model catalog that
	// resolution used, shown by show so catalog-sourced values stay
	// attributable.
	CatalogVersion string
	// Diagnostics lists the structured problems found while loading: dropped
	// invalid overrides, missing files, and broken pool references. It is
	// additive to the loader's existing log output and never replaces it.
	Diagnostics []Diagnostic
}

// LoadResolvedConfig loads the global config, overlays the project config,
// and builds the sparse origin index from the same files. It reuses the
// existing load/merge pipeline, then applies endpoint contracts and catalog
// materialization to the resulting snapshot. A missing global config yields a diagnostic and an empty index
// rather than an error or the first-run wizard; surfacing is up to callers.
func LoadResolvedConfig(globalPath, projectPath string, extraRefs ...string) (*ResolvedConfig, error) {
	rc := &ResolvedConfig{}

	globalData, err := os.ReadFile(globalPath)
	switch {
	case err == nil:
		globalCleaned, _, serr := stripTypeInvalidOverride(globalPath, globalData)
		if serr != nil {
			return nil, serr
		}
		rc.Global, err = loadConfigData(globalPath, globalData, true, &rc.Diagnostics)
		if err != nil {
			return nil, err
		}
		rc.addIndexLayer(ConfigLayer{Layer: OriginLayerGlobal, File: globalPath, Data: globalCleaned})
	case os.IsNotExist(err):
		rc.Diagnostics = append(rc.Diagnostics, Diagnostic{
			Severity:  DiagnosticSeverityWarning,
			File:      globalPath,
			Message:   "global config file not found; no providers are configured",
			Continues: true,
			Fallback:  "built-in defaults",
		})
		err = nil
	default:
		return nil, fmt.Errorf("read config %s: %w", globalPath, err)
	}

	if projectPath != "" {
		projectData, readErr := os.ReadFile(projectPath)
		switch {
		case readErr == nil:
			var projectMap map[string]any
			if err := yaml.Unmarshal(projectData, &projectMap); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", projectPath, err)
			}
			unsupported := unsupportedProjectTopLevelKeys(projectMap)
			for _, key := range unsupported {
				appendLoadDiagnostic(&rc.Diagnostics, projectPath, key, fmt.Sprintf("field %q is not supported in project config", key), "field ignored")
			}
			cleaned, drops, serr := stripTypeInvalidOverride(projectPath, projectData)
			if serr != nil {
				return nil, serr
			}
			for _, drop := range drops {
				if slices.Contains(unsupported, drop) {
					continue
				}
				log.Warnf("config %s: ignoring invalid value: %s", projectPath, drop)
				rc.Diagnostics = append(rc.Diagnostics, Diagnostic{
					Severity:  DiagnosticSeverityWarning,
					File:      projectPath,
					Path:      drop,
					Message:   "ignoring invalid value: " + drop,
					Continues: true,
					Fallback:  "inherited global/default value",
				})
			}
			rc.Project, err = loadConfigData(projectPath, cleaned, false, nil)
			if err != nil {
				return nil, err
			}
			rc.Config, err = mergeConfigOverrideData(rc.Global, cleaned, projectPath, &rc.Diagnostics)
			if err != nil {
				return nil, err
			}
			rc.addIndexLayer(ConfigLayer{Layer: OriginLayerProject, File: projectPath, Data: cleaned})
		case os.IsNotExist(readErr):
			rc.Config = rc.Global
			readErr = nil
		default:
			return nil, fmt.Errorf("read config %s: %w", projectPath, readErr)
		}
	}
	if rc.Config == nil {
		rc.Config = rc.Global
	}
	if rc.Config == nil {
		rc.Config = DefaultConfig()
	}
	// Normalize endpoint presets before catalog lookup so every consumer sees
	// the same protocol and request contract. Keep the tolerant loader behavior:
	// a contract error is reported structurally and the raw provider remains in
	// place for doctor/show to explain.
	for providerName, provider := range rc.Config.Providers {
		preset := strings.ToLower(strings.TrimSpace(provider.Preset))
		if preset == "" {
			continue
		}
		normalized, _, normalizeErr := NormalizeProviderPreset(provider)
		if normalizeErr != nil {
			rc.Diagnostics = append(rc.Diagnostics, Diagnostic{
				Severity:  DiagnosticSeverityError,
				Path:      "providers." + providerName + ".preset",
				Message:   normalizeErr.Error(),
				Continues: true,
				Scope:     "provider " + providerName,
			})
			continue
		}
		rc.Config.Providers[providerName] = normalized
	}

	// Catalog materialization runs before pool reference validation so a pool
	// entry that references a catalog-only model (never declared by the user)
	// resolves instead of reporting a broken reference.
	rc.Diagnostics = append(rc.Diagnostics, materializeCatalogModels(rc, extraRefs...)...)
	rc.Diagnostics = append(rc.Diagnostics, catalogSendOverrideDiagnostics(rc.Config, CatalogResponsesCompat)...)
	rc.CatalogVersion = modelcatalogVersion()
	rc.Diagnostics = append(rc.Diagnostics, ResolveConfiguredPoolRefs(rc.Config)...)
	return rc, nil
}

// addIndexLayer records one sanitized layer in the index, converting parse
// failures into a warning diagnostic instead of failing the whole load: the
// layer's own load path already reported (or never accepted) the problem.
func (rc *ResolvedConfig) addIndexLayer(layer ConfigLayer) {
	idx, err := BuildSourceIndex(layer)
	if err != nil {
		log.Warnf("config %s: origin index unavailable: %v", layer.File, err)
		return
	}
	if rc.Index == nil {
		rc.Index = idx
		return
	}
	mergeSourceIndex(rc.Index, idx)
}

// mergeSourceIndex folds one layer's entries into an existing index. Later
// calls append higher-priority layers; declarations keep their build order.
func mergeSourceIndex(dst, src *SourceIndex) {
	for key, origins := range src.Compaction {
		dst.Compaction[key] = append(dst.Compaction[key], origins...)
	}
	for name, po := range src.Providers {
		existing, ok := dst.Providers[name]
		if !ok {
			existing = ProviderOrigin{Models: make(map[string]ModelOrigin, len(po.Models))}
			dst.Providers[name] = existing
		}
		existing.Preset = append(existing.Preset, po.Preset...)
		for model, mo := range po.Models {
			target, ok := existing.Models[model]
			if !ok {
				target = ModelOrigin{Blocks: make(map[string][]BlockOrigin, len(mo.Blocks))}
				existing.Models[model] = target
			}
			target.Defined = append(target.Defined, mo.Defined...)
			target.Catalog = append(target.Catalog, mo.Catalog...)
			for block, decls := range mo.Blocks {
				target.Blocks[block] = append(target.Blocks[block], decls...)
			}
			target.Limit.Context = append(target.Limit.Context, mo.Limit.Context...)
			target.Limit.Input = append(target.Limit.Input, mo.Limit.Input...)
			target.Limit.Output = append(target.Limit.Output, mo.Limit.Output...)
			existing.Models[model] = target
		}
		dst.Providers[name] = existing
	}
	for pool, refs := range src.ModelPools {
		dst.ModelPools[pool] = append(dst.ModelPools[pool], refs...)
	}
}
