package filectx

import (
	"bytes"
	"testing"
)

func TestPrepareFilePartDenseLinesAllocs(t *testing.T) {
	data := bytes.Repeat([]byte("\n"), 4<<20)
	allocs := testing.AllocsPerRun(3, func() {
		content := prepareFilePart(data, LineRange{}, MaxFileBytes)
		if content.totalLines != len(data) || len(content.body) != MaxFileBytes {
			t.Fatalf("dense line facts = %+v", content)
		}
	})
	if allocs > 0 {
		t.Fatalf("dense line scan allocated %.0f objects", allocs)
	}
}

func BenchmarkBuildFilePartsDenseLines(b *testing.B) {
	for _, tc := range []struct {
		name    string
		pattern []byte
	}{
		{"blank", []byte("\n")},
		{"short", []byte("key = value\n")},
	} {
		b.Run(tc.name, func(b *testing.B) {
			data := bytes.Repeat(tc.pattern, (4<<20)/len(tc.pattern))
			opts := BuildFilePartsOptions{ReadFile: func(string) ([]byte, error) { return data, nil }}
			paths := []string{"sample.txt"}
			resolve := func(path string) string { return path }
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				BuildFilePartsWithOptions(paths, resolve, opts)
			}
		})
	}
}
