package mcp

import (
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/keakon/chord/internal/atomicfile"
	"github.com/keakon/chord/internal/config"
)

func credentialBindingID(targetID, profileID string, b CredentialBinding) string {
	return identityHash(struct{ TargetID, ProfileID, Resource, Issuer, ClientID, TokenAuthMethod, RedirectURI string }{targetID, profileID, b.Resource, b.Issuer, b.ClientID, b.TokenAuthMethod, b.RedirectURI})
}

func validTokenAuthMethod(method string) bool {
	return method == config.MCPOAuthAuthMethodNone || method == "client_secret_basic" || method == "client_secret_post"
}

func validStoreTime(t time.Time) bool { _, offset := t.Zone(); return t.IsZero() || offset == 0 }

func validateCredentialFile(t Target, f *CredentialFile) error {
	bad := func() error { return storeFailure(ErrorCredentialStoreCorrupt, atomicfile.NotReplaced) }
	if f == nil || f.SchemaVersion != 1 || f.Slots == nil || len(f.Slots) > 128 || f.Target != (CredentialTarget{t.Ref.WorkspaceID, t.Ref.Owner, t.Config.URL}) || t.TargetID != identityHash(struct{ Workspace, Owner, Endpoint string }{f.Target.WorkspaceID, f.Target.Owner, f.Target.Endpoint}) {
		return bad()
	}
	if endpoint, err := CanonicalEndpoint(f.Target.Endpoint); err != nil || endpoint != f.Target.Endpoint {
		return bad()
	}
	for id, slot := range f.Slots {
		if slot == nil || slot.AuthGeneration == 0 || id != identityHash(slot.Profile) || !validTokenAuthMethod(slot.Profile.TokenAuthMethod) {
			return bad()
		}
		p := slot.Profile
		if (p.RegistrationPolicy != "automatic" && p.RegistrationPolicy != "pre_registered") || (p.RegistrationPolicy == "pre_registered") != (p.ConfiguredClientID != "") {
			return bad()
		}
		redirect, err := url.Parse(p.RedirectURI)
		if err != nil || redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Port() == "" || redirect.Path != "/oauth/callback" || redirect.RawQuery != "" || redirect.Fragment != "" || redirect.User != nil {
			return bad()
		}
		if id == t.ProfileID && p != profileForTarget(t) {
			return bad()
		}
		switch slot.Status {
		case CredentialEmpty, CredentialLoggedOut:
			if slot.Token != nil || slot.TokenRevision != 0 || slot.RefreshInflight != nil || slot.RefreshBlocked != nil || slot.RejectedRevision != 0 || slot.Binding != nil {
				return bad()
			}
		case CredentialReady:
			if slot.Token == nil || slot.TokenRevision == 0 || slot.Binding == nil {
				return bad()
			}
		case CredentialReauthRequired, CredentialRefreshOutcomeUnknown:
		default:
			return bad()
		}
		if b := slot.Binding; b != nil {
			if b.BindingID != credentialBindingID(t.TargetID, id, *b) || b.ClientID == "" || b.Issuer == "" || !validTokenAuthMethod(b.TokenAuthMethod) || b.TokenAuthMethod != p.TokenAuthMethod || b.RedirectURI != p.RedirectURI || (p.IssuerHint != "" && p.IssuerHint != b.Issuer) || (p.ConfiguredClientID != "" && p.ConfiguredClientID != b.ClientID) {
				return bad()
			}
			if err := ValidateResourceBinding(t.Config.URL, b.Resource); err != nil {
				return bad()
			}
			if issuer, err := CanonicalEndpoint(b.Issuer); err != nil || issuer == "" {
				return bad()
			}
		}
		for _, registration := range []*CredentialRegistration{slot.Registration, slot.PendingRegistration} {
			if registration != nil && (registration.ClientID == "" || registration.Issuer == "" || registration.RedirectURI != p.RedirectURI || registration.TokenAuthMethod != p.TokenAuthMethod || !validStoreTime(registration.SecretExpiresAt)) {
				return bad()
			}
		}
		if pending := slot.PendingRegistration; pending != nil && pending.AuthGeneration != slot.AuthGeneration {
			return bad()
		}
		if registration := slot.Registration; registration != nil && registration.AuthGeneration != 0 {
			return bad()
		}
		if token := slot.Token; token != nil {
			if token.AccessToken == "" || token.IssuedAt.IsZero() || slot.TokenRevision == 0 || slot.Binding == nil || token.LifetimeSeconds < 0 || token.LifetimeSeconds > math.MaxInt64/int64(time.Second) || !validStoreTime(token.IssuedAt) || !validStoreTime(token.ExpiresAt) || (!token.ExpiresAt.IsZero() && token.ExpiresAt.Before(token.IssuedAt)) {
				return bad()
			}
		}
		if slot.RejectedRevision > slot.TokenRevision {
			return bad()
		}
		if marker := slot.RefreshInflight; marker != nil && (marker.OperationID == "" || marker.AuthGeneration != slot.AuthGeneration || marker.TokenRevision != slot.TokenRevision || marker.StartedAt.IsZero() || !validStoreTime(marker.StartedAt) || slot.Binding == nil || slot.Token == nil) {
			return bad()
		}
		if blocked := slot.RefreshBlocked; blocked != nil && (blocked.BlockID == "" || blocked.AuthGeneration != slot.AuthGeneration || blocked.TokenRevision != slot.TokenRevision || slot.Binding == nil || blocked.BindingID != slot.Binding.BindingID || blocked.BlockedAt.IsZero() || !validStoreTime(blocked.BlockedAt)) {
			return bad()
		}
		if len(slot.ScopeRequirements) > 32 {
			return bad()
		}
		bytes := 0
		for _, requirement := range slot.ScopeRequirements {
			if len(requirement.Scopes) > 64 || slot.Binding == nil || requirement.BindingID != slot.Binding.BindingID || requirement.AuthGeneration != slot.AuthGeneration || (requirement.TokenRevision == 0 || requirement.TokenRevision > slot.TokenRevision) || !validStoreTime(requirement.ObservedAt) {
				return bad()
			}
			for _, scope := range requirement.Scopes {
				bytes += len(scope)
				if strings.ContainsAny(scope, "\r\n\x00") {
					return bad()
				}
			}
		}
		if bytes > 8<<10 {
			return bad()
		}
	}
	return nil
}
