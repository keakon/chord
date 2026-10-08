package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/pathutil"
)

// unwrapToolArgs peels JSON string layers so tool handlers receive a JSON
// object, not a string. It must stay behaviourally identical to
// llm.UnwrapToolArgs, which is kept separate on purpose: that copy decodes with
// sonic on the streaming hot path, and sharing one implementation would either
// pull sonic into this package or slow the provider path down.
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
	ConcurrencyModeExclusive  ConcurrencyMode = "exclusive"
	ConcurrencyModeRead       ConcurrencyMode = "read"
	ConcurrencyModeConcurrent ConcurrencyMode = "concurrent"
	ConcurrencyModeWrite      ConcurrencyMode = "write"
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

// PolicyForInstance resolves resources on the instance bound to this call.
func PolicyForInstance(tool Tool, toolName string, args json.RawMessage) ConcurrencyPolicy {
	if aware, ok := tool.(ConcurrencyAwareTool); ok {
		return normalizeConcurrencyPolicy(toolName, aware.ConcurrencyPolicy(unwrapToolArgs(args)))
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

// WorkspaceLeaseConflict reports whether two tool executions contend for the
// same workspace resource. Unlike ConcurrencyConflict — which batches calls
// inside a single turn and treats exclusive mode as conflicting with
// everything — this cross-agent variant scopes exclusivity to the declared
// resource: `process:shell` serializes against other shells but not against
// reads or writes of unrelated files, and a `workspace` resource still
// overlaps everything. Tools whose default policy names only `tool:<name>`
// therefore serialize per tool instead of freezing every other agent.
//
// Read and concurrent calls share a resource: neither claims the exclusive
// right to change it that a write or exclusive call claims.
func WorkspaceLeaseConflict(a, b ConcurrencyPolicy) bool {
	a = normalizeConcurrencyPolicy("", a)
	b = normalizeConcurrencyPolicy("", b)
	if !resourceOverlap(a.Resource, b.Resource) {
		return false
	}
	return !leaseShareableMode(a.Mode) || !leaseShareableMode(b.Mode)
}

// leaseShareableMode reports whether a holder with this mode may share the
// resource with another holder. A concurrent call promises only that its
// resource tolerates overlapping calls, not that it is read-only.
func leaseShareableMode(mode ConcurrencyMode) bool {
	return mode == ConcurrencyModeRead || mode == ConcurrencyModeConcurrent
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
		return sameResourcePath(pathA, pathB)
	case kindA == "path" && kindB == "path":
		return sameResourcePath(pathA, pathB) || resourcePathContains(pathA, pathB) || resourcePathContains(pathB, pathA)
	case kindA == "path" && kindB == "file":
		return resourcePathContains(pathA, pathB)
	case kindA == "file" && kindB == "path":
		return resourcePathContains(pathB, pathA)
	default:
		return false
	}
}

// sameResourcePath reports whether two resource paths name the same physical
// file. Equal spellings are already known to match; different ones are
// re-checked through symlinked prefixes and, when both paths exist, the inode,
// so a directory alias cannot slip past the conflict check.
func sameResourcePath(a, b string) bool {
	if a == b {
		return true
	}
	resolvedA, resolvedB := a, b
	if resolved, ok := pathutil.ResolveSymlinksBestEffort(a); ok {
		resolvedA = resolved
	}
	if resolved, ok := pathutil.ResolveSymlinksBestEffort(b); ok {
		resolvedB = resolved
	}
	if resolvedA == resolvedB {
		return true
	}
	infoA, errA := os.Stat(resolvedA)
	infoB, errB := os.Stat(resolvedB)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

// resourcePathContains is pathContainsResourcePath after the same best-effort
// symlink resolution, so a directory alias still contains the files it
// physically holds.
func resourcePathContains(basePath, targetPath string) bool {
	if pathContainsResourcePath(basePath, targetPath) {
		return true
	}
	base, target := basePath, targetPath
	if resolved, ok := pathutil.ResolveSymlinksBestEffort(base); ok {
		base = resolved
	}
	if resolved, ok := pathutil.ResolveSymlinksBestEffort(target); ok {
		target = resolved
	}
	return pathContainsResourcePath(base, target)
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

func fileToolConcurrencyPolicyInDir(args json.RawMessage, readOnly bool, baseDir string) ConcurrencyPolicy {
	var parsed struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return ConcurrencyPolicy{}
	}
	return filePathConcurrencyPolicyInDir(parsed.Path, readOnly, baseDir)
}

func filePathConcurrencyPolicyInDir(path string, readOnly bool, baseDir string) ConcurrencyPolicy {
	if strings.TrimSpace(path) == "" {
		return ConcurrencyPolicy{}
	}
	resolved, err := resolveToolPathInDir(path, baseDir)
	if err != nil || resolved == "." || resolved == "" {
		return ConcurrencyPolicy{}
	}
	mode := ConcurrencyModeWrite
	if readOnly {
		mode = ConcurrencyModeRead
	}
	return ConcurrencyPolicy{Resource: "file:" + resolved, Mode: mode}
}

func deleteToolConcurrencyPolicyInDir(args json.RawMessage, baseDir string) ConcurrencyPolicy {
	if _, err := DecodeDeleteRequestInDir(args, baseDir); err != nil {
		return ConcurrencyPolicy{}
	}
	return defaultConcurrencyPolicy(NameDelete)
}

func pathToolConcurrencyPolicyInDir(args json.RawMessage, field string, baseDir string) ConcurrencyPolicy {
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
	resolved, err := resolveToolPathInDir(value, baseDir)
	if err != nil {
		return ConcurrencyPolicy{}
	}
	if resolved == "" {
		resolved = "."
	}
	return ConcurrencyPolicy{Resource: "path:" + resolved, Mode: ConcurrencyModeRead}
}

func pathsToolConcurrencyPolicyInDir(args json.RawMessage, field string, baseDir string) ConcurrencyPolicy {
	if strings.TrimSpace(field) == "" {
		return ConcurrencyPolicy{}
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return ConcurrencyPolicy{}
	}
	raw, ok := parsed[field]
	if !ok {
		return pathToolConcurrencyPolicyInDir(args, "path", baseDir)
	}
	// Mirror the executor's scalar->array coercion so a bare-string path still
	// gets a precise per-path read policy instead of a workspace-wide one.
	values, _, err := DecodeStringOrList(raw)
	if err != nil || len(values) != 1 {
		return ConcurrencyPolicy{Resource: "workspace", Mode: ConcurrencyModeRead}
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		value = "."
	}
	resolved, err := resolveToolPathInDir(value, baseDir)
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
