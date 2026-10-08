package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/modelcatalog"
)

// CatalogConfigAdvisory is one deterministic difference between an explicit
// model setting and a scalar value documented by the active catalog profile.
// It is deliberately a recommendation: catalog values are verified guidance,
// not a task-specific optimum.
type CatalogConfigAdvisory struct {
	Provider               string                `json:"provider"`
	Model                  string                `json:"model"`
	Field                  string                `json:"field"`
	Path                   string                `json:"path"`
	Current                any                   `json:"current"`
	Recommended            any                   `json:"recommended"`
	CurrentFingerprint     string                `json:"current_fingerprint"`
	RecommendedFingerprint string                `json:"recommended_fingerprint"`
	CatalogID              string                `json:"catalog_id"`
	CatalogVersion         string                `json:"catalog_version"`
	Sources                []modelcatalog.Source `json:"sources,omitempty"`
	CurrentOrigin          CatalogAdvisoryOrigin `json:"current_origin"`
	CanPin                 bool                  `json:"can_pin"`
	CanFollowCatalog       bool                  `json:"can_follow_catalog"`
}

// CatalogAdvisoryOrigin locates the explicit value that triggered an
// advisory. File and line are included so offline tools can explain which
// layer owns the setting.
type CatalogAdvisoryOrigin struct {
	Layer OriginLayer `json:"layer"`
	File  string      `json:"file,omitempty"`
	Line  int         `json:"line,omitempty"`
	Col   int         `json:"col,omitempty"`
}

// OriginRef returns the declaring file and line of the current value, or ""
// when the origin records no file.
func (a CatalogConfigAdvisory) OriginRef() string {
	if a.CurrentOrigin.File == "" {
		return ""
	}
	if a.CurrentOrigin.Line <= 0 {
		return a.CurrentOrigin.File
	}
	return fmt.Sprintf("%s:%d", a.CurrentOrigin.File, a.CurrentOrigin.Line)
}

// sharesDeclaration reports whether two advisories resolve to the same raw
// YAML declaration: same layer, file, line, column, field, catalog profile,
// and value fingerprints, with neither value directly writable. One edit or
// one keep-current acknowledgment covers every advisory in such a group.
// Directly writable leaves each belong to one binding's own models map, so
// they are never shared and always form single-member groups.
func (a CatalogConfigAdvisory) sharesDeclaration(b CatalogConfigAdvisory) bool {
	if a.CanPin || a.CanFollowCatalog || b.CanPin || b.CanFollowCatalog {
		return false
	}
	if a.CurrentOrigin.File == "" || b.CurrentOrigin.File == "" {
		return false
	}
	return a.CurrentOrigin.Layer == b.CurrentOrigin.Layer &&
		a.CurrentOrigin.File == b.CurrentOrigin.File &&
		a.CurrentOrigin.Line == b.CurrentOrigin.Line &&
		a.CurrentOrigin.Col == b.CurrentOrigin.Col &&
		a.Field == b.Field &&
		a.CatalogID == b.CatalogID &&
		a.CatalogVersion == b.CatalogVersion &&
		a.CurrentFingerprint == b.CurrentFingerprint &&
		a.RecommendedFingerprint == b.RecommendedFingerprint
}

// CatalogConfigAdvisoryGroups splits advisories into shared-declaration
// groups, preserving input order. Editing a group's declaration or recording
// one keep-current acknowledgment covers every member; directly writable
// recommendations each form their own group.
func CatalogConfigAdvisoryGroups(advisories []CatalogConfigAdvisory) [][]CatalogConfigAdvisory {
	groups := make([][]CatalogConfigAdvisory, 0, len(advisories))
	for _, advisory := range advisories {
		grouped := false
		for i := range groups {
			if groups[i][0].sharesDeclaration(advisory) {
				groups[i] = append(groups[i], advisory)
				grouped = true
				break
			}
		}
		if !grouped {
			groups = append(groups, []CatalogConfigAdvisory{advisory})
		}
	}
	return groups
}

