package main

import (
	"context"
	"fmt"
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
		Use:   "project [<session-id>]",
		Short: "Project a persisted session into per-turn JSONL facts",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.MaximumNArgs(1)(cmd, args); err != nil {
				return err
			}
			if len(args) == 0 && strings.TrimSpace(sessionDirOverride) == "" {
				return fmt.Errorf("session id or --session-dir is required")
			}
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			sid := ""
			if len(args) > 0 {
				sid = strings.TrimSpace(args[0])
			}
			sessionDir := strings.TrimSpace(sessionDirOverride)
			if sessionDir == "" {
				if sid == "" {
					return fmt.Errorf("session id is required")
				}
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
			return writeProjectionOutput(sessionDir, outPath, data)
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "", "write JSONL projection to this file instead of stdout")
	cmd.Flags().StringVar(&sessionDirOverride, "session-dir", "", "project this session directory directly instead of resolving <session-id>")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", chordsession.DefaultProjectionLimits().MaxTotalBytes, "maximum JSONL size in bytes (0 keeps the default)")
	return cmd
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
