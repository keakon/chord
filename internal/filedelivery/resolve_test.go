package filedelivery

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestWorkspaceBindingAndArtifactVersions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	loc, err := WorkspaceLocator(dir, "report.md")
	if err != nil {
		t.Fatal(err)
	}
	file, err := Inspect(context.Background(), loc, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if file.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("first"))) {
		t.Fatal("wrong digest")
	}
	if err = os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	current, err := Verify(context.Background(), file, "")
	if err != nil || current.SHA256 == file.SHA256 {
		t.Fatalf("workspace current version: %v", err)
	}
	session := t.TempDir()
	if err = os.Mkdir(filepath.Join(session, "artifacts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(session, "artifacts", "empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	artifactLoc, err := ArtifactLocator("artifacts/empty")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Inspect(context.Background(), artifactLoc, session, nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SizeBytes != 0 || artifact.SHA256 != fmt.Sprintf("%x", sha256.Sum256(nil)) {
		t.Fatal("empty artifact digest missing")
	}
	if err = os.WriteFile(filepath.Join(session, "artifacts", "empty"), []byte("overwritten"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(context.Background(), artifact, session); err == nil {
		t.Fatal("overwritten artifact accepted")
	}
}
func TestArtifactContainmentAndCancellation(t *testing.T) {
	for _, path := range []string{"../escape", "artifacts/../../escape", "/artifacts/a", "images", "x/a", "artifacts/a\x00"} {
		if _, err := ArtifactLocator(path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	session := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(session, "artifacts")); err != nil {
		t.Skip(err)
	}
	loc, _ := ArtifactLocator("artifacts/secret")
	if _, _, err := Open(loc, session); err == nil {
		t.Fatal("symlink root accepted")
	}
	file, _ := WorkspaceLocator(outside, "secret")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(ctx, file, "", nil); err == nil {
		t.Fatal("cancel ignored")
	}
	budget := int64(2)
	if _, err := Inspect(context.Background(), file, "", &budget); err == nil {
		t.Fatal("read budget ignored")
	}
}
func TestForkArtifactsSurviveSourceDeletion(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "artifacts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "artifacts", "report"), []byte("saved"), 0600); err != nil {
		t.Fatal(err)
	}
	loc, _ := ArtifactLocator("artifacts/report")
	file, err := Inspect(context.Background(), loc, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = CopyArtifacts(context.Background(), []message.DeliveredFile{file}, source, destination); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(context.Background(), file, destination); err != nil {
		t.Fatal(err)
	}
}
