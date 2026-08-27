package pathutil

import (
	"path/filepath"
	"runtime"
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

func TestAbbreviateHomeIn(t *testing.T) {
	sep := string(filepath.Separator)
	home := filepath.Join(string(filepath.Separator), "Users", "me")
	tests := []struct {
		name string
		path string
		home string
		want string
	}{
		{name: "empty path", path: "", home: home, want: ""},
		{name: "blank path", path: "  ", home: home, want: ""},
		{name: "empty home", path: "/a/b", home: "", want: "/a/b"},
		{name: "home itself", path: home, home: home, want: "~"},
		{name: "under home", path: filepath.Join(home, "a", "b"), home: home, want: "~" + sep + "a" + sep + "b"},
		{name: "outside home", path: filepath.Join(string(filepath.Separator), "tmp", "x"), home: home, want: filepath.Join(string(filepath.Separator), "tmp", "x")},
		{name: "sibling prefix", path: home + "2" + sep + "x", home: home, want: home + "2" + sep + "x"},
		{name: "relative unchanged", path: "a/b", home: home, want: "a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AbbreviateHomeIn(tt.path, tt.home); got != tt.want {
				t.Fatalf("AbbreviateHomeIn(%q, %q) = %q, want %q", tt.path, tt.home, got, tt.want)
			}
		})
	}
}

func TestAbbreviateHomeInWindowsSpelling(t *testing.T) {
	tests := []struct {
		name string
		path string
		home string
		want string
	}{
		{name: "case-insensitive", path: `C:\Users\Me\AppData\Local\x`, home: `c:\users\me`, want: `~\AppData\Local\x`},
		{name: "forward separators in path", path: `C:/Users/me/x`, home: `C:\Users\me`, want: `~/x`},
		{name: "mixed separators", path: `c:\Users\me/AppData/x`, home: `C:\Users\me`, want: `~/AppData/x`},
		{name: "home itself case differs", path: `c:\users\me`, home: `C:\Users\me`, want: `~`},
		{name: "sibling volume prefix", path: `C:\Users\mex\x`, home: `C:\Users\me`, want: `C:\Users\mex\x`},
		{name: "different volume", path: `D:\Users\me\x`, home: `C:\Users\me`, want: `D:\Users\me\x`},
		{name: "home with trailing slash", path: `C:\Users\me\a`, home: `C:\Users\me\`, want: `~\a`},
		{name: "outside path unchanged", path: `C:\Temp\x`, home: `C:\Users\me`, want: `C:\Temp\x`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AbbreviateHomeIn(tt.path, tt.home); got != tt.want {
				t.Fatalf("AbbreviateHomeIn(%q, %q) = %q, want %q", tt.path, tt.home, got, tt.want)
			}
		})
	}
}

func TestAbbreviateHomeUsesSlashSeparators(t *testing.T) {
	home := wantHomeDir(t)
	abs := filepath.Join(home, ".chord", "sessions", "x", "history-1.md")
	abbrev := AbbreviateHome(abs)
	want := "~/" + filepath.ToSlash(filepath.Join(".chord", "sessions", "x", "history-1.md"))
	if abbrev != want {
		t.Fatalf("AbbreviateHome(%q) = %q, want %q", abs, abbrev, want)
	}
	if strings.Contains(abbrev, `\`) {
		t.Fatalf("AbbreviateHome returned backslash separators: %q", abbrev)
	}
	expanded, err := ExpandTilde(abbrev)
	if err != nil {
		t.Fatalf("ExpandTilde(%q): %v", abbrev, err)
	}
	if expanded != abs {
		t.Fatalf("round trip = %q, want %q", expanded, abs)
	}
	// Windows volume spelling abbreviates through AbbreviateHomeIn, whose
	// result is normalized to "/" by AbbreviateHome on the Windows platform.
	if got := AbbreviateHomeIn(`C:\Users\me\AppData\Local\x`, `C:\Users\me`); got != `~\AppData\Local\x` {
		t.Fatalf("windows spelling = %q, want %q", got, `~\AppData\Local\x`)
	}
	if runtime.GOOS == "windows" {
		if got := AbbreviateHome(`C:\Users\me\AppData\Local\x`); got != "~/AppData/Local/x" {
			t.Fatalf("windows AbbreviateHome = %q, want %q", got, "~/AppData/Local/x")
		}
	}
}

func TestAbbreviateHomeRoundTripWithExpandTilde(t *testing.T) {
	home := wantHomeDir(t)
	abs := filepath.Join(home, ".chord", "sessions", "x", "history-1.md")
	abbrev := AbbreviateHome(abs)
	want := "~" + string(filepath.Separator) + filepath.Join(".chord", "sessions", "x", "history-1.md")
	if abbrev != want {
		t.Fatalf("AbbreviateHome(%q) = %q, want %q", abs, abbrev, want)
	}
	expanded, err := ExpandTilde(abbrev)
	if err != nil {
		t.Fatalf("ExpandTilde(%q): %v", abbrev, err)
	}
	if expanded != abs {
		t.Fatalf("round trip = %q, want %q", expanded, abs)
	}
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
