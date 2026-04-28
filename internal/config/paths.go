package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

const (
	DefaultConfigDirName = "chord"
	projectMetaFileName  = "project.json"
)

type PathsConfig struct {
	StateDir    string `json:"state_dir,omitempty" yaml:"state_dir,omitempty"`
	CacheDir    string `json:"cache_dir,omitempty" yaml:"cache_dir,omitempty"`
	SessionsDir string `json:"sessions_dir,omitempty" yaml:"sessions_dir,omitempty"`
	LogsDir     string `json:"logs_dir,omitempty" yaml:"logs_dir,omitempty"`
}

type PathOptions struct{ ConfigHome, StateDir, CacheDir, SessionsDir, LogsDir string }

type PathLocator struct{ ConfigHome, StateDir, CacheDir, SessionsRoot, LogsDir, ExportsDir string }

type ProjectLocator struct {
	ProjectRoot, CanonicalRoot, LogicalRoot, HomeRelativeRoot, ProjectKey string
	ProjectSessionsDir, RuntimeCacheDir, TUIDumpsDir, ProjectExportsDir   string
	ProjectMetaPath, RegistryMetaPath                                     string
}

type ProjectMetadata struct {
	SchemaVersion    int    `json:"schema_version"`
	ProjectKey       string `json:"project_key"`
	CanonicalRoot    string `json:"canonical_root"`
	LogicalRoot      string `json:"logical_root"`
	HomeRelativeRoot string `json:"home_relative_root,omitempty"`
	SessionsDir      string `json:"sessions_dir"`
	RuntimeCacheDir  string `json:"runtime_cache_dir"`
	DisplayName      string `json:"display_name"`
	CreatedAt        string `json:"created_at"`
	LastUsedAt       string `json:"last_used_at"`
}

func ResolvePathLocator(cfg *Config, opts PathOptions) (*PathLocator, error) {
	configHome, err := resolveConfigHome(opts.ConfigHome)
	if err != nil {
		return nil, err
	}
	stateDir, err := resolveStateDir(cfg, opts.StateDir)
	if err != nil {
		return nil, err
	}
	cacheDir, err := resolveCacheDir(cfg, opts.CacheDir)
	if err != nil {
		return nil, err
	}
	sessionsRoot := firstNonEmpty(opts.SessionsDir, os.Getenv("CHORD_SESSIONS_DIR"))
	if sessionsRoot == "" && cfg != nil {
		sessionsRoot = strings.TrimSpace(cfg.Paths.SessionsDir)
	}
	if sessionsRoot == "" {
		sessionsRoot = filepath.Join(stateDir, "sessions")
	}
	sessionsRoot, err = expandClean(sessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve sessions dir: %w", err)
	}
	logsDir := firstNonEmpty(opts.LogsDir, os.Getenv("CHORD_LOGS_DIR"))
	if logsDir == "" && cfg != nil {
		logsDir = strings.TrimSpace(cfg.Paths.LogsDir)
	}
	if logsDir == "" {
		logsDir = filepath.Join(stateDir, "logs")
	}
	logsDir, err = expandClean(logsDir)
	if err != nil {
		return nil, fmt.Errorf("resolve logs dir: %w", err)
	}
	return &PathLocator{ConfigHome: configHome, StateDir: stateDir, CacheDir: cacheDir, SessionsRoot: sessionsRoot, LogsDir: logsDir, ExportsDir: filepath.Join(stateDir, "exports")}, nil
}
func DefaultPathLocator() (*PathLocator, error) { return ResolvePathLocator(nil, PathOptions{}) }
func ConfigHomeDir() (string, error) {
	l, err := ResolvePathLocator(nil, PathOptions{})
	if err != nil {
		return "", err
	}
	return l.ConfigHome, nil
}
func AuthPath() (string, error) {
	h, err := ConfigHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "auth.yaml"), nil
}
func ConfigPath() (string, error) {
	h, err := ConfigHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "config.yaml"), nil
}

