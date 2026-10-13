package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/worktree"
)

const (
	maxDirectoryInstructionBytes          = 64 << 10
	maxDirectoryInstructionSnapshotBytes  = 256 << 10
	maxDirectoryInstructionObservedLevels = 16384
	maxDirectoryInstructionDetailBytes    = 1024
)

// prepareDirectoryInstructionBinding runs only during background preparation;
// Git is never invoked by discovery or the hot admission path.
func prepareDirectoryInstructionBinding(ctx context.Context, binding directoryInstructionBinding) (directoryInstructionBinding, error) {
	var err error
	binding.ContentRoot, err = canonicalInstructionDirectory(binding.ContentRoot)
	if err != nil {
		return binding, err
	}
	binding.WorkDir, err = canonicalInstructionDirectory(binding.WorkDir)
	if err != nil {
		return binding, err
	}
	if binding.CheckoutRoot == "" {
		binding.CheckoutRoot = pathutil.CheckoutRoot(binding.WorkDir, binding.ContentRoot)
	}
	binding.CheckoutRoot, err = canonicalInstructionDirectory(binding.CheckoutRoot)
	if err != nil {
		return binding, err
	}
	binding.FallbackAllowed = binding.ContentRoot == binding.CheckoutRoot
	if !binding.FallbackAllowed {
		checkout, e1 := worktree.GitCommonDir(ctx, binding.CheckoutRoot)
		content, e2 := worktree.GitCommonDir(ctx, binding.ContentRoot)
		binding.FallbackAllowed = e1 == nil && e2 == nil && checkout != "" && checkout == content
	}
	return binding, nil
}

// Only missing suffixes are reconstructed. ELOOP and permission failures must
// remain errors, rather than being mistaken for nonexistent directories.
func canonicalInstructionDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("directory instruction path must be absolute: %s", path)
	}
	path = filepath.Clean(path)
	real, err := filepath.EvalSymlinks(path)
	if err == nil {
		return real, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	real, err = canonicalInstructionDirectory(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, filepath.Base(path)), nil
}

func instructionTargetDirectory(target tools.InstructionTarget) (string, error) {
	switch target.Mode {
	case tools.InstructionTargetEntry:
		return canonicalInstructionDirectory(filepath.Dir(target.Path))
	case tools.InstructionTargetDirectory:
		return canonicalInstructionDirectory(target.Path)
	case tools.InstructionTargetContent:
		real, err := filepath.EvalSymlinks(target.Path)
		if err != nil {
			return "", err
		}
		return filepath.Dir(real), nil
	default:
		return "", fmt.Errorf("invalid instruction target mode")
	}
}

func instructionScopeChain(root, dir string) ([]string, bool) {
	rel, ok := relativePathWithin(root, dir)
	if !ok {
		return nil, false
	}
	scopes := []string{"."}
	if rel != "." {
		parts := strings.Split(filepath.ToSlash(rel), "/")
		for i := range parts {
			scopes = append(scopes, strings.Join(parts[:i+1], "/"))
		}
	}
	return scopes, true
}

func directoryBlocked(path, reason string, err error) directoryInstructionObservation {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	if len(detail) > maxDirectoryInstructionDetailBytes {
		detail = detail[:maxDirectoryInstructionDetailBytes]
		for !utf8.ValidString(detail) {
			detail = detail[:len(detail)-1]
		}
	}
	return directoryInstructionObservation{Status: "blocked", SourcePath: path, Reason: reason, Detail: detail}
}

type directoryInstructionPermissionContextKey struct{}
type directoryInstructionPermissionOverride struct {
	check    func(context.Context, string) error
	identity string
}

func (r *directoryInstructionRuntime) checkInstructionPermission(ctx context.Context, path string) error {
	if override, ok := ctx.Value(directoryInstructionPermissionContextKey{}).(directoryInstructionPermissionOverride); ok {
		return override.check(ctx, path)
	}
	if r.permissionCheck == nil {
		return nil
	}
	return r.permissionCheck(ctx, path)
}

