package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tooloutput"
	"github.com/keakon/chord/internal/tools"
)

func readArgs(path string) string {
	return fmt.Sprintf(`{"path":%q,"offset":1,"limit":200}`, path)
}

func partialReadResult(start, end, total int) string {
	return fmt.Sprintf("READ_RESULT lines=%d-%d total=%d\nsample line\n", start, end, total)
}

func TestPartialReadCountingGates(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		args      string
		rawResult string
		failed    bool
	}{
		{"full read", tools.NameRead, readArgs("a.go"), partialReadResult(1, 940, 940), false},
		{"near-full remainder", tools.NameRead, readArgs("a.go"), partialReadResult(2, 941, 941), false},
		{"file exceeds one read", tools.NameRead, readArgs("a.go"), partialReadResult(1, 200, tooloutput.MaxOutputLines+500), false},
		{"budget truncated", tools.NameRead, readArgs("a.go"), "READ_RESULT lines=1-200 total=940 truncated=budget requested_lines=1-900\nsample\n", false},
		{"no range header", tools.NameRead, readArgs("a.go"), "READ_RESULT lines=none total=940\n", false},
		{"failed read", tools.NameRead, readArgs("a.go"), partialReadResult(1, 200, 940), true},
		{"not a read", tools.NameGrep, readArgs("a.go"), partialReadResult(1, 200, 940), false},
		{"malformed args", tools.NameRead, `{`, partialReadResult(1, 200, 940), false},
		{"invalid header", tools.NameRead, readArgs("a.go"), "READ_RESULT lines=0-200 total=940\nsample\n", false},
		{"missing path", tools.NameRead, `{}`, partialReadResult(1, 200, 940), false},
	}
	for _, tc := range cases {
		turn := &Turn{}
		for i := range 6 {
			if note := turn.efficiencyNoteForToolResult(fmt.Sprintf("c%d", i), tc.toolName, tc.args, tc.rawResult, tc.failed); note != "" {
				t.Fatalf("%s: unexpected note %q", tc.name, note)
			}
		}
	}
}

// Wrapped provider arguments still identify the same read path before the
// observation crosses the package boundary.
func TestEfficiencyReadObservationUnwrapsArguments(t *testing.T) {
	raw, err := json.Marshal(readArgs("pkg/sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	turn := &Turn{}
	notes := 0
	for i := range 6 {
		note := turn.efficiencyNoteForToolResult(fmt.Sprintf("read-%d", i), " read ", string(raw), partialReadResult(1, 200, 940), false)
		if note != "" {
			notes++
			if !strings.Contains(note, "pkg/sample.go") || !strings.Contains(note, "940 lines") {
				t.Fatalf("incomplete note: %q", note)
			}
		}
	}
	if notes != 1 {
		t.Fatalf("notes=%d, want one", notes)
	}
	var absent *Turn
	if note := absent.efficiencyNoteForToolResult("read", tools.NameRead, readArgs("pkg/sample.go"), partialReadResult(1, 200, 940), false); note != "" {
		t.Fatalf("absent turn produced %q", note)
	}
}
