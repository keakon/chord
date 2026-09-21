package skill

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Declared skill resource checks. Paths are relative to the skill root and
// resolve against the symlink-resolved root, so a skill directory that is
// itself a symlink does not turn every resource into an out-of-root report.
//
// This is a diagnostic contract, not a security boundary: the model still
// reads resources through the Read permission gate, and hardlinks inside the
// root are indistinguishable from regular files.

const (
	ResourceStatusPassed  = "passed"
	ResourceStatusFailed  = "failed"
	ResourceStatusWarning = "warning"
)

const (
	ResourceReasonOK        = "ok"
	ResourceReasonMissing   = "resource_missing"
	ResourceReasonEmpty     = "resource_empty"
	ResourceReasonOutOfRoot = "resource_out_of_root"
	ResourceReasonNotReg    = "resource_not_regular"
	ResourceReasonInvalid   = "resource_invalid"
)

// ResourceEntry is the per-path verdict for one declared resource or one
// `${CHORD_SKILL_DIR}/...` placeholder reference found in the skill body.
type ResourceEntry struct {
	Declared    string // path as declared (or placeholder ref relative to the root)
	Path        string // absolute filesystem path that was checked
	Status      string // passed | failed | warning
	Reason      string
	Detail      string
	Placeholder bool // true when the entry comes from a body placeholder scan
}

// NormalizeResourceList trims and cleans resource declarations so equivalent
// spellings (such as "./references/a.md" and "references/a.md") share one
// canonical form before digest comparison and filesystem checks.
func NormalizeResourceList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		cleaned := filepath.Clean(filepath.FromSlash(trimmed))
		out = append(out, filepath.ToSlash(cleaned))
	}
	return out
}

// SummarizeResourceStatus folds per-entry verdicts into the skill-level
// resources dimension: failed wins over warning, warning wins over passed,
// and an empty entry list reports none.
func SummarizeResourceStatus(entries []ResourceEntry) string {
	if len(entries) == 0 {
		return "none"
	}
	warning := false
	for _, entry := range entries {
		switch entry.Status {
		case ResourceStatusFailed:
			return ResourceStatusFailed
		case ResourceStatusWarning:
			warning = true
		}
	}
	if warning {
		return ResourceStatusWarning
	}
	return ResourceStatusPassed
}

// CheckDeclaredResources validates frontmatter-declared resources against the
// skill root. Missing, out-of-root, and non-regular targets fail; empty files
// warn. The caller decides how the verdict affects visibility: resource
// problems never mark a skill as unloadable.
func CheckDeclaredResources(rootDir string, resources []string) []ResourceEntry {
	entries := make([]ResourceEntry, 0, len(resources))
	for _, raw := range resources {
		declared := strings.TrimSpace(raw)
		entries = append(entries, checkOneResource(rootDir, declared, false))
	}
	return entries
}

func checkOneResource(rootDir, declared string, placeholder bool) ResourceEntry {
	entry := ResourceEntry{Declared: declared, Placeholder: placeholder}
	if declared == "" {
		entry.Status = ResourceStatusFailed
		entry.Reason = ResourceReasonInvalid
		entry.Detail = "empty resource declaration"
		entry.Path = strings.TrimSpace(rootDir)
		return entry
	}
	if filepath.IsAbs(declared) || filepath.IsAbs(filepath.FromSlash(declared)) {
		entry.Status = ResourceStatusFailed
		if placeholder {
			entry.Status = ResourceStatusWarning
		}
		entry.Reason = ResourceReasonOutOfRoot
		entry.Detail = "absolute paths are not allowed; declare a root-relative path"
		entry.Path = filepath.FromSlash(declared)
		return entry
	}
	rootAbs, err := filepath.Abs(rootDir)
	if err != nil {
		rootAbs = rootDir
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		rootReal = rootAbs
	}
	relOS := filepath.FromSlash(filepath.Clean(filepath.FromSlash(declared)))
	joined := filepath.Join(rootAbs, relOS)
	entry.Path = joined
	targetReal, err := filepath.EvalSymlinks(joined)
	if err != nil {
		entry.Status = ResourceStatusFailed
		if placeholder {
			entry.Status = ResourceStatusWarning
		}
		entry.Reason = ResourceReasonMissing
		entry.Detail = "resource not found: " + err.Error()
		return entry
	}
	rel, err := filepath.Rel(rootReal, targetReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		entry.Status = ResourceStatusFailed
		if placeholder {
			entry.Status = ResourceStatusWarning
		}
		entry.Reason = ResourceReasonOutOfRoot
		entry.Detail = "resource resolves outside the skill root: " + targetReal
		entry.Path = targetReal
		return entry
	}
	info, err := os.Stat(targetReal)
	if err != nil {
		entry.Status = ResourceStatusFailed
		if placeholder {
			entry.Status = ResourceStatusWarning
		}
		entry.Reason = ResourceReasonMissing
		entry.Detail = "resource cannot be read: " + err.Error()
		entry.Path = targetReal
		return entry
	}
	entry.Path = targetReal
	if info.IsDir() || !info.Mode().IsRegular() {
		entry.Status = ResourceStatusFailed
		if placeholder {
			entry.Status = ResourceStatusWarning
		}
		entry.Reason = ResourceReasonNotReg
		entry.Detail = "resource is not a regular file"
		return entry
	}
	if info.Size() == 0 {
		entry.Status = ResourceStatusWarning
		entry.Reason = ResourceReasonEmpty
		entry.Detail = "resource is empty"
		return entry
	}
	entry.Status = ResourceStatusPassed
	entry.Reason = ResourceReasonOK
	return entry
}

var skillDirPlaceholderRE = regexp.MustCompile(`\$\{CHORD_SKILL_DIR\}/([^\s"'` + "`" + `\)\]]+)`)

// PlaceholderResourceRefs extracts root-relative paths from literal
// `${CHORD_SKILL_DIR}/<path>` references in a skill body. Bare relative
// paths in prose are intentionally ignored: they cannot be told apart from
// ordinary text without heuristics that would flood the report.
func PlaceholderResourceRefs(content string) []string {
	matches := skillDirPlaceholderRE.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		ref := strings.TrimSpace(match[1])
		ref = strings.TrimRight(ref, ".,;:!?")
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		normalized := NormalizeResourceList([]string{ref})
		if len(normalized) == 0 {
			continue
		}
		ref = normalized[0]
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

// CheckPlaceholderResources validates `${CHORD_SKILL_DIR}/...` references and
// returns only the problematic ones as warnings. Clean references produce no
// entries so placeholder hygiene never flips a skill with no declared
// resources from none to passed.
func CheckPlaceholderResources(rootDir, content string) []ResourceEntry {
	refs := PlaceholderResourceRefs(content)
	if len(refs) == 0 {
		return nil
	}
	var out []ResourceEntry
	for _, ref := range refs {
		entry := checkOneResource(rootDir, ref, true)
		if entry.Status == ResourceStatusPassed {
			continue
		}
		entry.Status = ResourceStatusWarning
		out = append(out, entry)
	}
	return out
}
