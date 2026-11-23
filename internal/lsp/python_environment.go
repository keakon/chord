package lsp

import (
	"context"
	"crypto/sha256"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/worktree"
)

func discoverPythonInterpreterInLinkedWorktrees(ctx context.Context, workspaceRoot string) string {
	if workspaceRoot == "" || ctx.Err() != nil {
		return ""
	}
	workspaceRoot = normalizeWaiterPath(workspaceRoot)
	// The LSP boundary may be the session's starting directory inside a checkout.
	checkoutRoot := pathutil.CheckoutRoot(workspaceRoot, "")
	if !pathExists(filepath.Join(checkoutRoot, ".git")) {
		return ""
	}
	projectRel, ok := pathutil.RelToBase(workspaceRoot, checkoutRoot)
	if !ok {
		return ""
	}
	commonDir, err := worktree.GitCommonDir(ctx, checkoutRoot)
	if err != nil {
		return ""
	}
	checkouts, err := worktree.CheckoutPathsInMain(ctx, commonDir)
	if err != nil || len(checkouts) < 2 || !slices.Contains(checkouts, checkoutRoot) {
		return ""
	}

	// Git lists the main checkout first; order the rest independently of Git.
	slices.Sort(checkouts[1:])
	var want map[string][sha256.Size]byte
	for _, checkout := range checkouts {
		if ctx.Err() != nil {
			return ""
		}
		if checkout == checkoutRoot {
			continue
		}
		candidate := filepath.Join(checkout, projectRel)
		real, err := filepath.EvalSymlinks(candidate)
		if err != nil || real != candidate || pathutil.CheckoutRoot(candidate, checkout) != checkout {
			continue
		}
		python := discoverPythonInterpreterAt(candidate, runtime.GOOS, func(venv, python string) bool {
			// A venv can symlink its executable to the base Python, but the
			// environment itself must live in this checkout.
			bin := filepath.Dir(python)
			realBin, err := filepath.EvalSymlinks(bin)
			if err != nil || realBin != bin {
				return false
			}
			info, err := os.Lstat(filepath.Join(venv, "pyvenv.cfg"))
			if err != nil || !info.Mode().IsRegular() {
				return false
			}
			// Pyright may run the interpreter to resolve its import paths.
			tracked, err := worktree.HasTrackedFiles(ctx, candidate, filepath.Base(venv))
			return err == nil && !tracked
		})
		if python == "" {
			continue
		}
		if want == nil {
			want = pythonEnvironmentManifests(ctx, workspaceRoot)
			if want == nil {
				return ""
			}
		}
		if !maps.Equal(pythonEnvironmentManifests(ctx, candidate), want) {
			continue
		}
		// A registered worktree path may have been reused by another repository.
		if dir, err := worktree.GitCommonDir(ctx, candidate); err == nil && dir == commonDir {
			return python
		}
	}
	return ""
}

