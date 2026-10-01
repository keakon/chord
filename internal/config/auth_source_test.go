package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCredentialDeclarations(t *testing.T) {
	t.Setenv("SET_VAR", "value")
	t.Setenv("BRACED_VAR", "value")
	t.Setenv("UNSET_VAR", "")
	path := filepath.Join(t.TempDir(), "auth.yaml")
	data := `sample-provider:
  - sk-literal
  - $SET_VAR
  - $UNSET_VAR
  - ${BRACED_VAR}
  - ""
  - refresh: r
oauth-provider:
  - access: a
empty-provider: []
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	decls, err := LoadCredentialDeclarations(path)
	if err != nil {
		t.Fatalf("LoadCredentialDeclarations: %v", err)
	}

	sample := decls["sample-provider"]
	if len(sample.Sources) != 6 {
		t.Fatalf("sample-provider sources = %+v, want 6 declarations", sample.Sources)
	}
	wantKinds := []CredentialSourceKind{
		CredentialSourceLiteral,
		CredentialSourceEnv,
		CredentialSourceEnv,
		CredentialSourceEnv,
		CredentialSourceExplicitEmpty,
		CredentialSourceOAuth,
	}
	for i, want := range wantKinds {
		if sample.Sources[i].Kind != want {
			t.Fatalf("source[%d].Kind = %q, want %q", i, sample.Sources[i].Kind, want)
		}
		if sample.Sources[i].Line == 0 || sample.Sources[i].File != path {
			t.Fatalf("source[%d] location = %s:%d, want a positioned declaration", i, sample.Sources[i].File, sample.Sources[i].Line)
		}
	}
	wantEnvVars := []string{"SET_VAR", "UNSET_VAR", "BRACED_VAR"}
	for i, want := range wantEnvVars {
		if got := sample.Sources[i+1].EnvVar; got != want {
			t.Fatalf("source[%d].EnvVar = %q, want %q", i+1, got, want)
		}
	}

	if !decls.Declared("sample-provider") {
		t.Fatal("Declared(sample-provider) = false, want true")
	}
	if !decls.Declared("empty-provider") || !decls["empty-provider"].DeclaredEmptyList {
		t.Fatal("empty-provider: explicitly empty list must count as a declaration")
	}
	if decls.Declared("oauth-provider") != true || len(decls["oauth-provider"].Sources) != 1 {
		t.Fatal("oauth-provider declaration not tracked")
	}
	if decls.Declared("absent-provider") {
		t.Fatal("Declared(absent-provider) = true, want false")
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

func TestLoadCredentialDeclarationsNoSecretsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.yaml")
	data := `sample-provider:
  - sk-very-secret-value
  - refresh: super-secret-refresh
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	decls, err := LoadCredentialDeclarations(path)
	if err != nil {
		t.Fatalf("LoadCredentialDeclarations: %v", err)
	}
	// The declaration metadata must not carry any credential material.
	for _, d := range decls["sample-provider"].Sources {
		if d.Kind != CredentialSourceLiteral && d.Kind != CredentialSourceOAuth {
			t.Fatalf("source kind = %q, want literal or oauth", d.Kind)
		}
	}
}
