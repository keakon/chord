// Package trust persists the user's authorization decisions for
// project-level Chord configuration. A project gains configuration power
// (MCP servers, hooks, providers, agents, commands, skills) by placing
// files in its checkout; Chord applies that layer only after the user
// authorized exactly the declaration file set it discovered, keyed by the
// project root and bound to a fingerprint over that set.
//
// The gate is a consent boundary, not a sandbox: it decides whether
// repository-owned declarations influence Chord at all. It does not make
// executed scripts, dependencies, or ordinary source code trusted.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/jsonx"
)

// fingerprintDomain separates project-trust fingerprints from other
// sha256 uses; changing it invalidates every stored authorization.
const fingerprintDomain = "chord/project-trust/v1"

// FileHash is one declaration file of a source root: its path relative to
// the root (slash-separated) and the hex-encoded sha256 of its content.
type FileHash struct {
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Target string `json:"target,omitempty"`
}

// Input is the declaration file set contributed by one source root.
type Input struct {
	Root  string     `json:"root,omitempty"`
	Files []FileHash `json:"files,omitempty"`
}

// InputSet is the complete gated declaration input set of one launch, one
// entry per contributing root. Every mutation path keeps it canonical:
// roots sorted, files sorted by path, no duplicate roots or paths.
type InputSet []Input

// Normalize sorts roots and declarations, retaining empty controlled roots.
func (s InputSet) Normalize() InputSet {
	out := make(InputSet, 0, len(s))
	for _, input := range s {
		if strings.TrimSpace(input.Root) == "" {
			continue
		}
		files := make([]FileHash, len(input.Files))
		copy(files, input.Files)
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		out = append(out, Input{Root: input.Root, Files: files})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}

// Empty reports whether no root contributes any declaration file.
func (s InputSet) Empty() bool {
	for _, input := range s {
		if len(input.Files) > 0 {
			return false
		}
	}
	return true
}

// Fingerprint binds the set to one opaque digest. Adding, removing,
// changing, or moving a declaration file between roots changes it.
func (s InputSet) Fingerprint() string {
	payload, err := jsonx.Marshal(s.Normalize())
	if err != nil {
		// InputSet is a plain JSON struct; marshalling cannot fail.
		panic(fmt.Sprintf("trust: marshal fingerprint input: %v", err))
	}
	sum := sha256.New()
	sum.Write([]byte(fingerprintDomain))
	sum.Write([]byte{0})
	sum.Write(payload)
	return hex.EncodeToString(sum.Sum(nil))
}

// Covers reports whether every file of one root's file set is present in
// the authorized set with the same hash. Runtime re-checks use it: a
// worktree switch may introduce skill directories, and only those whose
// declarations were part of the authorized set may be scanned.
func (s InputSet) Covers(root string, files []FileHash) bool {
	if len(files) == 0 {
		return true
	}
	for _, input := range s {
		if input.Root != root {
			continue
		}
		authorized := make(map[string]FileHash, len(input.Files))
		for _, f := range input.Files {
			authorized[f.Path] = f
		}
		for _, f := range files {
			if authorized[f.Path] != f {
				return false
			}
		}
		return true
	}
	return false
}

// maxHashFileBytes caps one hashed declaration file. Declaration inputs are
// small text documents; the cap keeps a re-check from reading a file that
// was swapped for a huge one after discovery.
const maxHashFileBytes = 1 << 20

// HashFile hashes one file's content for a FileHash entry.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxHashFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxHashFileBytes {
		return "", fmt.Errorf("file %s exceeds the %d byte hashing limit", path, maxHashFileBytes)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Verify re-hashes every file of the set and reports whether the content
// on disk still matches the authorized fingerprint input. It is the
// pre-apply re-check that keeps what the runtime loads identical to what
// was disclosed and authorized.
func (s InputSet) Verify() error {
	for _, input := range s {
		for _, f := range input.Files {
			path := filepath.Join(input.Root, filepath.FromSlash(f.Path))
			if f.Target != "" {
				target, err := filepath.EvalSymlinks(path)
				if err != nil {
					return fmt.Errorf("resolve project file %s: %w", f.Path, err)
				}
				if target != f.Target {
					return fmt.Errorf("project file %s changed target since authorization", f.Path)
				}
				path = target
			}
			sum, err := HashFile(path)
			if err != nil {
				return fmt.Errorf("project file %s: %w", f.Path, err)
			}
			if sum != f.SHA256 {
				return fmt.Errorf("project file %s changed since authorization", f.Path)
			}
		}
	}
	return nil
}
