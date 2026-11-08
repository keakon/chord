package tools

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/textfile"
)

// Injected file references promise line counts that match what the read tool
// would report for the same bytes after decoding. Keep the two line models in
// one test so a change to either side shows up as a contract mismatch.
func TestFileInjectionLineCountMatchesReadTool(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"\n",
		"\r\n",
		"\r",
		"\n\n",
		"alpha\nbeta\n",
		"alpha\nbeta",
		"alpha\r\nbeta\r\n",
		"alpha\rbeta\r",
		"alpha\r\nbeta\ngamma\rdelta\n",
		"\xEF\xBB\xBFalpha\nbeta\n",
		"\xEF\xBB\xBF",
		"\xEF\xBB\xBF\n",
	}
	for _, data := range cases {
		t.Run(fmt.Sprintf("%q", data), func(t *testing.T) {
			t.Parallel()
			decoded, err := textfile.Decode([]byte(data), "sample.txt")
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			want := len(splitReadToolLines(decoded.Text))
			result := filectx.BuildFilePartsWithOptions(
				[]string{"sample.txt"},
				func(path string) string { return path },
				filectx.BuildFilePartsOptions{
					MaxFileBytes: 4096,
					ReadFile:     func(string) ([]byte, error) { return []byte(data), nil },
				},
			)
			if len(result.Parts) != 1 {
				t.Fatalf("parts = %#v, want one injected file", result.Parts)
			}
			attr := fmt.Sprintf(" total_lines=%q", strconv.Itoa(want))
			if !strings.Contains(result.Parts[0].Text[:strings.IndexByte(result.Parts[0].Text, '>')], attr) {
				t.Fatalf("injected reference = %q, want %q", result.Parts[0].Text, attr)
			}
		})
	}
}
