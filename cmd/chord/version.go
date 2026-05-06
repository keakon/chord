package main

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/buildinfo"
)

// Version is the bare application version string. It is used by MCP
// ClientInfo.Version and must remain a simple semver-style string without
// commit/dirty annotations.
//
// The CLI `--version` output is richer and is sourced from internal/buildinfo.
// We keep Version bare so external integrations can continue to rely on it.
//
// It is overridable at build time via the historical ldflags path:
//
//	-ldflags "-X main.Version=<version>"
//
// New builds may also (or instead) override internal/buildinfo.Version
// directly, together with Commit, BuildTime, and Dirty for richer diagnostics:
//
//	-ldflags "-X github.com/keakon/chord/internal/buildinfo.Version=<version> ..."
//
// init() below mirrors whichever side was set so MCP, startup logs, diagnostics
// dumps, and CLI version output always agree on the version.
var Version = "dev"

func init() {
	// init() runs after package-level var initialization for both this file
	// and internal/buildinfo, but before main() — and before anything calls
	// buildinfo.Current() (which is sync.OnceValue-cached). This is the
	// correct time to bridge the two ldflags paths.
	switch {
	case Version != "dev" && buildinfo.Version == "dev":
		// Only the historical -X main.Version=... path was used.
		buildinfo.Version = Version
	case Version == "dev" && buildinfo.Version != "dev":
		// Only the new -X .../buildinfo.Version=... path was used.
		Version = buildinfo.Version
	}
	// If both are set, we trust each — CI may set them deliberately and the
	// values are expected to match.
}

func cliVersionTemplate() string {
	return formatCLIVersionTemplate(buildinfo.Current())
}

func formatCLIVersionTemplate(info buildinfo.Info) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "chord version %s\n", info.Short())
	for _, field := range info.Fields() {
		if field.Key == "chord_version" {
			continue
		}
		fmt.Fprintf(&sb, "%s: %s\n", field.Key, field.Value)
	}
	return sb.String()
}