func (l *PathLocator) LocateProject(projectRoot string) (*ProjectLocator, error) {
	if l == nil {
		return nil, fmt.Errorf("path locator is nil")
	}
	canonical, err := CanonicalProjectRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	logical, homeRel := LogicalProjectRoot(canonical)
	baseKey := SanitizeProjectKey(logical)
	key := baseKey
	metaPath := filepath.Join(l.SessionsRoot, key, projectMetaFileName)
	if meta, err := readProjectMetadata(metaPath); err == nil && meta.CanonicalRoot != canonical {
		key = baseKey + "--" + shortFingerprint(canonical)
		metaPath = filepath.Join(l.SessionsRoot, key, projectMetaFileName)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return &ProjectLocator{ProjectRoot: projectRoot, CanonicalRoot: canonical, LogicalRoot: logical, HomeRelativeRoot: homeRel, ProjectKey: key, ProjectSessionsDir: filepath.Join(l.SessionsRoot, key), RuntimeCacheDir: filepath.Join(l.CacheDir, "runtime", "session-cache", key), TUIDumpsDir: filepath.Join(l.LogsDir, "tui-dumps"), ProjectExportsDir: filepath.Join(l.ExportsDir, key), ProjectMetaPath: metaPath, RegistryMetaPath: filepath.Join(l.StateDir, "projects", key+".json")}, nil
}
func (l *PathLocator) EnsureProject(projectRoot string) (*ProjectLocator, error) {
	pl, err := l.LocateProject(projectRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(pl.ProjectSessionsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create project sessions dir: %w", err)
	}
	if err := writeProjectMetadata(pl); err != nil {
		return nil, err
	}
	if err := writeProjectRegistryMetadata(pl); err != nil {
		return nil, err
	}
	return pl, nil
}

func CanonicalProjectRoot(projectRoot string) (string, error) {
	if strings.TrimSpace(projectRoot) == "" {
		return "", fmt.Errorf("project root is empty")
	}
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("abs project root: %w", err)
	}
	clean := filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		clean = real
	}
	if runtime.GOOS == "darwin" {
		clean = norm.NFC.String(clean)
	}
	if runtime.GOOS == "windows" && len(clean) >= 2 && clean[1] == ':' {
		clean = strings.ToUpper(clean[:1]) + clean[1:]
	}
	return clean, nil
}
func LogicalProjectRoot(canonical string) (logical, homeRel string) {
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		home, _ = CanonicalProjectRoot(home)
		if rel, err := filepath.Rel(home, canonical); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".." {
			rel = filepath.ToSlash(rel)
			return "HOME/" + rel, rel
		}
		if canonical == home {
			return "HOME", ""
		}
	}
	return "ABS" + filepath.ToSlash(canonical), ""
}

var sanitizeDashRE = regexp.MustCompile(`-+`)

func SanitizeProjectKey(logical string) string {
	var b strings.Builder
	for _, r := range logical {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	key := strings.TrimRight(sanitizeDashRE.ReplaceAllString(b.String(), "-"), "-")
	if key == "" {
		key = "project"
	}
	if len(key) > 200 {
		key = strings.TrimRight(key[:190], "-") + "--" + shortFingerprint(logical)
	}
	return key
}
func resolveConfigHome(explicit string) (string, error) {
	path := firstNonEmpty(explicit, os.Getenv("CHORD_CONFIG_HOME"))
	if path == "" {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			path = filepath.Join(xdg, DefaultConfigDirName)
		}
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("get home directory: %w", err)
		}
		path = filepath.Join(home, ".config", DefaultConfigDirName)
	}
	return expandClean(path)
}
func resolveStateDir(cfg *Config, explicit string) (string, error) {
	path := firstNonEmpty(explicit, os.Getenv("CHORD_STATE_DIR"))
	if path == "" && cfg != nil {
		path = strings.TrimSpace(cfg.Paths.StateDir)
	}
	if path == "" {
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			path = filepath.Join(xdg, DefaultConfigDirName)
		}
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("get home directory: %w", err)
		}
		path = filepath.Join(home, ".local", "state", DefaultConfigDirName)
	}
	return expandClean(path)
}
func resolveCacheDir(cfg *Config, explicit string) (string, error) {
	path := firstNonEmpty(explicit, os.Getenv("CHORD_CACHE_DIR"))
	if path == "" && cfg != nil {
		path = strings.TrimSpace(cfg.Paths.CacheDir)
	}
	if path == "" {
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			path = filepath.Join(xdg, DefaultConfigDirName)
		}
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("get home directory: %w", err)
		}
		path = filepath.Join(home, ".cache", DefaultConfigDirName)
	}
	return expandClean(path)
}
func expandClean(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func shortFingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}
func readProjectMetadata(path string) (*ProjectMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m ProjectMetadata
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse project metadata %s: %w", path, err)
	}
	return &m, nil
}
func writeProjectMetadata(pl *ProjectLocator) error {
	existing, err := readProjectMetadata(pl.ProjectMetaPath)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	created := now
	if err == nil {
		if existing.CanonicalRoot != pl.CanonicalRoot {
			return fmt.Errorf("project key collision for %s: %s != %s", pl.ProjectKey, existing.CanonicalRoot, pl.CanonicalRoot)
		}
		created = existing.CreatedAt
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	meta := ProjectMetadata{SchemaVersion: 1, ProjectKey: pl.ProjectKey, CanonicalRoot: pl.CanonicalRoot, LogicalRoot: pl.LogicalRoot, HomeRelativeRoot: pl.HomeRelativeRoot, SessionsDir: filepath.ToSlash(filepath.Join("sessions", pl.ProjectKey)), RuntimeCacheDir: filepath.ToSlash(filepath.Join("runtime", "session-cache", pl.ProjectKey)), DisplayName: filepath.Base(pl.CanonicalRoot), CreatedAt: created, LastUsedAt: now}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(pl.ProjectMetaPath), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", pl.ProjectMetaPath, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, pl.ProjectMetaPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
func writeProjectRegistryMetadata(pl *ProjectLocator) error {
	data, err := os.ReadFile(pl.ProjectMetaPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pl.RegistryMetaPath), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", pl.RegistryMetaPath, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, pl.RegistryMetaPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