// CatalogConfigAdvisoryGroup returns the active advisories that share
// target's declaration, including target itself. A directly writable target,
// or one that is not part of advisories, yields just the target.
func CatalogConfigAdvisoryGroup(advisories []CatalogConfigAdvisory, target CatalogConfigAdvisory) []CatalogConfigAdvisory {
	group := make([]CatalogConfigAdvisory, 0, 2)
	for _, advisory := range advisories {
		if advisory.sharesDeclaration(target) {
			group = append(group, advisory)
		}
	}
	if len(group) == 0 {
		return []CatalogConfigAdvisory{target}
	}
	return group
}

// catalogAdvisoryBindingListLimit caps how many bindings one grouped listing
// names before summarizing the rest.
const catalogAdvisoryBindingListLimit = 4

// CatalogConfigAdvisoryBindingList names the provider/model bindings of one
// shared-declaration group for the grouped listing, truncating long lists.
func CatalogConfigAdvisoryBindingList(group []CatalogConfigAdvisory) string {
	labels := make([]string, 0, len(group))
	for _, advisory := range group {
		labels = append(labels, advisory.Provider+"/"+advisory.Model)
	}
	if len(labels) <= catalogAdvisoryBindingListLimit {
		return strings.Join(labels, ", ")
	}
	rest := len(labels) - catalogAdvisoryBindingListLimit
	return strings.Join(labels[:catalogAdvisoryBindingListLimit], ", ") + fmt.Sprintf(", and %d more", rest)
}

type catalogConfigAdvisoryAck struct {
	Provider               string `json:"provider"`
	Model                  string `json:"model"`
	Field                  string `json:"field"`
	CatalogID              string `json:"catalog_id"`
	CatalogVersion         string `json:"catalog_version"`
	CurrentFingerprint     string `json:"current_fingerprint"`
	RecommendedFingerprint string `json:"recommended_fingerprint"`
}

type catalogConfigAdvisoryAckFile struct {
	Acks []catalogConfigAdvisoryAck `json:"acks"`
}

