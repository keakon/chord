package desktop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMacAssociationSelectionUsesValidatedArguments(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("SSH_TTY", "")
	path := filepath.Join(t.TempDir(), "-中文 ' report.pdf")
	if err := os.WriteFile(path, []byte("%PDF"), 0600); err != nil {
		t.Fatal(err)
	}
	app := Application{ID: "file:///Applications/Example%20Viewer.app/", Name: "Example Viewer", Default: true}
	data, _ := json.Marshal([]Application{app})
	var launched []string
	s := New()
	s.run = func(_ context.Context, query bool, name string, args ...string) ([]byte, error) {
		if query {
			if args[len(args)-1] != path {
				t.Fatal("file interpolated into JXA")
			}
			return data, nil
		}
		launched = append([]string{name}, args...)
		return nil, nil
	}
	if err := s.OpenWith(context.Background(), path, app.ID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(launched, []string{"/usr/bin/open", "-a", "/Applications/Example Viewer.app/", path}) {
		t.Fatalf("unsafe argv: %#v", launched)
	}
	launched = nil
	if err := s.OpenWith(context.Background(), path, "file:///tmp/arbitrary.app"); err == nil || launched != nil {
		t.Fatal("unassociated app executed")
	}
}
