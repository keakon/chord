package modelcatalog

import "testing"

func TestValidateVersion(t *testing.T) {
	for _, version := range []string{"2026-10-04.1", "2024-02-29.12"} {
		if err := ValidateVersion(version); err != nil {
			t.Fatal(err)
		}
	}
	for _, version := range []string{"v2026-10-04.1", "2026-02-29.1", "2026-10-04.0", "2026-10-04.01", "2026-10-04.+1", "2026-10-04.1.2", "1.2"} {
		if err := ValidateVersion(version); err == nil {
			t.Fatalf("accepted %q", version)
		}
	}
}
