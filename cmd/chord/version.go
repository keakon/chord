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
// We keep Version bare so external integrations can rely on it.
var Version = buildinfo.Current().Version

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
