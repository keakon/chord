package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/pathutil"
)

// unwrapToolArgs peels JSON string layers so tool handlers receive a JSON
// object, not a string. It must stay behaviourally identical to
// llm.UnwrapToolArgs without adding a dependency on the provider package.
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

// ConcurrencyPolicy declares every resource one tool invocation touches. All
// resources share one mode.
type ConcurrencyPolicy struct {
	Resources            []string
	Mode                 ConcurrencyMode
	AbortSiblingsOnError bool
}

func defaultConcurrencyPolicy(toolName string) ConcurrencyPolicy {
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "tool"
	}
	return ConcurrencyPolicy{
		Resources: []string{"tool:" + name},
		Mode:      ConcurrencyModeExclusive,
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
	policy.Resources = NormalizeConcurrencyResources(policy.Resources)
	if len(policy.Resources) == 0 {
		if policy.Mode == ConcurrencyModeExclusive {
			return defaultConcurrencyPolicy(toolName)
		}
		policy.Resources = []string{"global"}
	}
	return policy
}

// NormalizeConcurrencyResources trims, drops empty entries, de-duplicates and
// sorts a resource set. The result is a freshly allocated slice: a queued
// policy never aliases the caller's memory, so later edits to the input cannot
// change a request that is already waiting.
func NormalizeConcurrencyResources(resources []string) []string {
	if len(resources) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(resources))
	seen := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		resource = strings.TrimSpace(resource)
		if resource == "" {
			continue
		}
		if _, duplicate := seen[resource]; duplicate {
			continue
		}
		seen[resource] = struct{}{}
		normalized = append(normalized, resource)
	}
	sort.Strings(normalized)
	return normalized
}

// Conflicts reports whether this policy conflicts with another call in one
// model response. Both policies must already be normalized — PolicyForInstance
// returns them that way — so the comparison never copies or re-sorts the
// resource sets. Any exclusive call is a batch boundary regardless of what the
// two sets name; otherwise an overlapping pair with a write on either side
// conflicts.
func (p ConcurrencyPolicy) Conflicts(other ConcurrencyPolicy) bool {
	if p.Mode == ConcurrencyModeExclusive || other.Mode == ConcurrencyModeExclusive {
		return true
	}
	if !resourceSetsOverlap(p.Resources, other.Resources) {
		return false
	}
	return p.Mode == ConcurrencyModeWrite || other.Mode == ConcurrencyModeWrite
}

// LeaseConflicts reports whether two executions contend for the same
// workspace resource. Unlike Conflicts — which batches calls inside a single
// turn and treats exclusive mode as conflicting with everything — this
// cross-agent variant scopes exclusivity to the declared resources:
// `process:shell` serializes against other shells but not against reads or
// writes of unrelated files, and a `workspace` resource still overlaps
// everything. Both policies must already be normalized.
func (p ConcurrencyPolicy) LeaseConflicts(other ConcurrencyPolicy) bool {
	if !resourceSetsOverlap(p.Resources, other.Resources) {
		return false
	}
	return !leaseShareableMode(p.Mode) || !leaseShareableMode(other.Mode)
}

func resourceSetsOverlap(a, b []string) bool {
	for _, left := range a {
		for _, right := range b {
			if resourceOverlap(left, right) {
				return true
			}
		}
	}
	return false
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
		// A file target can be a directory symlink being deleted. Its
		// descendants depend on that entry even when their final inodes differ.
		return resourcePathsOverlap(pathA, pathB)
	case kindA == "path" && kindB == "path":
		return resourcePathsOverlap(pathA, pathB)
	case kindA == "path" && kindB == "file":
		return resourcePathsOverlap(pathA, pathB)
	case kindA == "file" && kindB == "path":
		return resourcePathsOverlap(pathA, pathB)
	default:
		return false
	}
}

// Check entry dependencies before resolving aliases: deleting an ancestor
// symlink invalidates a descendant spelling even when it points outside it.
func resourcePathsOverlap(a, b string) bool {
	if pathContainsResourcePath(a, b) || pathContainsResourcePath(b, a) {
		return true
	}
	resolvedA, resolvedB := a, b
	if resolved, ok := pathutil.ResolveSymlinksBestEffort(a); ok {
		resolvedA = resolved
	}
	if resolved, ok := pathutil.ResolveSymlinksBestEffort(b); ok {
		resolvedB = resolved
	}
	if pathContainsResourcePath(resolvedA, resolvedB) || pathContainsResourcePath(resolvedB, resolvedA) {
		return true
	}
	infoA, errA := os.Stat(resolvedA)
	infoB, errB := os.Stat(resolvedB)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
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
	return ConcurrencyPolicy{Resources: []string{"file:" + resolved}, Mode: mode}
}

func deleteToolConcurrencyPolicyInDir(args json.RawMessage, baseDir string) ConcurrencyPolicy {
	req, err := DecodeDeleteRequestInDir(args, baseDir)
	if err != nil {
		return ConcurrencyPolicy{}
	}
	resources := make([]string, 0, len(req.Paths))
	for _, path := range req.Paths {
		// The decoded paths are the ones execution will remove; absolute form
		// keeps two agents' spellings of one file comparable.
		absolute, absErr := filepath.Abs(path)
		if absErr != nil {
			return ConcurrencyPolicy{Resources: []string{"workspace"}, Mode: ConcurrencyModeExclusive}
		}
		resources = append(resources, "file:"+absolute)
	}
	if len(resources) == 0 {
		return ConcurrencyPolicy{Resources: []string{"workspace"}, Mode: ConcurrencyModeExclusive}
	}
	return ConcurrencyPolicy{Resources: resources, Mode: ConcurrencyModeWrite}
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
		return ConcurrencyPolicy{Resources: []string{"workspace"}, Mode: ConcurrencyModeRead}
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
	return ConcurrencyPolicy{Resources: []string{"path:" + resolved}, Mode: ConcurrencyModeRead}
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
		return ConcurrencyPolicy{Resources: []string{"workspace"}, Mode: ConcurrencyModeRead}
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
	return ConcurrencyPolicy{Resources: []string{"path:" + resolved}, Mode: ConcurrencyModeRead}
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
	return ConcurrencyPolicy{Resources: []string{"url:" + url}, Mode: ConcurrencyModeRead}
}
