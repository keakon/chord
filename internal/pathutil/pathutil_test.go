package pathutil

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandTildeUnix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "bare tilde", in: "~", want: "EXPANDED"},
		{name: "tilde slash", in: "~/notes.txt", want: "EXPANDED/notes.txt"},
		{name: "tilde backslash is literal", in: `~\notes.txt`, want: `~\notes.txt`},
		{name: "tilde inside component", in: "a/~b/c", want: "a/~b/c"},
		{name: "plain relative", in: "src/main.go", want: "src/main.go"},
		{name: "absolute", in: "/tmp/x", want: "/tmp/x"},
		{name: "whitespace trimmed", in: "  ~/a  ", want: "EXPANDED/a"},
	}
	wantHome := strings.TrimSuffix(wantHomeDir(t), "/")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandTilde(tt.in, false)
			if err != nil {
				t.Fatalf("expandTilde(%q) error: %v", tt.in, err)
			}
			want := strings.ReplaceAll(tt.want, "EXPANDED", wantHome)
			if got != want {
				t.Fatalf("expandTilde(%q) = %q, want %q", tt.in, got, want)
			}
		})
	}
}

func TestExpandTildeWindows(t *testing.T) {
	sep := string(filepath.Separator)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "bare tilde", in: "~", want: "EXPANDED"},
		{name: "backslash separator", in: `~\notes.txt`, want: "EXPANDED" + sep + "notes.txt"},
		{name: "slash separator", in: "~/notes.txt", want: "EXPANDED" + sep + "notes.txt"},
		{name: "tilde inside component", in: "a/~b/c", want: "a/~b/c"},
		{name: "drive-relative", in: "C:/x", want: "C:/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandTilde(tt.in, true)
			if err != nil {
				t.Fatalf("expandTilde(%q) error: %v", tt.in, err)
			}
			want := strings.ReplaceAll(tt.want, "EXPANDED", wantHomeDir(t))
			if got != want {
				t.Fatalf("expandTilde(%q) = %q, want %q", tt.in, got, want)
			}
		})
	}
}

func wantHomeDir(t *testing.T) string {
	t.Helper()
	rc, err := expandTilde("~", false)
	if err != nil {
		t.Fatalf("expanding home dir: %v", err)
	}
	return rc
}

func TestResolveInDir(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		baseDir string
		want    string
	}{
		{name: "relative joins base", path: "a/b", baseDir: "/repo", want: "/repo/a/b"},
		{name: "dot-prefix cleaned", path: "./a", baseDir: "/repo", want: "/repo/a"},
		{name: "inner dotdot cleaned", path: "a/../b", baseDir: "/repo", want: "/repo/b"},
		{name: "escaping dotdot", path: "../x", baseDir: "/repo", want: "/x"},
		{name: "absolute stays", path: "/etc/hosts", baseDir: "/repo", want: "/etc/hosts"},
		{name: "empty base keeps relative", path: "a/b", baseDir: "", want: "a/b"},
		{name: "empty path", path: "", baseDir: "/repo", want: "."},
		{name: "tilde expand", path: "~/x", baseDir: "/repo", want: "EXPANDED/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveInDir(tt.path, tt.baseDir)
			if err != nil {
				t.Fatalf("ResolveInDir(%q, %q) error: %v", tt.path, tt.baseDir, err)
			}
			want := strings.ReplaceAll(tt.want, "EXPANDED", wantHomeDir(t))
			if got != want {
				t.Fatalf("ResolveInDir(%q, %q) = %q, want %q", tt.path, tt.baseDir, got, want)
			}
		})
	}
}

func TestNormalizeWithinBase(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		baseDir string
		want    string
	}{
		{name: "inside becomes relative", path: "/repo/src/x.go", baseDir: "/repo", want: "src/x.go"},
		{name: "relative inside", path: "src/x.go", baseDir: "/repo", want: "src/x.go"},
		{name: "base itself", path: "/repo", baseDir: "/repo", want: "."},
		{name: "escape stays absolute", path: "/tmp/out", baseDir: "/repo", want: "/tmp/out"},
		{name: "escape via dotdot becomes absolute", path: "../out", baseDir: "/repo", want: "/out"},
		{name: "clean traversal", path: "/repo/a/../b", baseDir: "/repo", want: "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeWithinBase(tt.path, tt.baseDir)
			if err != nil {
				t.Fatalf("NormalizeWithinBase(%q, %q) error: %v", tt.path, tt.baseDir, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeWithinBase(%q, %q) = %q, want %q", tt.path, tt.baseDir, got, tt.want)
			}
		})
	}
}

func TestRelToBase(t *testing.T) {
	tests := []struct {
		name string
		path string
		base string
		want string
		ok   bool
	}{
		{name: "descendant", path: "/repo/a/b", base: "/repo", want: "a/b", ok: true},
		{name: "base itself", path: "/repo", base: "/repo", want: ".", ok: true},
		{name: "escape", path: "/var/x", base: "/repo", want: "", ok: false},
		{name: "parent hop", path: "/repo/../x", base: "/repo", want: "", ok: false},
		{name: "sibling prefix", path: "/repo2/x", base: "/repo", want: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := RelToBase(tt.path, tt.base)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("RelToBase(%q, %q) = (%q, %v), want (%q, %v)", tt.path, tt.base, got, ok, tt.want, tt.ok)
			}
		})
	}
}
