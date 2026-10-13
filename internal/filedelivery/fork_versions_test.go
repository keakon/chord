package filedelivery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestForkCopiesOnlyAvailableVersionOfSamePath(t *testing.T) {
	ctx := context.Background()
	src, dst := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(src, "artifacts"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(src, "artifacts", "report.txt")
	loc, err := ArtifactLocator("artifacts/report.txt")
	if err != nil {
		t.Fatal(err)
	}
	versions := make([]message.DeliveredFile, 2)
	for i, text := range []string{"first", "second"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		versions[i], err = Inspect(ctx, loc, src, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := CopyArtifacts(ctx, versions, src, dst); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, versions[0], dst); err == nil {
		t.Fatal("overwritten version became available")
	}
	if _, err := Verify(ctx, versions[1], dst); err != nil {
		t.Fatal(err)
	}
}
