package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestCommandOutputPreviewRetainsReusableEvidence(t *testing.T) {
	for _, name := range []string{tools.NameShell, tools.NameJobOutput} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			result := "start marker\n" + strings.Repeat("diagnostic detail\n", 1800) + "final status marker\n"
			got := formatToolExecutionOutput(result, dir, "call-123", name, nil, "")
			// The preview is bounded by the shell budget plus the marker line and
			// reference it carries, keeps both the leading and the trailing
			// evidence, and names the artifact holding the full result.
			if len(got) > shellResultPreviewBytes+4*1024 || !strings.Contains(got, "start marker") || !strings.Contains(got, "final status marker") || !strings.Contains(got, tools.ArtifactReferencePrefix) {
				t.Fatalf("missing bounded preview, evidence, or artifact: %s", got)
			}
			var files []string
			err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					files = append(files, path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 {
				t.Fatalf("artifacts = %v", files)
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != result {
				t.Fatal("saved output lost evidence")
			}
			if again := formatToolExecutionOutput(result, dir, "call-123", name, nil, ""); again != got {
				t.Fatal("formatting the same result changed its artifact reference")
			}
		})
	}
}
