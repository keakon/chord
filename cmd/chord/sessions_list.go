package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/recovery"
)

type sessionListEntry struct {
	ID         string    `json:"id"`
	LastActive time.Time `json:"last_active"`
	Locked     bool      `json:"locked"`
}

type sessionListReport struct {
	SchemaVersion int                `json:"schema_version"`
	Workspace     string             `json:"workspace"`
	Sessions      []sessionListEntry `json:"sessions"`
}

func newSessionsListCmd() *cobra.Command {
	var asJSON bool
	var limit int
	var directory string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent sessions for the current repository or directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 1 || limit > 1000 {
				return fmt.Errorf("limit must be between 1 and 1000")
			}
			cwd, err := filepath.Abs(directory)
			if err != nil {
				return fmt.Errorf("resolve directory: %w", err)
			}
			info, err := os.Stat(cwd)
			if err != nil {
				return fmt.Errorf("inspect directory: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("not a directory: %s", cwd)
			}
			root := resolveContentRoot(cmd.Context(), cwd)
			if root == "" {
				root = cwd
			}
			locator, err := startupPathLocator()
			if err != nil {
				return fmt.Errorf("resolve session paths: %w", err)
			}
			project, err := locator.LocateProject(root)
			if err != nil {
				return fmt.Errorf("locate session project: %w", err)
			}
			sessions, err := recovery.ListSessionActivity(project.ProjectSessionsDir, "")
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			report := sessionListReport{SchemaVersion: 1, Workspace: project.CanonicalRoot, Sessions: make([]sessionListEntry, 0, min(limit, len(sessions)))}
			for _, s := range sessions[:min(limit, len(sessions))] {
				report.Sessions = append(report.Sessions, sessionListEntry{ID: s.ID, LastActive: s.LastModTime, Locked: s.Locked})
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			for _, s := range report.Sessions {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tlocked=%t\n", s.ID, s.LastActive.Format(time.RFC3339), s.Locked); err != nil {
					return fmt.Errorf("write session list: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&directory, "directory", "d", ".", "Project directory whose sessions to list")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Write a bounded JSON report without message content")
	cmd.Flags().IntVar(&limit, "limit", 10, "Maximum number of recent sessions (1-1000)")
	return cmd
}