// CatalogConfigAdvisories returns active profile recommendations after
// explicit keep-current acknowledgments have been filtered by their complete
// target and value fingerprint.
func CatalogConfigAdvisories(rc *ResolvedConfig) []CatalogConfigAdvisory {
	all := catalogConfigAdvisories(rc)
	if len(all) == 0 {
		return nil
	}
	acks, err := loadCatalogConfigAdvisoryAcks()
	if err != nil {
		acks = nil
	}
	filtered := slices.DeleteFunc(all, func(advisory CatalogConfigAdvisory) bool {
		return catalogConfigAdvisoryAcknowledged(advisory, acks)
	})
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// CatalogConfigAdvisoryMessages returns the user-facing field-level catalog
// notices for startup toasts.
func CatalogConfigAdvisoryMessages(advisories []CatalogConfigAdvisory) []string {
	return catalogConfigAdvisoryMessages(advisories)
}

// CatalogConfigAdvisoryLogMessages returns the compact runtime-log lines for
// active recommendations, one per distinct declaration. The lines record what
// differs and where the value is declared or inherited; the commands that
// resolve them stay in `chord config advise`.
func CatalogConfigAdvisoryLogMessages(advisories []CatalogConfigAdvisory) []string {
	groups := CatalogConfigAdvisoryGroups(advisories)
	var out []string
	for _, group := range groups {
		out = append(out, catalogConfigAdvisoryLogMessage(group))
	}
	return out
}

// CatalogConfigAdvisorySummary returns one line pointing at the command that
// resolves every active recommendation, or "" when none are active. The count
// matches the grouped log lines: one per distinct declaration.
func CatalogConfigAdvisorySummary(advisories []CatalogConfigAdvisory) string {
	groups := CatalogConfigAdvisoryGroups(advisories)
	if len(groups) == 1 {
		return `1 catalog configuration recommendation outstanding; run "chord config advise" to apply the recommended value or keep the current one`
	}
	if len(groups) == 0 {
		return ""
	}
	return fmt.Sprintf(`%d catalog configuration recommendations outstanding; run "chord config advise" to apply the recommended values or keep the current ones`, len(groups))
}

// ResolvedAdvisories combines the existing effective-config checks with
// profile recommendations that require raw-layer origins.
func ResolvedAdvisories(rc *ResolvedConfig) []string {
	if rc == nil {
		return nil
	}
	out := Advisories(rc.Config)
	return append(out, catalogConfigAdvisoryMessages(CatalogConfigAdvisories(rc))...)
}

func catalogConfigAdvisories(rc *ResolvedConfig) []CatalogConfigAdvisory {
	if rc == nil || rc.Config == nil || rc.Index == nil {
		return nil
	}
	var out []CatalogConfigAdvisory
	for _, providerName := range slices.Sorted(maps.Keys(rc.Config.Providers)) {
		provider := rc.Config.Providers[providerName]
		for _, modelName := range slices.Sorted(maps.Keys(provider.Models)) {
			model := provider.Models[modelName]
			facts, _, profile, found, err := catalogModelDefaults(provider, modelName, model)
			if err != nil || !found || profile == nil {
				continue
			}
			catalogID := strings.TrimSpace(facts.ID)
			if model.Catalog != nil && strings.TrimSpace(model.Catalog.ID) != "" {
				catalogID = strings.TrimSpace(model.Catalog.ID)
			}
			if catalogID == "" {
				continue
			}
			var leaves []catalogProfileLeaf
			for _, key := range slices.Sorted(maps.Keys(profile.Model)) {
				if catalogAdvisoryModelField[key] {
					flattenCatalogProfileLeaves(&leaves, key, profile.Model[key])
				}
			}
			if compaction := profile.Compaction; compaction != nil {
				if compaction.Threshold != nil {
					leaves = append(leaves, catalogProfileLeaf{Field: "compaction.threshold", Value: *compaction.Threshold})
				}
				if compaction.Reminder != nil {
					leaves = append(leaves, catalogProfileLeaf{Field: "compaction.reminder", Value: *compaction.Reminder})
				}
			}
			for _, leaf := range leaves {
				current, ok := rc.Index.ModelField(providerName, modelName, leaf.Field)
				if !ok {
					continue
				}
				currentFingerprint := catalogAdvisoryFingerprint(current.Value)
				recommendedFingerprint := catalogAdvisoryFingerprint(leaf.Value)
				if currentFingerprint == "" || recommendedFingerprint == "" || currentFingerprint == recommendedFingerprint {
					continue
				}
				out = append(out, CatalogConfigAdvisory{
					Provider:               providerName,
					Model:                  modelName,
					Field:                  leaf.Field,
					Path:                   "providers." + providerName + ".models." + modelName + "." + leaf.Field,
					Current:                current.Value,
					Recommended:            leaf.Value,
					CurrentFingerprint:     currentFingerprint,
					RecommendedFingerprint: recommendedFingerprint,
					CatalogID:              catalogID,
					CatalogVersion:         rc.CatalogVersion,
					Sources:                append([]modelcatalog.Source(nil), profile.Sources...),
					CurrentOrigin: CatalogAdvisoryOrigin{
						Layer: current.Layer,
						File:  current.File,
						Line:  current.Line,
						Col:   current.Col,
					},
					CanPin:           current.Editable,
					CanFollowCatalog: canFollowCatalogField(rc.Index, providerName, modelName, leaf.Field, current),
				})
			}
		}
	}
	return out
}

type catalogProfileLeaf struct {
	Field string
	Value any
}

var catalogAdvisoryModelField = map[string]bool{
	"thinking":            true,
	"reasoning":           true,
	"text":                true,
	"parallel_tool_calls": true,
	"prompt_cache":        true,
	"store":               true,
}

func flattenCatalogProfileLeaves(out *[]catalogProfileLeaf, prefix string, value any) {
	switch value := value.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(value)) {
			flattenCatalogProfileLeaves(out, prefix+"."+key, value[key])
		}
	case bool, string, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		if catalogAdvisoryFingerprint(value) != "" {
			*out = append(*out, catalogProfileLeaf{Field: prefix, Value: value})
		}
	}
}

