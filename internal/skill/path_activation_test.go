package skill

import (
	"path/filepath"
	"testing"
)

func TestPathActivationPatterns(t *testing.T) {
	for _, tc := range []struct {
		patterns           []string
		path               string
		conditional, match bool
	}{
		{nil, "main.go", false, false}, {[]string{}, "main.go", false, false},
		{[]string{"**"}, "main.go", true, true}, {[]string{"**/*.go"}, "main.go", true, true},
		{[]string{"docs/**", "src/**"}, "src/a.txt", true, true},
		{[]string{"src/**"}, "SRC/a.go", true, false},
	} {
		d := BuildPathDescriptor(&Meta{Name: "sample", Location: filepath.Join(t.TempDir(), "SKILL.md"), Paths: tc.patterns})
		if d.Conditional != tc.conditional || d.Match(tc.path) != tc.match || d.Diagnostic != "" {
			t.Fatalf("%v: %+v", tc, d)
		}
	}
	for _, pattern := range []string{"", " ", "a ", "/a", "C:/a", "//host/a", "a/../b", "./a", "!a", `a\b`, "a//b", "a/", "["} {
		d := BuildPathDescriptor(&Meta{Name: "sample", Paths: []string{"**", pattern}})
		if !d.Conditional || d.Diagnostic == "" || d.Match("a.go") {
			t.Fatalf("invalid pattern %q accepted: %+v", pattern, d)
		}
	}
}
func TestPathActivationRevisionSetAndIdentity(t *testing.T) {
	m := &Meta{Name: "sample", Location: filepath.Join(t.TempDir(), "SKILL.md"), Paths: []string{"src/**", "**/*.go"}}
	a := BuildPathDescriptor(m)
	m.Paths = []string{"**/*.go", "src/**", "src/**"}
	b := BuildPathDescriptor(m)
	if a.Revision != b.Revision || a.Identity != b.Identity {
		t.Fatal("OR order changed descriptor")
	}
	m.Description = "changed"
	c := BuildPathDescriptor(m)
	if c.Revision == b.Revision || c.Identity != b.Identity {
		t.Fatal("metadata revision did not isolate content from identity")
	}
	m.Location = filepath.Join(t.TempDir(), "SKILL.md")
	if BuildPathDescriptor(m).Identity == c.Identity {
		t.Fatal("location not part of identity")
	}
}

func TestPathActivationCatalogSnapshotOwnsMetadata(t *testing.T) {
	original := &Meta{Name: "sample", Location: filepath.Join(t.TempDir(), "SKILL.md"), Description: "old", Paths: []string{"src/**"}, AllowedTools: []string{"read"}, Resources: []string{"reference.txt"}}
	catalog := SnapshotCatalog([]*Meta{original})
	descriptor := PathActivationDescriptor(catalog[0])
	original.Description = "new"
	original.Paths[0] = "docs/**"
	original.AllowedTools[0] = "bash"
	original.Resources[0] = "changed.txt"
	if !PathActivationDescriptor(catalog[0]).Match("src/main.go") || PathActivationDescriptor(catalog[0]).Revision != descriptor.Revision || catalog[0].Description != "old" || catalog[0].Resources[0] != "reference.txt" {
		t.Fatal("installed catalog changed through caller metadata")
	}
}
