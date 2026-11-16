package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Declaration is one file consumed by a skill load. Path preserves its
// discovery identity; Target binds the symlink resolution used for this read.
type Declaration struct {
	Path   string
	Target string
	Data   []byte
}

// Snapshot owns the bytes used for both authorization and parsing. Missing
// sidecars remain in Files with nil Data so their later addition is detectable.
type Snapshot struct {
	Files []Declaration
}

// ReadSnapshot uses the caller's reader for every declaration so one read
// budget can cover the main file, sidecars and other project inputs.
func ReadSnapshot(path string, readFile func(string) ([]byte, error)) (*Snapshot, error) {
	main, err := readDeclaration(path, false, readFile)
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{Files: []Declaration{main}}
	for _, name := range []string{"chord.yaml", "agent.yaml"} {
		file, err := readDeclaration(filepath.Join(filepath.Dir(main.Target), name), true, readFile)
		if err != nil {
			return nil, err
		}
		snapshot.Files = append(snapshot.Files, file)
	}
	return snapshot, nil
}

func readDeclaration(path string, optional bool, readFile func(string) ([]byte, error)) (Declaration, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return Declaration{}, fmt.Errorf("resolve declaration path: %w", err)
	}
	file := Declaration{Path: path}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			// A dangling symlink is a broken declaration, not an absent one.
			if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
				return file, nil
			}
		}
		return file, fmt.Errorf("resolve declaration %s: %w", path, err)
	}
	file.Target = target
	file.Data, err = readFile(target)
	if err != nil {
		return file, fmt.Errorf("read declaration %s: %w", path, err)
	}
	return file, nil
}

// Load parses only captured bytes; it performs no filesystem access.
func (s *Snapshot) Load() (*Skill, error) {
	main := s.Files[0]
	fm, content, err := parseFrontmatter(string(main.Data))
	if err != nil {
		return nil, fmt.Errorf("parse skill %s: %w", main.Path, err)
	}
	if fm.Name == "" || fm.Description == "" {
		return nil, fmt.Errorf("skill %s: name and description are required", main.Path)
	}
	meta := buildMeta(main.Path, main.Target, fm)
	for _, file := range s.Files[1:] {
		if file.Data == nil {
			continue
		}
		var sidecar frontmatter
		if yaml.Unmarshal(file.Data, &sidecar) != nil {
			continue
		}
		applySidecarMeta(filepath.Base(file.Path), sidecar, meta)
		break
	}
	descriptor := BuildPathDescriptor(meta)
	meta.pathDescriptor = &descriptor
	return &Skill{Meta: *meta, Content: content}, nil
}
