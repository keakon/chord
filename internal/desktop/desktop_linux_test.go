package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopEntriesPreserveHiddenMenuAssociations(t *testing.T) {
	for _, tc := range []struct {
		name, flags string
		present     bool
	}{
		{"visible", "", true},
		{"no display", "NoDisplay=true\n", true},
		{"hidden", "Hidden=true\n", false},
		{"hidden no display", "Hidden=true\nNoDisplay=true\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_DATA_HOME", root)
			t.Setenv("XDG_DATA_DIRS", root)
			if err := os.MkdirAll(filepath.Join(root, "applications"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "applications", "viewer.desktop"), []byte("[Desktop Entry]\nType=Application\nName=Viewer\nMimeType=text/plain;\nExec=viewer %f\n"+tc.flags), 0600); err != nil {
				t.Fatal(err)
			}
			entries, err := desktopEntries(t.Context(), []string{"viewer.desktop"})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := entries["viewer.desktop"]; ok != tc.present {
				t.Fatalf("entry present=%v want %v", ok, tc.present)
			}
		})
	}
}
