package memory

import (
	"fmt"
	"path/filepath"

	"github.com/keakon/chord/internal/config"
)

const (
	// ProjectLayoutDir is the project-local directory holding detailed memory
	// records. It lives next to MEMORY.md so both move with the project.
	ProjectLayoutDir = ".chord/memory/records"
	// ProjectIndexName is the user-visible memory entry point and bounded index
	// at the project root.
	ProjectIndexName = "MEMORY.md"
	// managedStartMarker opens the Chord-managed index section in MEMORY.md.
	managedStartMarker = "<!-- chord:managed:start -->"
	// managedEndMarker closes the Chord-managed index section in MEMORY.md.
	managedEndMarker = "<!-- chord:managed:end -->"
)

// Layout resolves project-local and per-project state paths for memory.
// It reuses config.ProjectLocator so the memory package never re-derives the
// project key.
type Layout struct {
	// ProjectRoot is the canonical project root for resolving relative record
	// paths against project files.
	ProjectRoot string
	// RecordsDir is the absolute path to .chord/memory/records/.
	RecordsDir string
	// IndexPath is the absolute path to the project MEMORY.md.
	IndexPath string

	// StateDir is the ordered per-project machine state directory
	// (<state>/memory/<project-key>/). It holds checkpoints and locks.
	StateDir string

	// CheckpointPath / LockPath are machine-owned files under StateDir. They
	// are not content authority: deleting them only resets
	// optimization/coordination state that can be rebuilt.
	CheckpointPath string
	LockPath       string

	// ProjectKey mirrors the project locator key for diagnostics.
	ProjectKey string
}

// resolveLayout builds a Layout for projectRoot without requiring the machine
// state directory to exist (commit paths create it lazily via the
// cross-process lock). When locator is nil the default path locator is used;
// callers that already resolved one (startup chain) must pass it so custom
// paths.state_dir / paths.sessions_dir settings are honored consistently.
func resolveLayout(projectRoot string, locator *config.PathLocator) (*Layout, error) {
	if locator == nil {
		var err error
		locator, err = config.DefaultPathLocator()
		if err != nil {
			return nil, fmt.Errorf("resolve memory layout: %w", err)
		}
	}
	pl, err := locator.LocateProject(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("locate project for memory: %w", err)
	}
	records := filepath.Join(pl.CanonicalRoot, filepath.FromSlash(ProjectLayoutDir))
	stateDir := filepath.Join(locator.StateDir, "memory", pl.ProjectKey)
	return &Layout{
		ProjectRoot:    pl.CanonicalRoot,
		RecordsDir:     records,
		IndexPath:      filepath.Join(pl.CanonicalRoot, ProjectIndexName),
		StateDir:       stateDir,
		CheckpointPath: filepath.Join(stateDir, "extraction-checkpoints.json"),
		LockPath:       filepath.Join(stateDir, "memory.lock"),
		ProjectKey:     pl.ProjectKey,
	}, nil
}
