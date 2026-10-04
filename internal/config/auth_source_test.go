package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCredentialDeclarations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.yaml")
	// Declarations count before environment expansion and availability
	// filtering: literals, $VAR references (set or not), an explicitly empty
	// key, an OAuth mapping, and an explicitly empty list all declare.
	data := `sample-provider:
  - sk-literal
  - $SET_VAR
  - $UNSET_VAR
  - ${BRACED_VAR}
  - ""
  - refresh: r
oauth-provider:
  - access: a
null-only-provider:
  - null
empty-provider: []
scalar-provider: sk-literal
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	decls, err := LoadCredentialDeclarations(path)
	if err != nil {
		t.Fatalf("LoadCredentialDeclarations: %v", err)
	}

	for _, provider := range []string{"sample-provider", "oauth-provider", "empty-provider"} {
		if !decls.Declared(provider) {
			t.Fatalf("Declared(%s) = false, want true", provider)
		}
	}
	for _, provider := range []string{"null-only-provider", "scalar-provider", "absent-provider"} {
		if decls.Declared(provider) {
			t.Fatalf("Declared(%s) = true, want false", provider)
		}
	}
}

func TestLoadCredentialDeclarationsMissingFile(t *testing.T) {
	decls, err := LoadCredentialDeclarations(filepath.Join(t.TempDir(), "auth.yaml"))
	if err != nil {
		t.Fatalf("LoadCredentialDeclarations: %v", err)
	}
	if decls == nil || decls.Declared("sample-provider") {
		t.Fatalf("decls = %+v, want empty non-nil", decls)
	}
}
