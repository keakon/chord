package atomicfile

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestReplaceDurableProtectedACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "record.json")
	if _, err := ReplaceDurable(path, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Dir(path), path} {
		sd, err := windows.GetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		control, _, err := sd.Control()
		if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatalf("DACL not protected: %v", err)
		}
		sddl := sd.String()
		if !strings.Contains(sddl, user.User.Sid.String()) || strings.Contains(sddl, ";;;WD)") || strings.Contains(sddl, ";;;BU)") {
			t.Fatal("unexpected DACL subjects")
		}
	}
}
