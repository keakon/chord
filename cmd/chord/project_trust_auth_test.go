package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthLoginProvidersRequireProjectTrust(t *testing.T) {
	t.Chdir(t.TempDir())
	setupDoctorConfigHome(t, "")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".chord"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".chord", "config.yaml"), []byte("providers:\n  project:\n    preset: codex\n    type: responses\n    api_url: https://project.example/v1/responses\nproxy: socks5://project-proxy.example:1080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	// The provider login and refresh flows contact project-declared endpoints,
	// so they refuse unauthorized project inputs like mcp login does.
	_, _, err := loadAuthLoginProviders(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("provider flows without authorization must fail, got %v", err)
	}

	// The one-run trust override applies the current project layer.
	flagSkipProjectConfig, flagTrustProjectConfig = false, true
	t.Cleanup(func() { flagSkipProjectConfig, flagTrustProjectConfig = false, false })
	providers, _, err := loadAuthLoginProviders(t.Context())
	if err != nil {
		t.Fatalf("provider flows with --trust-project-config must proceed: %v", err)
	}
	if _, ok := providers["project"]; !ok {
		t.Fatal("trusted project layer must expose project providers")
	}

	// --skip-project-config applies no project layer at all.
	flagSkipProjectConfig, flagTrustProjectConfig = true, false
	providers, _, err = loadAuthLoginProviders(t.Context())
	if err != nil {
		t.Fatalf("provider flows with --skip-project-config must proceed: %v", err)
	}
	if _, ok := providers["project"]; ok {
		t.Fatalf("skipped project layer must not expose project providers: %v", providers)
	}

	// Contradictory one-run overrides are rejected.
	flagSkipProjectConfig, flagTrustProjectConfig = true, true
	if _, _, err := loadAuthLoginProviders(t.Context()); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("contradictory overrides must fail, got %v", err)
	}
}
