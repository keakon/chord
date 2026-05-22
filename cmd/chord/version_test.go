package main

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/buildinfo"
)

func TestDefaultMainVersionMatchesBuildinfoDefaultDevVersion(t *testing.T) {
	if Version != buildinfo.DefaultDevVersion {
		t.Fatalf("Version = %q, want %q", Version, buildinfo.DefaultDevVersion)
	}
	if buildinfo.Version != buildinfo.DefaultDevVersion {
		t.Fatalf("buildinfo.Version = %q, want %q", buildinfo.Version, buildinfo.DefaultDevVersion)
	}
}

func TestFormatCLIVersionTemplateIncludesBuildMetadata(t *testing.T) {
	info := buildinfo.Info{
		Version:         "v1.2.3",
		Commit:          "abc123def4567890",
		BuildTime:       "2026-05-05T00:00:00Z",
		VCSTime:         "2026-05-04T00:00:00Z",
		Dirty:           "true",
		GoVersion:       "go1.26.0",
		GOOS:            "darwin",
		GOARCH:          "arm64",
		ExecutablePath:  "/tmp/chord",
		ExecutableMTime: "2026-05-05T01:00:00Z",
	}
	out := formatCLIVersionTemplate(info)

	if !strings.HasPrefix(out, "chord version v1.2.3* abc123def456\n") {
		t.Fatalf("version output header mismatch: %q", out)
	}
	for _, want := range []string{
		"chord_commit: abc123def4567890\n",
		"chord_dirty: true\n",
		"chord_build_time: 2026-05-05T00:00:00Z\n",
		"chord_vcs_time: 2026-05-04T00:00:00Z\n",
		"go_version: go1.26.0\n",
		"goos: darwin\n",
		"goarch: arm64\n",
		"executable_path: /tmp/chord\n",
		"executable_mtime: 2026-05-05T01:00:00Z\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("version output missing %q\n%s", want, out)
		}
	}
}