func canFollowCatalogField(idx *SourceIndex, provider, model, field string, current ModelFieldOrigin) bool {
	if idx == nil || !current.Editable {
		return false
	}
	mo, ok := idx.Model(provider, model)
	if !ok {
		return false
	}
	block := field
	if before, _, ok := strings.Cut(field, "."); ok {
		block = before
	}
	nullRank := -1
	for _, declaration := range mo.Blocks[block] {
		if declaration.Null {
			if rank := originLayerRank(declaration.Layer); rank > nullRank {
				nullRank = rank
			}
		}
	}
	fieldDeclarations := 0
	for _, declaration := range mo.Fields[field] {
		if originLayerRank(declaration.Layer) <= nullRank {
			continue
		}
		fieldDeclarations++
	}
	if fieldDeclarations != 1 {
		return false
	}
	count := 0
	for path := range mo.Fields {
		if path != block && !strings.HasPrefix(path, block+".") {
			continue
		}
		if _, ok := idx.ModelField(provider, model, path); ok {
			count++
		}
	}
	return count == 1
}

func catalogConfigAdvisoryMessages(advisories []CatalogConfigAdvisory) []string {
	groups := CatalogConfigAdvisoryGroups(advisories)
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, catalogConfigAdvisoryMessage(group))
	}
	return out
}

func catalogConfigAdvisoryMessage(group []CatalogConfigAdvisory) string {
	advisory := group[0]
	current := formatCatalogAdvisoryValue(advisory.Current)
	recommended := formatCatalogAdvisoryValue(advisory.Recommended)
	keepCommand := "chord config advise " + advisory.Provider + "/" + advisory.Model + " " + advisory.Field + " --keep-current"
	accept := ""
	if advisory.CanFollowCatalog {
		accept = "follow-catalog"
	} else if advisory.CanPin {
		accept = "pin"
	}
	if accept != "" {
		return fmt.Sprintf(
			"%s differs from verified catalog profile %q (current %s, recommended %s); accept with %q or keep the current value with %q",
			advisory.Path,
			advisory.CatalogID,
			current,
			recommended,
			"chord config advise "+advisory.Provider+"/"+advisory.Model+" "+advisory.Field+" --accept "+accept,
			keepCommand,
		)
	}
	if len(group) > 1 {
		return fmt.Sprintf(
			"%s differs from verified catalog profile %q (current %s, recommended %s); declared once at %s and shared by %d bindings; edit that line to %s to change every binding that inherits it, or keep all %d with %q",
			advisory.Path,
			advisory.CatalogID,
			current,
			recommended,
			advisory.OriginRef(),
			len(group),
			recommended,
			len(group),
			keepCommand,
		)
	}
	origin := "inherited through YAML"
	adopt := ""
	if ref := advisory.OriginRef(); ref != "" {
		origin = "inherited through YAML from " + ref
		adopt = "set " + recommended + " there to change every binding that inherits it, or "
	}
	return fmt.Sprintf(
		"%s differs from verified catalog profile %q (current %s, recommended %s); %s; %sexpand the inherited entry to add an explicit override, or keep the current value with %q",
		advisory.Path,
		advisory.CatalogID,
		current,
		recommended,
		origin,
		adopt,
		keepCommand,
	)
}

// catalogConfigAdvisoryLogMessage renders one compact runtime-log line: what
// differs from the verified profile, and where the value is declared or
// inherited. Commands stay out of the log; `chord config advise` prints them.
func catalogConfigAdvisoryLogMessage(group []CatalogConfigAdvisory) string {
	advisory := group[0]
	message := fmt.Sprintf(
		"%s differs from verified catalog profile %q (current %s, recommended %s)",
		advisory.Path,
		advisory.CatalogID,
		formatCatalogAdvisoryValue(advisory.Current),
		formatCatalogAdvisoryValue(advisory.Recommended),
	)
	ref := advisory.OriginRef()
	if len(group) > 1 {
		return fmt.Sprintf("%s; declared once at %s, shared by %d bindings", message, ref, len(group))
	}
	if advisory.CanPin || advisory.CanFollowCatalog {
		if ref == "" {
			return message
		}
		return message + "; declared at " + ref
	}
	if ref == "" {
		return message + "; inherited through YAML"
	}
	return message + "; inherited through YAML from " + ref
}

