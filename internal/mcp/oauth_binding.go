package mcp

import (
	"context"
	"time"
)

// discoverSavedBinding performs public metadata requests outside the credential
// lock. Changed identity evidence is committed only for the observed account.
func (s *AuthService) discoverSavedBinding(ctx context.Context, t Target, expected ExpectedIdentity, issuer string) (OAuthDiscovery, error) {
	metadata, err := s.discoverer.Discover(ctx, t, DiscoveryOptions{VerifiedIssuer: issuer})
	if err == nil {
		return metadata, nil
	}
	class := diagnoseAuth(err).ErrorClass
	if class != ErrorResourceMismatch && class != ErrorIssuerMismatch {
		return OAuthDiscovery{}, err
	}
	if saveErr := s.store.UpdateLocked(ctx, t, func(tx *CredentialTransaction) error {
		slot := fileSlot(tx.File, t)
		if !identityMatches(slot, expected) {
			return authError(ErrorIdentityChanged)
		}
		slot.Status = CredentialReauthRequired
		_, saveErr := tx.Commit()
		return saveErr
	}); saveErr != nil {
		return OAuthDiscovery{}, saveErr
	}
	return OAuthDiscovery{}, err
}

// acquireVerifiedLease rechecks credentials after discovery: a concurrent
// logout, rejection, or refresh must not be hidden by the earlier snapshot.
func (s *AuthService) acquireVerifiedLease(ctx context.Context, t Target, expected ExpectedIdentity, issuer string) (CredentialLease, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		metadata, err := s.discoverSavedBinding(ctx, t, expected, issuer)
		if err != nil {
			return CredentialLease{}, err
		}
		var lease CredentialLease
		expired := false
		err = s.store.UpdateLocked(ctx, t, func(tx *CredentialTransaction) error {
			slot := fileSlot(tx.File, t)
			if !identityMatches(slot, expected) {
				return authError(ErrorIdentityChanged)
			}
			if !s.discoverer.valid(metadata) {
				expired = true
				return nil
			}
			if metadata.Resource != slot.Binding.Resource || metadata.Issuer != slot.Binding.Issuer {
				class := ErrorIssuerMismatch
				if metadata.Resource != slot.Binding.Resource {
					class = ErrorResourceMismatch
				}
				slot.Status = CredentialReauthRequired
				if _, err := tx.Commit(); err != nil {
					return err
				}
				return authError(class)
			}
			if slot.RefreshInflight != nil || slot.Status == CredentialRefreshOutcomeUnknown {
				return markUnknownRefresh(tx, slot)
			}
			if slot.Token == nil || slot.Status != CredentialReady {
				return authError(ErrorAuthRequired)
			}
			if slot.RejectedRevision == slot.TokenRevision || (!slot.Token.ExpiresAt.IsZero() && !s.now().Before(slot.Token.ExpiresAt)) {
				if slot.RefreshBlocked != nil {
					return authError(ErrorRefreshBlocked)
				}
				return authError(ErrorInvalidToken)
			}
			lease = CredentialLease{token: slot.Token.AccessToken, version: slotVersion(slot)}
			return nil
		})
		if err != nil || !expired {
			return lease, err
		}
	}
}
