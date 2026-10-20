package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

type InstructionTargetMode uint8

const (
	InstructionTargetContent InstructionTargetMode = iota
	InstructionTargetEntry
	InstructionTargetDirectory
)

type InstructionTarget struct {
	Path string
	Mode InstructionTargetMode
}

// decodeInstructionArgs shares each tool's typed decoder, including aliases.
func decodeInstructionArgs[T any](raw json.RawMessage) (T, error) {
	var a T
	err := json.Unmarshal(unwrapToolArgs(raw), &a)
	if err != nil {
		return a, fmt.Errorf("invalid arguments: %w", err)
	}
	return a, nil
}

// InstructionTargets returns only explicit paths; it never parses shell text
// or walks search results. Invalid arguments must not imply complete coverage.
func InstructionTargets(name string, args json.RawMessage, baseDir string) ([]InstructionTarget, error) {
	return instructionTargets(name, args, baseDir, true)
}

// InstructionTargetsWithoutIO conservatively decodes targets for stream admission.
// Grep paths use Content mode until discovery resolves their file type.
func InstructionTargetsWithoutIO(name string, args json.RawMessage, baseDir string) ([]InstructionTarget, error) {
	return instructionTargets(name, args, baseDir, false)
}

func instructionTargets(name string, args json.RawMessage, baseDir string, inspect bool) ([]InstructionTarget, error) {
	var paths []string
	mode := InstructionTargetEntry
	switch name {
	case NameRead:
		a, err := decodeInstructionArgs[readArgs](args)
		if err != nil {
			return nil, err
		}
		paths = []string{a.Path}
		mode = InstructionTargetContent
	case NameWrite:
		a, err := decodeInstructionArgs[writeArgs](args)
		if err != nil {
			return nil, err
		}
		paths = []string{a.Path}
	case NameEdit:
		a, err := decodeInstructionArgs[replaceEditArgs](args)
		if err != nil {
			return nil, err
		}
		paths = []string{a.Path}
	case NameDelete:
		a, err := DecodeDeleteRequestInDir(args, baseDir)
		if err != nil {
			return nil, err
		}
		paths = a.Paths
	case NameApplyPatch:
		targets, err := ApplyPatchTargets(args, baseDir)
		if err != nil {
			return nil, err
		}
		for _, t := range targets {
			if t.SourcePath != "" {
				paths = append(paths, t.SourcePath)
			}
			if t.TargetPath != "" {
				paths = append(paths, t.TargetPath)
			}
		}
	case NameGlob:
		a, err := decodeInstructionArgs[globArgs](args)
		if err != nil {
			return nil, err
		}
		if len(normalizeStringList(a.Patterns)) == 0 {
			return nil, fmt.Errorf("patterns is required")
		}
		path := strings.TrimSpace(a.Path)
		if path == "" {
			path = baseDir
		}
		paths = []string{path}
		mode = InstructionTargetDirectory
	case NameGrep:
		a, err := decodeInstructionArgs[grepArgs](args)
		if err != nil {
			return nil, err
		}
		if a.Pattern == "" {
			return nil, fmt.Errorf("pattern is required")
		}
		paths = grepSearchPaths(a, baseDir)
		mode = InstructionTargetContent
	case NameShell:
		a, err := decodeInstructionArgs[shellArgs](args)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Command) == "" {
			return nil, fmt.Errorf("command is required")
		}
		if !inspect {
			path := strings.TrimSpace(a.Workdir)
			if path == "" {
				path = baseDir
			}
			resolved, err := resolveToolPathAbsInDir(path, baseDir)
			if err != nil {
				return nil, err
			}
			return []InstructionTarget{{Path: resolved, Mode: InstructionTargetDirectory}}, nil
		}
		path, err := resolveCommandWorkdir(a.Workdir, baseDir)
		if err != nil {
			return nil, err
		}
		return []InstructionTarget{{Path: path, Mode: InstructionTargetDirectory}}, nil
	default:
		return nil, nil
	}
	out := make([]InstructionTarget, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("path is required")
		}
		resolved, err := resolveToolPathAbsInDir(path, baseDir)
		if err != nil {
			if name == NameGrep && inspect {
				continue
			}
			return nil, err
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		targetMode := mode
		if name == NameGrep && inspect {
			// Match grep's partial-success handling for paths it never reads.
			_, info, err := resolveExistingToolPathInDir(path, baseDir, PathTargetAny, "search")
			if err != nil {
				continue
			}
			if info.IsDir() {
				targetMode = InstructionTargetDirectory
			}
		}
		out = append(out, InstructionTarget{Path: resolved, Mode: targetMode})
	}
	return out, nil
}
