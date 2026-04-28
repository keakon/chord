package tools

import "testing"

func TestIsBinaryExtension(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		// Positive cases across every category.
		{"x.pyc", true},
		{"x.PYC", true}, // case-insensitive
		{"foo.class", true},
		{"lib.so", true},
		{"app.exe", true},
		{"archive.tar.gz", true}, // only the final extension is inspected
		{"pack.7z", true},
		{"data.sqlite3", true},
		{"photo.PNG", true},
		{"clip.webm", true},
		{"font.woff2", true},
		{"report.pdf", true},

		// Negative cases: source code, plain text, config, markup.
		{"main.go", false},
		{"README.md", false},
		{"Makefile", false}, // no extension → false
		{"script", false},
		{"icon.svg", false}, // SVG is text (XML)
		{"data.json", false},
		{"style.css", false},
		{"config.yaml", false},
		{".gitignore", false},
		{"lock.file", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBinaryExtension(tt.name); got != tt.want {
				t.Errorf("IsBinaryExtension(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
