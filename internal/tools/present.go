package tools

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/keakon/chord/internal/filedelivery"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/permission"
)

type PresentArtifact struct {
	RelPath   string `json:"rel_path"`
	SHA256    string `json:"sha256"`
	SizeBytes *int64 `json:"size_bytes"`
}
type PresentFile struct {
	Path        *string          `json:"path,omitempty"`
	Artifact    *PresentArtifact `json:"artifact,omitempty"`
	Description string           `json:"description,omitempty"`
}
type PresentArgs struct {
	Files []PresentFile `json:"files"`
}

func ParsePresentArgs(raw json.RawMessage) (PresentArgs, error) {
	var args PresentArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return args, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return args, errors.New("expected one argument object")
	}
	if len(args.Files) < 1 || len(args.Files) > 16 {
		return args, errors.New("files must contain 1–16 items")
	}
	for i, f := range args.Files {
		fail := func(reason string) (PresentArgs, error) { return args, fmt.Errorf("files[%d]: %s", i, reason) }
		if (f.Path == nil) == (f.Artifact == nil) {
			return fail("exactly one of path or artifact required")
		}
		if len(f.Description) > 1024 || !utf8.ValidString(f.Description) {
			return fail("description must be valid UTF-8, at most 1024 bytes")
		}
		if f.Path != nil && (*f.Path == "" || len(*f.Path) > 8192 || strings.ContainsRune(*f.Path, 0)) {
			return fail("invalid path")
		}
		if a := f.Artifact; a != nil {
			if _, err := filedelivery.ArtifactLocator(a.RelPath); err != nil {
				return fail(err.Error())
			}
			digest, err := hex.DecodeString(a.SHA256)
			if err != nil || len(digest) != 32 || strings.ToLower(a.SHA256) != a.SHA256 || a.SizeBytes == nil || *a.SizeBytes < 0 {
				return fail("artifact requires lowercase SHA-256 and nonnegative size_bytes")
			}
		}
	}
	return args, nil
}
func PresentLocators(args PresentArgs, base string) ([]message.FileLocator, error) {
	out := make([]message.FileLocator, len(args.Files))
	for i, f := range args.Files {
		var err error
		if f.Path != nil {
			out[i], err = filedelivery.WorkspaceLocator(base, *f.Path)
		} else {
			out[i], err = filedelivery.ArtifactLocator(f.Artifact.RelPath)
		}
		if err != nil {
			return nil, fmt.Errorf("files[%d]: %w", i, err)
		}
	}
	return out, nil
}

// IsChordInternalDeliveryTarget reports whether a resolved present locator
// lands in a .chord directory of the checkout it belongs to. Such files are
// private working state (notes, plans, memory, skills, config) rather than
// user-facing final files, so the permission layer denies them by default; an
// explicit present-specific rule still wins. Artifact locators are
// session-scoped and never count as internal.
//
// A path inside a known checkout is judged by its spelling under that
// checkout's root, not under the delivering agent's: a linked worktree can
// itself live under a .chord directory (worktree.root: .chord/worktrees), and
// the main agent delivering the worktree's ordinary file must not see the
// .chord segment of the worktree's own location. A target no checkout root
// contains is judged by the locator's own root and path, so another
// workspace's working state stays protected even when that workspace happens
// to sit beside this one.
func IsChordInternalDeliveryTarget(loc message.FileLocator, scope permission.PathScope) bool {
	if loc.Workspace == nil || loc.Workspace.Root == "" {
		return false
	}
	if rel, ok := scope.RelativizePath(loc.Workspace.Path); ok {
		return hasChordInternalSegment(rel)
	}
	root := filepath.Clean(loc.Workspace.Root)
	if rel, inside := pathutil.RelToBase(loc.Workspace.Path, root); inside {
		return hasChordInternalSegment(rel)
	}
	// A locator stores a symlink-resolved Path next to the working directory as
	// it was given, so a root reached through a symlink compares as if the file
	// were outside it. Resolving the root before falling back keeps a worktree
	// checkout under .chord from condemning the ordinary files it delivers.
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		if rel, inside := pathutil.RelToBase(loc.Workspace.Path, resolved); inside {
			return hasChordInternalSegment(rel)
		}
	}
	return hasChordInternalSegment(loc.Workspace.Path)
}

