package llm

import "strings"

type oversizeRegistry struct {
	byTarget map[string]struct{}
}

func newOversizeRegistry() *oversizeRegistry {
	return &oversizeRegistry{byTarget: make(map[string]struct{})}
}

func (r *oversizeRegistry) mark(providerName, modelID, variant string) {
	if r == nil {
		return
	}
	if key := oversizeTargetKey(providerName, modelID, variant); key != "" {
		r.byTarget[key] = struct{}{}
	}
}

func (r *oversizeRegistry) seen(providerName, modelID, variant string) bool {
	if r == nil {
		return false
	}
	key := oversizeTargetKey(providerName, modelID, variant)
	if key == "" {
		return false
	}
	_, ok := r.byTarget[key]
	return ok
}

func oversizeTargetKey(providerName, modelID, variant string) string {
	providerName = strings.TrimSpace(providerName)
	modelID = strings.TrimSpace(modelID)
	variant = strings.TrimSpace(variant)
	if providerName == "" || modelID == "" {
		return ""
	}
	key := providerName + "/" + modelID
	if variant != "" {
		key += "@" + variant
	}
	return key
}
