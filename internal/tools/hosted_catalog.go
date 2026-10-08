package tools

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/toolname"
)

// ValidateHostedToolCatalog checks the entire catalog before registration so
// a bad entry cannot replace a built-in tool or depend on map iteration order.
func ValidateHostedToolCatalog(user map[string]config.HostedToolConfig, registry *Registry) error {
	reserved := []string{NameRead, NameWrite, NameEdit, NameApplyPatch, NameDelete, NameGrep, NameGlob, NameWebFetch, NameShell, NameJobOutput, NameJobList, NameJobKill, NameTodoWrite, NameQuestion, NameDone, NameDelegate, NameNotify, NameSkill, NameHandoff, NameEscalate, NameCancel, NameComplete, NameSaveArtifact, NameReadArtifact, NameViewImage, NameCompactContext}
	seen := make(map[string]string)
	for _, raw := range slices.Sorted(maps.Keys(user)) {
		name := NormalizeName(raw)
		if !toolname.IsValid(name) {
			return fmt.Errorf("hosted tool %q has an invalid name", raw)
		}
		if previous, ok := seen[name]; ok {
			return fmt.Errorf("hosted tools %q and %q resolve to the same name %q", previous, raw, name)
		}
		seen[name] = raw
		if user[raw].TimeoutSeconds < 0 {
			return fmt.Errorf("hosted tool %q timeout_s must be non-negative", raw)
		}
		for _, family := range slices.Sorted(maps.Keys(user[raw].Declarations)) {
			decl := user[raw].Declarations[family]
			if err := config.ValidateHostedToolHeaders(decl.Headers); err != nil {
				return fmt.Errorf("hosted tool %q declaration %q: %w", raw, family, err)
			}
		}
		if slices.Contains(reserved, name) || strings.HasPrefix(name, toolname.MCPToolPrefix) {
			return fmt.Errorf("hosted tool %q uses a reserved tool name", raw)
		}
		if registry != nil {
			if _, ok := registry.Get(name); ok {
				return fmt.Errorf("hosted tool %q conflicts with a registered tool", raw)
			}
		}
		for _, path := range user[raw].ImagePaths {
			for segment := range strings.SplitSeq(path, ".") {
				if strings.TrimSpace(segment) == "" {
					return fmt.Errorf("hosted tool %q has invalid image path %q", raw, path)
				}
			}
		}
	}
	// Validate the merged entries so built-in partial overrides remain valid.
	catalog := ResolveHostedToolCatalog(user)
	for _, name := range slices.Sorted(maps.Keys(catalog)) {
		spec := catalog[name]
		if kind, _ := spec.Parameters["type"].(string); kind != "object" {
			return fmt.Errorf("hosted tool %q parameters must be an object schema", name)
		}
		if _, err := json.Marshal(spec.Parameters); err != nil {
			return fmt.Errorf("hosted tool %q parameters: %w", name, err)
		}
		if len(spec.Declarations) == 0 {
			return fmt.Errorf("hosted tool %q needs a messages or responses declaration", name)
		}
		for _, family := range slices.Sorted(maps.Keys(spec.Declarations)) {
			decl := spec.Declarations[family]
			if family != config.ProviderTypeMessages && family != config.ProviderTypeResponses {
				return fmt.Errorf("hosted tool %q has unsupported declaration family %q", name, family)
			}
			if kind, _ := decl.Tool["type"].(string); strings.TrimSpace(kind) == "" {
				return fmt.Errorf("hosted tool %q declaration %q needs a non-empty tool type", name, family)
			}
			if decl.Force != nil {
				switch value := decl.Force.(type) {
				case string:
					if strings.TrimSpace(value) == "" {
						return fmt.Errorf("hosted tool %q declaration %q force must not be empty", name, family)
					}
				case map[string]any:
					if len(value) == 0 {
						return fmt.Errorf("hosted tool %q declaration %q force must not be empty", name, family)
					}
				default:
					return fmt.Errorf("hosted tool %q declaration %q force must be a string or object", name, family)
				}
			}
			if _, err := json.Marshal(decl); err != nil {
				return fmt.Errorf("hosted tool %q declaration %q: %w", name, family, err)
			}
			if err := config.ValidateHostedToolHeaders(decl.Headers); err != nil {
				return fmt.Errorf("hosted tool %q declaration %q: %w", name, family, err)
			}
		}
	}
	return nil
}
