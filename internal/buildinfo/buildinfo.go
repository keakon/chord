// Package buildinfo exposes a single source of truth for the running Chord
// binary's identity (version, commit, dirty state, build/VCS time, Go toolchain,
// executable path/mtime). It is consumed by the CLI version output, startup
// logs, and diagnostics dumps.
//
// The values are computed once on first use and cached for the rest of the
// process lifetime; no field changes after the binary starts.
package buildinfo

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Build-time variables. Release/CI builds may override these via -ldflags, e.g.
//
//	-X github.com/keakon/chord/internal/buildinfo.Version=v0.1.0
//	-X github.com/keakon/chord/internal/buildinfo.Commit=$(git rev-parse HEAD)
//	-X github.com/keakon/chord/internal/buildinfo.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)
//	-X github.com/keakon/chord/internal/buildinfo.Dirty=false
//
// Plain `go build ./cmd/chord` still records useful VCS fields through Go's
// build info when the build is performed inside a Git checkout with buildvcs
// enabled, so Commit and Dirty remain populated even without ldflags.
var (
	// Version may be set by release/CI builds via ldflags. When it is empty,
	// Current falls back to the module version embedded by `go install @version`,
	// then to DefaultDevVersion for local development builds.
	Version   = ""
	Commit    = ""
	BuildTime = ""
	Dirty     = ""
)

// Info describes the running Chord binary and the Go toolchain metadata
// embedded in it. BuildTime is only populated when explicitly injected by the
// build; VCSTime is the source revision time reported by Go build info and is
// not the same thing.
type Info struct {
	Version         string
	Commit          string
	BuildTime       string
	VCSTime         string
	Dirty           string // "true", "false", or "unknown"
	GoVersion       string
	GOOS            string
	GOARCH          string
	ExecutablePath  string
	ExecutableMTime string
}

// Field is a single key/value pair for diagnostics output. Field is a named
// type (rather than an anonymous struct) so callers can declare variables and
// helpers around the slice returned by [Info.Fields].
type Field struct {
	Key   string
	Value string
}

const (
	unknown           = "unknown"
	DefaultDevVersion = "v0.6.2-dev"
)

// current is the cached result of [computeCurrent]. The build identity does
// not change during a process's lifetime, so we read os.Stat / debug.ReadBuildInfo
// at most once per process.
var current = sync.OnceValue(computeCurrent)

var (
	semverTagPattern      = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?(?:\+[0-9A-Za-z][0-9A-Za-z.-]*)?$`)
	pseudoVersionSuffixRE = regexp.MustCompile(`(?:^|[.-])[0-9]{14}-[0-9a-fA-F]{12}$`)
)

// Current returns best-effort build metadata for the running binary. Explicit
// ldflags values take precedence over Go VCS fallback fields. The result is
// cached after the first call.
func Current() Info { return current() }

func computeCurrent() Info {
	settings, moduleVersion := readBuildMetadata()
	info := Info{
		Version:   resolvedVersion(Version, moduleVersion),
		Commit:    strings.TrimSpace(Commit),
		BuildTime: strings.TrimSpace(BuildTime),
		VCSTime:   strings.TrimSpace(settings["vcs.time"]),
		Dirty:     strings.TrimSpace(Dirty),
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}
	if info.Commit == "" {
		info.Commit = strings.TrimSpace(settings["vcs.revision"])
	}
	if info.Commit == "" {
		info.Commit = unknown
	}
	if info.BuildTime == "" {
		info.BuildTime = unknown
	}
	if info.VCSTime == "" {
		info.VCSTime = unknown
	}
	if info.Dirty == "" {
		info.Dirty = strings.TrimSpace(settings["vcs.modified"])
	}
	if info.Dirty == "" {
		info.Dirty = unknown
	}
	info.ExecutablePath, info.ExecutableMTime = executableMetadata()
	return info
}

// Short returns a compact one-line identity intended for human-facing
// surfaces (e.g. a `chord version --verbose` output). It includes the version,
// a trailing `*` on the version when the working tree was modified at build
// time, and the short commit when known. A clean or unknown dirty state is
// omitted to keep the line concise.
func (i Info) Short() string {
	version := valueOrUnknown(i.Version)
	if i.Dirty == "true" {
		version += "*"
	}
	parts := []string{version}
	if commit := shortCommit(i.Commit); commit != "" && commit != unknown {
		parts = append(parts, commit)
	}
	return strings.Join(parts, " ")
}

// Fields returns the full set of diagnostic key/value pairs in stable order.
// Used by diagnostics bundles and TUI dumps so every field line has the same
// `key: value` shape and ordering across surfaces.
func (i Info) Fields() []Field {
	return []Field{
		{"chord_version", valueOrUnknown(i.Version)},
		{"chord_commit", valueOrUnknown(i.Commit)},
		{"chord_build_time", valueOrUnknown(i.BuildTime)},
		{"chord_vcs_time", valueOrUnknown(i.VCSTime)},
		{"chord_dirty", valueOrUnknown(i.Dirty)},
		{"go_version", valueOrUnknown(i.GoVersion)},
		{"goos", valueOrUnknown(i.GOOS)},
		{"goarch", valueOrUnknown(i.GOARCH)},
		{"executable_path", valueOrUnknown(i.ExecutablePath)},
		{"executable_mtime", valueOrUnknown(i.ExecutableMTime)},
	}
}

// LogString returns a compact key=value list for startup logs. It includes
// only the fields that are meaningful at every startup (version, commit,
// dirty state, build/VCS time, Go toolchain). Long-tail metadata such as
// executable path and mtime is reserved for diagnostics dumps to keep the
// startup line a manageable length.
func (i Info) LogString() string {
	startupKeys := map[string]struct{}{
		"chord_version":    {},
		"chord_commit":     {},
		"chord_dirty":      {},
		"chord_build_time": {},
		"chord_vcs_time":   {},
		"go_version":       {},
	}
	fields := i.Fields()
	parts := make([]string, 0, len(startupKeys))
	for _, field := range fields {
		if _, ok := startupKeys[field.Key]; !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", field.Key, field.Value))
	}
	return strings.Join(parts, " ")
}

func readBuildMetadata() (map[string]string, string) {
	settings := make(map[string]string)
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi == nil {
		return settings, ""
	}
	for _, setting := range bi.Settings {
		settings[setting.Key] = setting.Value
	}
	return settings, bi.Main.Version
}

func resolvedVersion(explicitVersion, moduleVersion string) string {
	if version := strings.TrimSpace(explicitVersion); version != "" {
		return version
	}
	if version := strings.TrimSpace(moduleVersion); isReleaseModuleVersion(version) {
		return version
	}
	return DefaultDevVersion
}

func isReleaseModuleVersion(version string) bool {
	baseVersion, _, _ := strings.Cut(version, "+")
	return semverTagPattern.MatchString(version) && !pseudoVersionSuffixRE.MatchString(baseVersion)
}

func executableMetadata() (string, string) {
	path, err := os.Executable()
	if err != nil || strings.TrimSpace(path) == "" {
		return unknown, unknown
	}
	info, err := os.Stat(path)
	if err != nil {
		return path, unknown
	}
	mtime := info.ModTime()
	if mtime.IsZero() {
		return path, unknown
	}
	return path, mtime.Format(time.RFC3339Nano)
}

func shortCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if commit == "" || commit == unknown {
		return commit
	}
	if len(commit) <= 12 {
		return commit
	}
	return commit[:12]
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return unknown
	}
	return value
}