func (r *directoryInstructionRuntime) readInstructionFile(ctx context.Context, path string) directoryInstructionObservation {
	if err := r.checkInstructionPermission(ctx, path); err != nil {
		return directoryBlocked(path, message.DirectoryInstructionReasonPermission, err)
	}
	if err := ctx.Err(); err != nil {
		return directoryBlocked(path, "cancelled", err)
	}
	real, err := filepath.EvalSymlinks(path)
	if os.IsNotExist(err) {
		return directoryInstructionObservation{Status: "absent", SourcePath: path}
	}
	if err != nil {
		return directoryBlocked(path, message.DirectoryInstructionReasonIO, err)
	}
	if real != path {
		if err := r.checkInstructionPermission(ctx, real); err != nil {
			return directoryBlocked(real, message.DirectoryInstructionReasonPermission, err)
		}
	}
	allowed := false
	for _, root := range []string{r.binding.CheckoutRoot, r.binding.ContentRoot} {
		if root == r.binding.ContentRoot && !r.binding.FallbackAllowed && root != r.binding.CheckoutRoot {
			continue
		}
		if _, ok := relativePathWithin(root, real); ok {
			if real != path {
				scopes, _ := instructionScopeChain(root, filepath.Dir(real))
				for _, scope := range scopes[1:] {
					_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(scope), ".git"))
					if err == nil {
						return directoryBlocked(real, message.DirectoryInstructionReasonPermission, fmt.Errorf("rule source crosses a nested checkout: %s", real))
					}
					if !os.IsNotExist(err) {
						return directoryBlocked(real, message.DirectoryInstructionReasonIO, err)
					}
				}
			}
			allowed = true
			break
		}
	}
	if !allowed {
		return directoryBlocked(real, message.DirectoryInstructionReasonPermission, fmt.Errorf("rule source is outside the authorized project: %s", real))
	}
	f, err := pathutil.OpenCanonicalRegular(real)
	if err != nil {
		return directoryBlocked(real, message.DirectoryInstructionReasonIO, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return directoryBlocked(real, message.DirectoryInstructionReasonIO, err)
	}
	if opened.Size() > maxDirectoryInstructionBytes {
		return directoryBlocked(real, message.DirectoryInstructionReasonSize, fmt.Errorf("rule file exceeds 64 KiB: %s", real))
	}
	data, err := io.ReadAll(io.LimitReader(f, maxDirectoryInstructionBytes+1))
	if err != nil {
		return directoryBlocked(real, message.DirectoryInstructionReasonIO, err)
	}
	if err := ctx.Err(); err != nil {
		return directoryBlocked(real, "cancelled", err)
	}
	if len(data) > maxDirectoryInstructionBytes {
		return directoryBlocked(real, message.DirectoryInstructionReasonSize, fmt.Errorf("rule file exceeds 64 KiB: %s", real))
	}
	if !utf8.Valid(data) {
		return directoryBlocked(real, message.DirectoryInstructionReasonEncoding, fmt.Errorf("rule file is not valid UTF-8: %s", real))
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return directoryInstructionObservation{Status: "absent", SourcePath: real}
	}
	hash := sha256.Sum256([]byte(body))
	permissionIdentity := r.permissionIdentity
	if override, ok := ctx.Value(directoryInstructionPermissionContextKey{}).(directoryInstructionPermissionOverride); ok {
		permissionIdentity = override.identity
	}
	return directoryInstructionObservation{Status: "present", SourcePath: real, Body: body, BodyHash: hex.EncodeToString(hash[:]), PermissionIdentity: permissionIdentity}
}

