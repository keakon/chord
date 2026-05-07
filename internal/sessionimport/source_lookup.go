package sessionimport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SourceFileLookup struct {
	Source string
	Root   string
	Path   string
}

func resolveImportInputPath(source string, inputPath string, sourceID string, root string) (SourceFileLookup, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	inputPath = strings.TrimSpace(inputPath)
	sourceID = strings.TrimSpace(sourceID)
	root = strings.TrimSpace(root)
	if inputPath != "" {
		return SourceFileLookup{Source: source, Root: root, Path: inputPath}, nil
	}
	if sourceID == "" {
		return SourceFileLookup{}, fmt.Errorf("import: either input path or --id is required")
	}
	switch source {
	case "codex":
		if root == "" {
			root = filepath.Join(userHomeDir(), ".codex", "sessions")
		}
		path, err := findCodexRolloutByID(root, sourceID)
		if err != nil {
			return SourceFileLookup{}, err
		}
		return SourceFileLookup{Source: source, Root: root, Path: path}, nil
	case "claude":
		if root == "" {
			root = filepath.Join(userHomeDir(), ".claude", "projects")
		}
		path, err := findClaudeTranscriptByID(root, sourceID)
		if err != nil {
			return SourceFileLookup{}, err
		}
		return SourceFileLookup{Source: source, Root: root, Path: path}, nil
	case "opencode":
		return SourceFileLookup{}, fmt.Errorf("opencode import by --id is not supported directly; run `opencode export %s > file.json` and import that file", sourceID)
	default:
		return SourceFileLookup{}, fmt.Errorf("import: unsupported source %q", source)
	}
}

func findCodexRolloutByID(root string, sourceID string) (string, error) {
	var matches []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if !strings.HasPrefix(filepath.Base(path), "rollout-") || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		ok, _ := codexFileContainsSessionID(path, sourceID)
		if ok {
			matches = append(matches, path)
		}
		return nil
	})
	return chooseSingleSourceMatch("codex", sourceID, matches)
}

func findClaudeTranscriptByID(root string, sourceID string) (string, error) {
	var matches []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasSuffix(base, ".jsonl") {
			return nil
		}
		if strings.TrimSuffix(base, ".jsonl") == sourceID {
			matches = append(matches, path)
		}
		return nil
	})
	return chooseSingleSourceMatch("claude", sourceID, matches)
}

func chooseSingleSourceMatch(source, sourceID string, matches []string) (string, error) {
	if len(matches) == 0 {
		return "", fmt.Errorf("%s import: no session found for id %q", source, sourceID)
	}
	sort.Strings(matches)
	if len(matches) > 1 {
		return "", fmt.Errorf("%s import: multiple files matched id %q; use an explicit file path", source, sourceID)
	}
	return matches[0], nil
}

func codexFileContainsSessionID(path string, sourceID string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if itemRaw, ok := obj["item"]; ok {
			var item map[string]json.RawMessage
			if err := json.Unmarshal(itemRaw, &item); err == nil {
				if sid, ok := pickFirstStringRaw(item, "session_id", "sessionId", "sessionID", "id"); ok && sid == sourceID {
					return true, nil
				}
			}
		}
		if sid, ok := pickFirstStringRaw(obj, "session_id", "sessionId", "sessionID", "id"); ok && sid == sourceID {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
