package mcp

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type bindingMetadataFixture struct {
	*metadataFixture
	beforeRead func()
}

func (f *bindingMetadataFixture) GetJSON(ctx context.Context, endpoint string, out any) error {
	if f.beforeRead != nil {
		fn := f.beforeRead
		f.beforeRead = nil
		fn()
	}
	return f.metadataFixture.GetJSON(ctx, endpoint, out)
}

func TestOAuthAcquireValidTokenRechecksBinding(t *testing.T) {
	for _, scenario := range []string{"cache", "network failure", "logout during discovery"} {
		t.Run(scenario, func(t *testing.T) {
			store := NewCredentialStore(t.TempDir())
			target := refreshTestTarget()
			expected := seedRefreshTest(t, store, target, time.Now())
			if err := store.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
				slot := fileSlot(tx.File, target)
				slot.Token.ExpiresAt = time.Now().Add(time.Hour).UTC()
				_, err := tx.Commit()
				return err
			}); err != nil {
				t.Fatal(err)
			}
			fixture := &bindingMetadataFixture{metadataFixture: newMetadataFixture()}
			service := NewAuthService(AuthOptions{Store: store, Discoverer: NewOAuthDiscoverer(fixture)})
			defer service.Close()
			switch scenario {
			case "network failure":
				fixture.failures["https://resource.example.invalid/.well-known/oauth-protected-resource/mcp"] = &OAuthHTTPError{StatusCode: http.StatusServiceUnavailable}
			case "logout during discovery":
				fixture.beforeRead = func() {
					if _, err := service.Logout(t.Context(), target); err != nil {
						t.Fatal(err)
					}
				}
			}
			lease, err := service.Acquire(t.Context(), target, expected)
			if scenario == "cache" {
				if err != nil || lease.token != "access-before" {
					t.Fatalf("valid token: %v", err)
				}
				calls := len(fixture.calls)
				if _, err := service.Acquire(t.Context(), target, expected); err != nil || len(fixture.calls) != calls || calls == 0 {
					t.Fatalf("metadata cache was not reused: %v", err)
				}
				return
			}
			if err == nil || lease.token != "" {
				t.Fatal("unverified or removed credentials returned")
			}
			file, readErr := store.InspectFile(t.Context(), target)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "network failure" {
				slot := fileSlot(file, target)
				if slot.Status != CredentialReady || slot.Token.AccessToken != "access-before" {
					t.Fatal("metadata network failure destroyed saved credentials")
				}
			}
		})
	}
}
