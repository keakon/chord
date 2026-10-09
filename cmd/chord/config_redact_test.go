package main

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestSensitiveConfigKey(t *testing.T) {
	sensitive := []string{"api_key", "apiKey", "client_secret", "password", "authorization", "token", "refresh", "access", "Key"}
	for _, key := range sensitive {
		if !sensitiveConfigKey(key) {
			t.Fatalf("sensitiveConfigKey(%q) = false, want true", key)
		}
	}
	plain := []string{"token_url", "key_rotation", "key_order", "api_url", "type", "preset"}
	for _, key := range plain {
		if sensitiveConfigKey(key) {
			t.Fatalf("sensitiveConfigKey(%q) = true, want false", key)
		}
	}
}

func TestRedactConfigURLRedactsUserinfoAndDiagnosticURLs(t *testing.T) {
	raw := "http://sample-user:sample-password@proxy.example.invalid:8080?api_key=sample-key&region=test"
	redacted := redactConfigURL(raw)
	for _, secret := range []string{"sample-user", "sample-password", "sample-key"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("URL leaked %q: %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "proxy.example.invalid:8080") || !strings.Contains(redacted, "region=test") {
		t.Fatalf("endpoint lost: %s", redacted)
	}
	diagnostic := redactConfigDiagnostic(config.Diagnostic{Message: "invalid proxy " + raw})
	if strings.Contains(diagnostic.Message, "sample-password") {
		t.Fatalf("diagnostic leaked: %+v", diagnostic)
	}
	if strings.Contains(redactConfigURL("https://example.invalid?api_key=sample-key;invalid"), "sample-key") {
		t.Fatal("malformed query leaked")
	}
}