func pythonEnvironmentManifests(ctx context.Context, root string) map[string][sha256.Size]byte {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	checkoutRoot := pathutil.CheckoutRoot(rootAbs, "")
	projectRel, ok := pathutil.RelToBase(rootAbs, checkoutRoot)
	if !ok {
		return nil
	}
	checkoutReal, err := filepath.EvalSymlinks(checkoutRoot)
	if err != nil {
		return nil
	}
	// Keep optional startup discovery bounded even if a checkout has an oversized lock.
	remaining := int64(16 << 20)
	manifests := make(map[string][sha256.Size]byte)
	hasProject := false
	const maxManifestFiles = 1024
	state := make(map[string]uint8)
	var addManifest func(string, bool) bool
	addManifest = func(rel string, requirements bool) bool {
		if ctx.Err() != nil {
			return false
		}
		rel = filepath.Clean(rel)
		fullPath := filepath.Join(checkoutRoot, rel)
		if _, ok := pathutil.RelToBase(fullPath, checkoutRoot); !ok || filepath.IsAbs(rel) || rel == "." {
			return false
		}
		key := filepath.ToSlash(rel)
		switch state[key] {
		case 1:
			return false
		case 2:
			return true
		}
		if len(manifests) >= maxManifestFiles {
			return false
		}
		realPath, err := filepath.EvalSymlinks(fullPath)
		if err != nil {
			return false
		}
		if _, ok := pathutil.RelToBase(realPath, checkoutReal); !ok {
			return false
		}
		info, err := os.Stat(realPath)
		if err != nil || !info.Mode().IsRegular() || info.Size() > remaining {
			return false
		}
		f, err := os.Open(realPath)
		if err != nil {
			return false
		}
		data, err := io.ReadAll(io.LimitReader(f, remaining+1))
		_ = f.Close()
		if err != nil || int64(len(data)) > remaining {
			return false
		}
		remaining -= int64(len(data))
		manifests[key] = sha256.Sum256(data)
		state[key] = 1
		if requirements {
			includes, ok := pythonRequirementIncludes(data)
			if !ok {
				return false
			}
			for _, include := range includes {
				includeRel := filepath.Clean(filepath.Join(filepath.Dir(rel), filepath.FromSlash(include)))
				if !addManifest(includeRel, true) {
					return false
				}
			}
		}
		state[key] = 2
		return true
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil
		}
		name := entry.Name()
		requirements := false
		switch name {
		case "pyproject.toml", "setup.py", "setup.cfg", "Pipfile", "environment.yml", "environment.yaml", "conda.yml", "conda.yaml":
			hasProject = true
		case ".python-version", "uv.lock", "poetry.lock", "pdm.lock", "pdm.toml", "Pipfile.lock":
		default:
			requirements = strings.HasPrefix(name, "requirements")
			if !(requirements || strings.HasPrefix(name, "constraints")) || !(strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".in")) {
				continue
			}
			hasProject = hasProject || requirements
		}
		if !addManifest(filepath.Join(projectRel, name), requirements) {
			return nil
		}
	}
	if !hasProject {
		return nil
	}
	return manifests
}

func pythonRequirementIncludes(data []byte) ([]string, bool) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var logical strings.Builder
	var includes []string
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		continued := strings.HasSuffix(strings.TrimRight(line, " \t"), "\\")
		if continued {
			logical.WriteString(strings.TrimSuffix(strings.TrimRight(line, " \t"), "\\"))
			logical.WriteByte(' ')
			continue
		}
		logical.WriteString(line)
		current := logical.String()
		logical.Reset()
		if hash := strings.Index(current, " #"); hash >= 0 {
			current = current[:hash]
		}
		fields := strings.Fields(current)
		for i := 0; i < len(fields); i++ {
			path, consumed, found, ok := pythonRequirementInclude(fields, i)
			if !ok {
				return nil, false
			}
			if found {
				includes = append(includes, path)
				i += consumed - 1
			}
		}
	}
	if logical.Len() > 0 {
		return nil, false
	}
	return includes, true
}

func pythonRequirementInclude(fields []string, i int) (path string, consumed int, found, ok bool) {
	token := fields[i]
	switch {
	case token == "-r" || token == "--requirement" || token == "-c" || token == "--constraint":
		if i+1 >= len(fields) {
			return "", 0, true, false
		}
		path, consumed, found = fields[i+1], 2, true
	case strings.HasPrefix(token, "--requirement=") || strings.HasPrefix(token, "--constraint="):
		path, consumed, found = strings.SplitN(token, "=", 2)[1], 1, true
	case strings.HasPrefix(token, "-r") && len(token) > 2:
		path, consumed, found = strings.TrimPrefix(token[2:], "="), 1, true
	case strings.HasPrefix(token, "-c") && len(token) > 2:
		path, consumed, found = strings.TrimPrefix(token[2:], "="), 1, true
	default:
		return "", 0, false, true
	}
	if len(path) >= 2 && (path[0] == '"' && path[len(path)-1] == '"' || path[0] == '\'' && path[len(path)-1] == '\'') {
		path = path[1 : len(path)-1]
	} else if strings.ContainsAny(path, "'\"") {
		return "", 0, true, false
	}
	parsed, err := url.Parse(path)
	if err != nil || path == "" || filepath.IsAbs(filepath.FromSlash(path)) || parsed.Scheme != "" || parsed.Host != "" {
		return "", 0, true, false
	}
	return path, consumed, true, true
}
