package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

func unwrapToolArgs(raw json.RawMessage) json.RawMessage {
	for len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			break
		}
		raw = json.RawMessage(s)
	}
	return raw
}

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
				return normalizeConcurrencyPolicy(toolName, aware.ConcurrencyPolicy(unwrapToolArgs(args)))
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
	if !resourceOverlap(a.Resource, b.Resource) {
		return false
	}
	return a.Mode == ConcurrencyModeWrite || b.Mode == ConcurrencyModeWrite
}

func resourceOverlap(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if a == "workspace" || b == "workspace" {
		return true
	}
	kindA, pathA, okA := splitConcurrencyResource(a)
	kindB, pathB, okB := splitConcurrencyResource(b)
	if !okA || !okB {
		return false
	}
	switch {
	case kindA == "file" && kindB == "file":
		return pathA == pathB
	case kindA == "path" && kindB == "path":
		return pathContainsResourcePath(pathA, pathB) || pathContainsResourcePath(pathB, pathA)
	case kindA == "path" && kindB == "file":
		return pathContainsResourcePath(pathA, pathB)
	case kindA == "file" && kindB == "path":
		return pathContainsResourcePath(pathB, pathA)
	default:
		return false
	}
}

func splitConcurrencyResource(resource string) (kind, path string, ok bool) {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return "", "", false
	}
	idx := strings.IndexByte(resource, ':')
	if idx <= 0 || idx >= len(resource)-1 {
		return "", "", false
	}
	return resource[:idx], filepath.Clean(resource[idx+1:]), true
}

func fileToolConcurrencyPolicy(args json.RawMessage, readOnly bool) ConcurrencyPolicy {
	var parsed struct {
		Path string `json:"path"`
	}
	policy := ConcurrencyPolicy{}
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return policy
	}
	if strings.TrimSpace(parsed.Path) == "" {
		return policy
	}
	resolved, err := resolveToolPath(parsed.Path)
	if err != nil {
		return policy
	}
	if resolved == "." || resolved == "" {
		return policy
	}
	policy.Resource = "file:" + resolved
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
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
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
	resolved, err := resolveToolPath(value)
	if err != nil {
		return ConcurrencyPolicy{}
	}
	if resolved == "" {
		resolved = "."
	}
	return ConcurrencyPolicy{Resource: "path:" + resolved, Mode: ConcurrencyModeRead}
}

func urlToolConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	var parsed struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return ConcurrencyPolicy{}
	}
	url := strings.TrimSpace(parsed.URL)
	if url == "" {
		url = "url:unknown"
	}
	return ConcurrencyPolicy{Resource: "url:" + url, Mode: ConcurrencyModeRead}
}