func (r *directoryInstructionRuntime) readInstructionLevel(ctx context.Context, scope string) directoryInstructionObservation {
	path := filepath.Join(r.binding.CheckoutRoot, filepath.FromSlash(scope), "AGENTS.md")
	first := r.readInstructionFile(ctx, path)
	if first.Status == "present" || first.Status == "blocked" && first.Reason != message.DirectoryInstructionReasonIO || !r.binding.FallbackAllowed || r.binding.ContentRoot == r.binding.CheckoutRoot {
		return first
	}
	second := r.readInstructionFile(ctx, filepath.Join(r.binding.ContentRoot, filepath.FromSlash(scope), "AGENTS.md"))
	if second.Status == "present" {
		if first.Status == "blocked" {
			second.Detail = first.Detail
		}
		return second
	}
	if first.Status == "blocked" && second.Status == "absent" {
		return first
	}
	return second
}

type directoryInstructionDiscoveryRequestKey struct{}

func withDirectoryInstructionDiscoveryRequest(ctx context.Context, seq uint64) context.Context {
	return context.WithValue(ctx, directoryInstructionDiscoveryRequestKey{}, seq)
}

// Discover serializes filesystem discovery per binding; cancellation publishes
// only completed observations and never lends one caller's context to another.
func (r *directoryInstructionRuntime) Discover(ctx context.Context, binding directoryInstructionBinding, targets []tools.InstructionTarget) (directoryInstructionDiscovery, error) {
	d := directoryInstructionDiscovery{Binding: binding, Targets: append([]tools.InstructionTarget(nil), targets...), Resolutions: make(map[tools.InstructionTarget]directoryInstructionTargetResolution, len(targets)), Observations: map[string]directoryInstructionObservation{}, Complete: true}
	if binding != r.binding {
		return d, fmt.Errorf("directory instruction binding changed")
	}
	select {
	case <-ctx.Done():
		return d, ctx.Err()
	case <-r.ctx.Done():
		return d, r.ctx.Err()
	case <-r.discoveryToken:
	}
	defer func() { r.discoveryToken <- struct{}{} }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	requestSeq, _ := ctx.Value(directoryInstructionDiscoveryRequestKey{}).(uint64)
	permissionIdentity := r.permissionIdentity
	if override, ok := ctx.Value(directoryInstructionPermissionContextKey{}).(directoryInstructionPermissionOverride); ok {
		permissionIdentity = override.identity
	}
	if requestSeq != r.failureRequest || permissionIdentity != r.failurePermissionIdentity {
		r.failurePermissionIdentity = permissionIdentity
		r.failureRequest = requestSeq
		r.failures = make(map[string]directoryInstructionObservation)
	}
	if r.failures == nil {
		r.failures = make(map[string]directoryInstructionObservation)
	}
	changed := false
	defer func() {
		if changed {
			observations := make(map[string]directoryInstructionObservation, len(r.observed))
			maps.Copy(observations, r.observed)
			revision := uint64(1)
			if prior := r.frozen.Load(); prior != nil {
				revision = prior.Revision + 1
			}
			targets := make(map[tools.InstructionTarget]string, len(r.resolvedTargets))
			maps.Copy(targets, r.resolvedTargets)
			r.frozen.Store(&directoryInstructionFrozenView{Observations: observations, Targets: targets, Revision: revision})
		}
	}()
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return d, err
		}
		dir, err := instructionTargetDirectory(target)
		if err != nil {
			d.Complete = false
			failure := directoryBlocked(target.Path, message.DirectoryInstructionReasonIO, err)
			d.Resolutions[target] = directoryInstructionTargetResolution{Failure: &failure}
			d.Observations[directoryInstructionUnresolvedScope(binding.CheckoutRoot, target)] = failure
			continue
		}
		d.Resolutions[target] = directoryInstructionTargetResolution{}
		scopes, inside := instructionScopeChain(binding.CheckoutRoot, dir)
		if !inside {
			if old, known := r.resolvedTargets[target]; (known && old != "") || (!known && len(r.resolvedTargets) < maxDirectoryInstructionObservedLevels) {
				r.resolvedTargets[target] = ""
				changed = true
			}
			continue
		}
		nested := false
		for _, scope := range scopes[1:] {
			boundary, known := r.boundaries[scope]
			if !known {
				if len(r.boundaries) >= maxDirectoryInstructionObservedLevels {
					d.Complete = false
					failure := directoryBlocked(target.Path, message.DirectoryInstructionReasonCapacity, fmt.Errorf("directory observation limit reached"))
					d.Observations[scope] = failure
					d.Resolutions[target] = directoryInstructionTargetResolution{Failure: &failure}
					nested = true
					break
				}
				_, err := os.Lstat(filepath.Join(binding.CheckoutRoot, filepath.FromSlash(scope), ".git"))
				if err != nil && !os.IsNotExist(err) {
					d.Complete = false
					failure := directoryBlocked(target.Path, message.DirectoryInstructionReasonIO, err)
					d.Observations[scope] = failure
					d.Resolutions[target] = directoryInstructionTargetResolution{Failure: &failure}
					nested = true
					break
				}
				boundary = err == nil
				r.boundaries[scope] = boundary
			}
			if boundary {
				nested = true
				break
			}
		}
		if nested {
			if old, known := r.resolvedTargets[target]; d.Complete && ((known && old != "") || (!known && len(r.resolvedTargets) < maxDirectoryInstructionObservedLevels)) {
				r.resolvedTargets[target] = ""
				changed = true
			}
			continue
		}
		d.Resolutions[target] = directoryInstructionTargetResolution{Directory: dir}
		if old, ok := r.resolvedTargets[target]; (ok && old != dir) || (!ok && len(r.resolvedTargets) < maxDirectoryInstructionObservedLevels) {
			r.resolvedTargets[target] = dir
			changed = true
		}
		for _, scope := range scopes {
			if _, done := d.Observations[scope]; done {
				continue
			}
			if requestSeq != 0 {
				if failed, ok := r.failures[scope]; ok {
					d.Observations[scope] = failed
					d.Complete = false
					continue
				}
			}
			observation, cached := r.observed[scope]
			if cached {
				if err := r.checkInstructionPermission(ctx, filepath.Join(binding.CheckoutRoot, filepath.FromSlash(scope), "AGENTS.md")); err != nil {
					observation = directoryBlocked(observation.SourcePath, message.DirectoryInstructionReasonPermission, err)
				} else if observation.SourcePath != "" {
					if err := r.checkInstructionPermission(ctx, observation.SourcePath); err != nil {
						observation = directoryBlocked(observation.SourcePath, message.DirectoryInstructionReasonPermission, err)
					}
				}
			} else if len(r.observed) >= maxDirectoryInstructionObservedLevels {
				observation = directoryBlocked(target.Path, message.DirectoryInstructionReasonCapacity, fmt.Errorf("directory observation limit reached; start a new session with a narrower access scope"))
			} else {
				observation = r.readInstructionLevel(ctx, scope)
				if err := ctx.Err(); err != nil {
					return d, err
				}
				if r.observedBytes+len(observation.Body) > maxDirectoryInstructionSnapshotBytes {
					observation = directoryBlocked(observation.SourcePath, message.DirectoryInstructionReasonCapacity, fmt.Errorf("directory instruction snapshot exceeds 256 KiB; start a new session"))
				}
				if observation.Status != "blocked" || observation.Reason == message.DirectoryInstructionReasonCapacity || observation.Reason == message.DirectoryInstructionReasonSize || observation.Reason == message.DirectoryInstructionReasonEncoding {
					r.observed[scope] = observation
					r.observedBytes += len(observation.Body)
					changed = true
				}
			}
			if requestSeq != 0 && observation.Status == "blocked" && (observation.Reason == message.DirectoryInstructionReasonIO || observation.Reason == message.DirectoryInstructionReasonPermission) {
				r.failures[scope] = observation
			}
			d.Observations[scope] = observation
			if observation.Status == "blocked" {
				d.Complete = false
			}
		}
	}
	return d, nil
}

func directoryInstructionUnresolvedScope(root string, target tools.InstructionTarget) string {
	dir := target.Path
	if target.Mode != tools.InstructionTargetDirectory {
		dir = filepath.Dir(dir)
	}
	if rel, inside := relativePathWithin(root, dir); inside {
		return filepath.ToSlash(rel)
	}
	return "."
}