func formatCatalogAdvisoryValue(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func catalogAdvisoryFingerprint(value any) string {
	canonical, ok := canonicalCatalogAdvisoryScalar(value)
	if !ok {
		return ""
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func canonicalCatalogAdvisoryScalar(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return "string:" + strconv.Quote(value), true
	case bool:
		return "bool:" + strconv.FormatBool(value), true
	case int:
		return "number:" + strconv.Itoa(value), true
	case int8:
		return "number:" + strconv.FormatInt(int64(value), 10), true
	case int16:
		return "number:" + strconv.FormatInt(int64(value), 10), true
	case int32:
		return "number:" + strconv.FormatInt(int64(value), 10), true
	case int64:
		return "number:" + strconv.FormatInt(value, 10), true
	case uint:
		return "number:" + strconv.FormatUint(uint64(value), 10), true
	case uint8:
		return "number:" + strconv.FormatUint(uint64(value), 10), true
	case uint16:
		return "number:" + strconv.FormatUint(uint64(value), 10), true
	case uint32:
		return "number:" + strconv.FormatUint(uint64(value), 10), true
	case uint64:
		return "number:" + strconv.FormatUint(value, 10), true
	case float32:
		return canonicalCatalogAdvisoryFloat(float64(value))
	case float64:
		return canonicalCatalogAdvisoryFloat(value)
	case json.Number:
		return "number:" + value.String(), true
	default:
		return "", false
	}
}

func canonicalCatalogAdvisoryFloat(value float64) (string, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", false
	}
	if value == 0 {
		value = 0
	}
	return "number:" + strconv.FormatFloat(value, 'g', -1, 64), true
}

// RecordCatalogConfigAdvisoryAcknowledgments suppresses active
// recommendations with one exact acknowledgment each. Pass a whole
// shared-declaration group to resolve it with one command. A changed current
// value, recommendation, catalog ID, or catalog version automatically
// re-arms the affected entries.
func RecordCatalogConfigAdvisoryAcknowledgments(advisories ...CatalogConfigAdvisory) error {
	if len(advisories) == 0 {
		return nil
	}
	path, err := catalogConfigAdvisoryAckPath()
	if err != nil {
		return err
	}
	lock, err := LockConfigMutation(path)
	if err != nil {
		return fmt.Errorf("lock catalog config advisory state: %w", err)
	}
	defer func() { _ = lock.Close() }()
	var state catalogConfigAdvisoryAckFile
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	changed := false
	for _, advisory := range advisories {
		entry := catalogConfigAdvisoryAck{
			Provider:               advisory.Provider,
			Model:                  advisory.Model,
			Field:                  advisory.Field,
			CatalogID:              advisory.CatalogID,
			CatalogVersion:         advisory.CatalogVersion,
			CurrentFingerprint:     advisory.CurrentFingerprint,
			RecommendedFingerprint: advisory.RecommendedFingerprint,
		}
		if slices.Contains(state.Acks, entry) {
			continue
		}
		state.Acks = append(state.Acks, entry)
		changed = true
	}
	if !changed {
		return nil
	}
	data, err := json.MarshalIndent(&state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := writeConfigFileAtomicallyReplace(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func catalogConfigAdvisoryAckPath() (string, error) {
	home, err := ConfigHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve config home: %w", err)
	}
	return filepath.Join(home, "model-config-advisories.json"), nil
}

func loadCatalogConfigAdvisoryAcks() ([]catalogConfigAdvisoryAck, error) {
	path, err := catalogConfigAdvisoryAckPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state catalogConfigAdvisoryAckFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return state.Acks, nil
}

func catalogConfigAdvisoryAcknowledged(advisory CatalogConfigAdvisory, acks []catalogConfigAdvisoryAck) bool {
	for _, ack := range acks {
		if ack.Provider == advisory.Provider &&
			ack.Model == advisory.Model &&
			ack.Field == advisory.Field &&
			ack.CatalogID == advisory.CatalogID &&
			ack.CatalogVersion == advisory.CatalogVersion &&
			ack.CurrentFingerprint == advisory.CurrentFingerprint &&
			ack.RecommendedFingerprint == advisory.RecommendedFingerprint {
			return true
		}
	}
	return false
}
