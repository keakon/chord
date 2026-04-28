package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

type ConcurrencyMode string

const (
	ConcurrencyModeExclusive ConcurrencyMode = "exclusive"
	ConcurrencyModeRead      ConcurrencyMode = "read"
	ConcurrencyModeWrite     ConcurrencyMode = "write"
)

type ConcurrencyPolicy struct {
	Resource             string
	Mode                 ConcurrencyMode
	AbortSiblingsOnError bool
}

func defaultConcurrencyPolicy(toolName string) ConcurrencyPolicy {
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "tool"
	}
	return ConcurrencyPolicy{
		Resource: "tool:" + name,
		Mode:     ConcurrencyModeExclusive,
	}
}

func PolicyForTool(registry *Registry, toolName string, args json.RawMessage) ConcurrencyPolicy {
	if registry != nil {
		if tool, ok := registry.Get(toolName); ok {
			if aware, ok := tool.(ConcurrencyAwareTool); ok {
				return normalizeConcurrencyPolicy(toolName, aware.ConcurrencyPolicy(args))
			}
		}
	}
	return defaultConcurrencyPolicy(toolName)
}

func normalizeConcurrencyPolicy(toolName string, policy ConcurrencyPolicy) ConcurrencyPolicy {
	if policy.Mode == "" {
		policy.Mode = ConcurrencyModeExclusive
	}
	if strings.TrimSpace(policy.Resource) == "" {
		if policy.Mode == ConcurrencyModeExclusive {
			return defaultConcurrencyPolicy(toolName)
		}
		policy.Resource = "global"
	}
	return policy
}

func ConcurrencyConflict(a, b ConcurrencyPolicy) bool {
	a = normalizeConcurrencyPolicy("", a)
	b = normalizeConcurrencyPolicy("", b)
	if a.Mode == ConcurrencyModeExclusive || b.Mode == ConcurrencyModeExclusive {
		return true
	}
	if a.Resource != b.Resource {
		return false
	}
	return a.Mode == ConcurrencyModeWrite || b.Mode == ConcurrencyModeWrite
}

func fileToolConcurrencyPolicy(args json.RawMessage, readOnly bool) ConcurrencyPolicy {
	var parsed struct {
		Path string `json:"path"`
	}
	policy := ConcurrencyPolicy{}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return policy
	}
	if strings.TrimSpace(parsed.Path) == "" {
		return policy
	}
	clean := filepath.Clean(parsed.Path)
	if clean == "." || clean == "" {
		return policy
	}
	policy.Resource = "file:" + clean
	if readOnly {
		policy.Mode = ConcurrencyModeRead
	} else {
		policy.Mode = ConcurrencyModeWrite
	}
	return policy
}

func deleteToolConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	if _, err := DecodeDeleteRequest(args); err != nil {
		return ConcurrencyPolicy{}
	}
	return defaultConcurrencyPolicy("Delete")
}

func pathToolConcurrencyPolicy(args json.RawMessage, field string) ConcurrencyPolicy {
	if strings.TrimSpace(field) == "" {
		return ConcurrencyPolicy{}
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(args, &parsed); err != nil {
		return ConcurrencyPolicy{}
	}
	raw, ok := parsed[field]
	if !ok {
		return ConcurrencyPolicy{Resource: "workspace", Mode: ConcurrencyModeRead}
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ConcurrencyPolicy{}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = "."
	}
	clean := filepath.Clean(value)
	if clean == "" {
		clean = "."
	}
	return ConcurrencyPolicy{Resource: "path:" + clean, Mode: ConcurrencyModeRead}
}

func urlToolConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	var parsed struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return ConcurrencyPolicy{}
	}
	url := strings.TrimSpace(parsed.URL)
	if url == "" {
		url = "url:unknown"
	}
	return ConcurrencyPolicy{Resource: "url:" + url, Mode: ConcurrencyModeRead}
}
