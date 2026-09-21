package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	chordsession "github.com/keakon/chord/internal/session"
)

// newSessionsCmd groups read-only session inspection commands. It writes
// nothing into the session directory: projections load main.jsonl through the
// read-only transcript loader and emit JSONL facts to stdout (or --out), so a
// live session can be projected while another process holds its lock.
func newSessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "sessions",
		Short:         "Inspect persisted sessions (read-only)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newSessionsProjectCmd())
	return cmd
}

func newSessionsProjectCmd() *cobra.Command {
	var outPath string
	var sessionDirOverride string
	var maxBytes int
	cmd := &cobra.Command{
		Use:           "project <session-id>",
		Short:         "Project a persisted session into per-turn JSONL facts",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			sid := strings.TrimSpace(args[0])
			if sid == "" {
				return fmt.Errorf("session id is required")
			}
			sessionDir := strings.TrimSpace(sessionDirOverride)
			if sessionDir == "" {
				loc, err := resolveSessionWorktree(ctx, sid)
				if err != nil {
					return err
				}
				sessionDir, err = sessionDirForLocation(loc, sid)
				if err != nil {
					return err
				}
			} else {
				sid = filepath.Base(sessionDir)
			}
			msgs, err := loadSessionMessagesReadOnly(sessionDir)
			if err != nil {
				return err
			}
			exported, err := chordsession.Export(msgs, nil, map[string]string{
				chordsession.MetadataKeySessionID: sid,
			})
			if err != nil {
				return err
			}
			limits := chordsession.DefaultProjectionLimits()
			if maxBytes > 0 {
				limits.MaxTotalBytes = maxBytes
			}
			data, err := chordsession.ProjectJSONLWithLimits(exported, limits)
			if err != nil {
				return err
			}
			if strings.TrimSpace(outPath) == "" {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			if err := rejectProjectionOutput(sessionDir, outPath); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return fmt.Errorf("create output directory: %w", err)
			}
			if err := os.WriteFile(outPath, data, 0o644); err != nil {
				return fmt.Errorf("write projection file: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "", "write JSONL projection to this file instead of stdout")
	cmd.Flags().StringVar(&sessionDirOverride, "session-dir", "", "project this session directory directly instead of resolving <session-id>")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", chordsession.DefaultProjectionLimits().MaxTotalBytes, "maximum JSONL size in bytes (0 keeps the default)")
	return cmd
}

// rejectProjectionOutput refuses an --out path that would replace the session
// it is reading. A path inside the session directory, or a hard link to a file
// already there, is rejected. The check runs before any write.
func rejectProjectionOutput(sessionDir, outPath string) error {
	sessionAbs, err := filepath.Abs(sessionDir)
	if err != nil {
		return fmt.Errorf("resolve session directory: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(sessionAbs); err == nil {
		sessionAbs = resolved
	}
	// MkdirAll and WriteFile do not clean ".." the way filepath.Abs does, so a
	// symlink followed by ".." lands inside the session even though the
	// cleaned path looks like a sibling. The check has to follow that same
	// spelling, which means building the absolute path without Clean.
	outAbs, err := absWithoutClean(outPath)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	outAbs, err = resolveExistingAncestor(outAbs)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(outAbs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		// resolveExistingAncestor could not follow this link, so a write would
		// create its target: refuse instead of letting WriteFile pick a spot.
		return fmt.Errorf("refusing to write the projection through the symlink %s", outAbs)
	}
	rel, err := filepath.Rel(sessionAbs, outAbs)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to write the projection inside the session directory %s", sessionAbs)
	}
	outInfo, err := os.Stat(outAbs)
	if err != nil {
		return nil
	}
	sessionEntries, err := os.ReadDir(sessionAbs)
	if err != nil {
		return fmt.Errorf("read session directory: %w", err)
	}
	for _, entry := range sessionEntries {
		info, err := os.Stat(filepath.Join(sessionAbs, entry.Name()))
		if err != nil || !os.SameFile(outInfo, info) {
			continue
		}
		return fmt.Errorf("refusing to overwrite session file %s", entry.Name())
	}
	return nil
}

// absWithoutClean makes path absolute without filepath.Clean. The later write
// uses the raw path, and Clean would erase a ".." that the write still walks.
func absWithoutClean(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return wd + string(filepath.Separator) + path, nil
}

// resolveExistingAncestor follows symlinks in the deepest existing ancestor of
// path and re-appends the components that do not exist yet, so a caller can see
// where a later MkdirAll/WriteFile would land. ".." is applied only after a
// symlink is followed: cleaning it first would hide a write that walks out of
// the link and back into the session.
func resolveExistingAncestor(path string) (string, error) {
	return resolvePathComponents(path, 0)
}

func resolvePathComponents(path string, depth int) (string, error) {
	const maxSymlinkDepth = 64
	if depth > maxSymlinkDepth {
		return "", fmt.Errorf("resolve %s: too many symlinks", path)
	}
	vol := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, vol)
	parts := strings.Split(rest, string(filepath.Separator))
	resolved := vol
	if strings.HasPrefix(rest, string(filepath.Separator)) {
		resolved = vol + string(filepath.Separator)
	}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			resolved = parentPath(resolved)
			continue
		}
		next := joinPathComponent(resolved, part)
		info, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) {
				resolved = next
				continue
			}
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		if !filepath.IsAbs(target) {
			target = joinPathComponent(resolved, target)
		}
		followed, err := resolvePathComponents(target, depth+1)
		if err != nil {
			return "", err
		}
		resolved = followed
	}
	return resolved, nil
}

func parentPath(path string) string {
	vol := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, vol)
	rest = strings.TrimRight(rest, string(filepath.Separator))
	i := strings.LastIndex(rest, string(filepath.Separator))
	if i < 0 {
		return vol
	}
	if i == 0 {
		return vol + string(filepath.Separator)
	}
	return vol + rest[:i]
}

func joinPathComponent(parent, child string) string {
	if parent == "" {
		return child
	}
	if strings.HasSuffix(parent, string(filepath.Separator)) {
		return parent + child
	}
	return parent + string(filepath.Separator) + child
}

// loadSessionMessagesReadOnly loads main.jsonl through the shared read-only
// transcript loader: no session lock, no attachment bytes, truncated-tail
// tolerance, and bounded retries when a concurrent rewrite (compaction,
// RewriteLog's in-place O_TRUNC) leaves a transient mid-file parse error. The
// loader is rooted at the session's parent directory because LoadDir only
// accepts direct children of its root; --session-dir outside any sessions root
// therefore works here while Load (session-id form) would refuse it.
func loadSessionMessagesReadOnly(sessionDir string) ([]message.Message, error) {
	loader := recovery.NewReadOnlyTranscriptLoader(filepath.Dir(sessionDir))
	msgs, err := loader.LoadDir(sessionDir)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("session has no messages in %s", sessionDir)
	}
	return msgs, nil
}