func hasChordInternalSegment(path string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(path), "/"), ".chord")
}

type PresentTool struct{ BaseDir string }

func (t PresentTool) Name() string                 { return NamePresent }
func (t PresentTool) IsReadOnly() bool             { return false }
func (t PresentTool) WithBaseDir(base string) Tool { t.BaseDir = base; return t }
func (t PresentTool) Description() string {
	return "Declare final user-facing files through durable delivery cards. Use it only when the user needs a file itself, such as a generated report, document, web page, image, PDF, binary, a file outside the repository, or a specific file the user asked to receive. Do not declare repository source, tests, changelog, or configuration files merely because you edited them. Does not create files, open applications, or prove tests passed. Files under .chord/ are private working state, not deliverables. Direct tool calls only."
}
func (t PresentTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"files"}, "properties": map[string]any{"files": map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "description": "Final files to deliver; all entries must validate.", "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"path":        map[string]any{"type": "string", "description": "Workspace file path for a final user-facing file, relative to this agent's working directory or absolute. Mutually exclusive with artifact. Repository source, tests, changelog, and configuration files are not delivery targets unless the user explicitly asked to receive that file. Files under .chord/ are private working state and are rejected as delivery targets."},
		"description": map[string]any{"type": "string", "maxLength": 1024, "description": "Optional short explanation of the file's purpose."},
		"artifact":    map[string]any{"type": "object", "additionalProperties": false, "description": "Existing session artifact with an exact declared digest and size.", "required": []string{"rel_path", "sha256", "size_bytes"}, "properties": map[string]any{"rel_path": map[string]any{"type": "string", "description": "Session-relative path under artifacts/ or images/."}, "sha256": map[string]any{"type": "string", "description": "64 lowercase hexadecimal SHA-256 characters."}, "size_bytes": map[string]any{"type": "integer", "minimum": 0, "description": "Exact file size in bytes, including zero."}}},
	}}}}}
}
func (t PresentTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	sink, _ := ctx.Value(deliveryContextKey{}).(*DeliveryCollector)
	sessionDir := SessionDirFromContext(ctx)
	if sink == nil || sessionDir == "" {
		return "", errors.New("present requires a direct call in a persistent session")
	}
	args, err := ParsePresentArgs(raw)
	if err != nil {
		return "", err
	}
	locators, err := PresentLocators(args, t.BaseDir)
	if err != nil {
		return "", err
	}
	if approved, ok := ctx.Value(approvedDeliveryTargetsKey{}).([]message.FileLocator); ok && !reflect.DeepEqual(approved, locators) {
		return "", errors.New("file target changed after permission approval")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var files []message.DeliveredFile
	seen := make(map[string]int)
	budget := filedelivery.BatchReadLimit
	for i, loc := range locators {
		f, err := filedelivery.Inspect(ctx, loc, sessionDir, &budget)
		if err != nil {
			return "", fmt.Errorf("files[%d]: %w", i, err)
		}
		if a := args.Files[i].Artifact; a != nil && (a.SHA256 != f.SHA256 || *a.SizeBytes != f.SizeBytes) {
			return "", fmt.Errorf("files[%d]: artifact digest or size mismatch", i)
		}
		f.Source = message.DeliverySource{SessionID: filepath.Base(sessionDir), TaskID: TaskIDFromContext(ctx), ToolCallID: ToolCallIDFromContext(ctx), TurnID: strconv.FormatUint(TurnIDFromContext(ctx), 10), ItemIndex: i}
		f.ID = message.DeliveryID(f.Source)
		f.Description = args.Files[i].Description
		keyBytes, _ := json.Marshal(loc)
		key := string(keyBytes)
		if prior, ok := seen[key]; ok {
			files[prior].Description = f.Description
			continue
		}
		seen[key] = len(files)
		files = append(files, f)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	sink.Add(files)
	return fmt.Sprintf("Declared %d file(s) for delivery. This declaration does not establish verification or test success.", len(files)), nil
}
