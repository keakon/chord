package skill

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/keakon/chord/internal/permission"
)

// PathDescriptor is immutable catalog metadata. Invalid conditional patterns
// remain conditional, but never match; they do not remove name-based access.
type PathDescriptor struct {
	Conditional bool
	Patterns    []string
	Diagnostic  string
	Identity    string
	Revision    string
}

func BuildPathDescriptor(meta *Meta) PathDescriptor {
	if meta == nil {
		return PathDescriptor{}
	}
	d := PathDescriptor{Conditional: len(meta.Paths) > 0}
	location, err := filepath.Abs(meta.Location)
	if err == nil {
		location = filepath.Clean(location)
	} else {
		location = meta.Location
	}
	h := sha256.New()
	for _, part := range []string{strings.TrimSpace(meta.Name), location} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write([]byte(part))
	}
	d.Identity = hex.EncodeToString(h.Sum(nil))
	for _, pattern := range meta.Paths {
		if !validActivationPattern(pattern) {
			d.Diagnostic = fmt.Sprintf("invalid skill path pattern %q; use checkout-relative forward-slash globs", pattern)
			break
		}
	}
	d.Patterns = slices.Clone(meta.Paths)
	slices.Sort(d.Patterns)
	d.Patterns = slices.Compact(d.Patterns)
	allowed := slices.Clone(meta.AllowedTools)
	slices.Sort(allowed)
	allowed = slices.Compact(allowed)
	revision := struct {
		Name, Description, WhenToUse, ArgumentHint, Context, Model, Effort string
		AllowedTools, Paths, Resources                                     []string
		DisableModelInvocation                                             bool
	}{meta.Name, meta.Description, meta.WhenToUse, meta.ArgsHint, meta.Context, meta.Model, meta.Effort, allowed, d.Patterns, meta.Resources, meta.DisableModelInvocation}
	data, _ := json.Marshal(revision)
	sum := sha256.Sum256(data)
	d.Revision = hex.EncodeToString(sum[:])
	return d
}

func validActivationPattern(pattern string) bool {
	if pattern == "" || strings.TrimSpace(pattern) != pattern || strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "!") || strings.Contains(pattern, "\\") || strings.Contains(pattern, "//") || strings.HasSuffix(pattern, "/") || (len(pattern) >= 2 && ((pattern[0] >= 'A' && pattern[0] <= 'Z') || (pattern[0] >= 'a' && pattern[0] <= 'z')) && pattern[1] == ':') {
		return false
	}
	for segment := range strings.SplitSeq(pattern, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return doublestar.ValidatePattern(pattern)
}

// Match takes a normalized, checkout-relative file path and never opens files.
func (d PathDescriptor) Match(relativePath string) bool {
	if !d.Conditional || d.Diagnostic != "" || relativePath == "" || strings.HasPrefix(relativePath, "/") || strings.Contains(relativePath, "\\") {
		return false
	}
	for segment := range strings.SplitSeq(relativePath, "/") {
		if segment == "." || segment == ".." || segment == "" {
			return false
		}
	}
	for _, pattern := range d.Patterns {
		if doublestar.MatchUnvalidated(pattern, relativePath) {
			return true
		}
	}
	return false
}

// StartupForRuleset separates startup recommendations from loading permission.
func StartupForRuleset(catalog []*Meta, ruleset permission.Ruleset) []*Meta {
	return filterVisibleSkills(catalog, func(meta *Meta) bool { return ModelCallableUnderRuleset(meta, ruleset) && len(meta.Paths) == 0 })
}

// PathActivationDescriptor returns the descriptor frozen with this catalog
// entry. Synthetic metadata may be evaluated directly before installation.
func PathActivationDescriptor(meta *Meta) PathDescriptor {
	if meta != nil && meta.pathDescriptor != nil {
		return *meta.pathDescriptor
	}
	return BuildPathDescriptor(meta)
}

// SnapshotCatalog transfers ownership into the immutable runtime catalog.
// Neither caller-owned slices nor Meta pointers can change an executing read's
// metadata revision after its candidate snapshot has been captured.
func SnapshotCatalog(catalog []*Meta) []*Meta {
	out := make([]*Meta, 0, len(catalog))
	for _, meta := range catalog {
		if meta == nil {
			continue
		}
		copyMeta := *meta
		if location, err := filepath.Abs(meta.Location); err == nil {
			if real, err := filepath.EvalSymlinks(location); err == nil {
				location = real
			}
			copyMeta.Location = filepath.Clean(location)
			copyMeta.RootDir = filepath.Dir(copyMeta.Location)
		}
		copyMeta.AllowedTools = slices.Clone(meta.AllowedTools)
		copyMeta.Paths = slices.Clone(meta.Paths)
		copyMeta.Resources = slices.Clone(meta.Resources)
		descriptor := BuildPathDescriptor(&copyMeta)
		copyMeta.pathDescriptor = &descriptor
		out = append(out, &copyMeta)
	}
	return out
}
