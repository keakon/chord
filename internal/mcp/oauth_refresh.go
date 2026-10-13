package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
)

func (s *AuthService) refresh(ctx context.Context, t Target, expected ExpectedIdentity, observed CredentialVersion) error {
	file, err := s.store.ReadForUse(ctx, t)
	if err != nil {
		return err
	}
	old := fileSlot(file, t)
	if !identityMatches(old, expected) {
		return authError(ErrorIdentityChanged)
	}
	for {
		discovery, err := s.discoverSavedBinding(ctx, t, expected, old.Binding.Issuer)
		if err != nil {
			return err
		}
		metadataExpired := false
		err = s.store.UpdateLocked(ctx, t, func(tx *CredentialTransaction) error {
			slot := fileSlot(tx.File, t)
			if !identityMatches(slot, expected) {
				return authError(ErrorIdentityChanged)
			}
			if slot.RefreshInflight != nil || slot.Status == CredentialRefreshOutcomeUnknown {
				return markUnknownRefresh(tx, slot)
			}
			if slot.Token == nil || slot.Status != CredentialReady {
				return authError(ErrorAuthRequired)
			}
			if slot.RefreshBlocked != nil {
				return authError(ErrorRefreshBlocked)
			}
			if !versionMatches(slot, observed) && !needsRefresh(slot, s.now()) {
				return nil
			}
			if !s.discoverer.valid(discovery) {
				metadataExpired = true
				return nil
			}
			if discovery.Resource != slot.Binding.Resource || discovery.Issuer != slot.Binding.Issuer {
				slot.Status = CredentialReauthRequired
				_, err := tx.Commit()
				if err != nil {
					return err
				}
				return authError(ErrorIdentityChanged)
			}
			if slot.Token.RefreshToken == "" {
				slot.Status = CredentialReauthRequired
				_, err := tx.Commit()
				if err != nil {
					return err
				}
				return authError(ErrorRefreshInvalid)
			}
			if slot.TokenRevision == math.MaxUint64 {
				return authError(ErrorCredentialStoreFailed)
			}
			values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {slot.Token.RefreshToken}, "resource": {slot.Binding.Resource}, "client_id": {slot.Binding.ClientID}}
			// Environment failures occur before the marker and before any secret is sent.
			secret, err := clientSecret(t, slot.Registration, slot.Binding.TokenAuthMethod, s.now())
			if err != nil {
				return err
			}
			opID, err := s.randomID()
			if err != nil {
				return err
			}
			slot.RefreshInflight = &CredentialRefreshInflight{OperationID: opID, AuthGeneration: slot.AuthGeneration, TokenRevision: slot.TokenRevision, StartedAt: s.now().UTC()}
			if _, err := tx.Commit(); err != nil {
				return err
			}
			raw, err := s.tokenRequest(ctx, discovery.AuthorizationServer.TokenEndpoint, values, slot.Binding.TokenAuthMethod, slot.Binding.ClientID, secret)
			if err != nil {
				return s.finishRefreshFailure(tx, slot, err)
			}
			response, err := parseTokenResponse(raw, slot.Token.GrantedScopes)
			if err != nil {
				slot.Status = CredentialRefreshOutcomeUnknown
				if _, saveErr := tx.Commit(); saveErr != nil {
					return saveErr
				}
				return authError(ErrorRefreshOutcomeUnknown)
			}
			token := s.savedToken(response, slot.Token.RequestedScopes)
			if response.refreshToken == "" {
				token.RefreshToken = slot.Token.RefreshToken
			}
			slot.Token = token
			slot.TokenRevision++
			slot.RejectedRevision = 0
			slot.RefreshInflight = nil
			slot.RefreshBlocked = nil
			slot.Status = CredentialReady
			_, err = tx.Commit()
			return err
		})
		if !metadataExpired || err != nil {
			return err
		}
	}
}

func markUnknownRefresh(tx *CredentialTransaction, slot *CredentialSlot) error {
	if slot.RefreshInflight != nil && slot.Status != CredentialRefreshOutcomeUnknown {
		slot.Status = CredentialRefreshOutcomeUnknown
		if _, err := tx.Commit(); err != nil {
			return err
		}
	}
	return authError(ErrorRefreshOutcomeUnknown)
}

func (s *AuthService) finishRefreshFailure(tx *CredentialTransaction, slot *CredentialSlot, cause error) error {
	e, ok := errors.AsType[*OAuthHTTPError](cause)
	if ok && e.SendEvidence == SendNotSent {
		slot.RefreshInflight = nil
		if _, err := tx.Commit(); err != nil {
			return err
		}
		return cause
	}
	if ok && (e.StatusCode == 400 || e.StatusCode == 401) {
		if e.OAuthError == "invalid_grant" {
			slot.Token = nil
			slot.RejectedRevision = 0
			slot.RefreshInflight = nil
			slot.Status = CredentialReauthRequired
			if _, err := tx.Commit(); err != nil {
				return err
			}
			return authError(ErrorRefreshInvalid)
		}
		if slices.Contains([]string{"invalid_client", "invalid_scope", "unauthorized_client", "unsupported_grant_type"}, e.OAuthError) {
			blockID, err := s.randomID()
			if err != nil {
				return err
			}
			slot.RefreshBlocked = &CredentialRefreshBlocked{BlockID: blockID, BindingID: slot.Binding.BindingID, AuthGeneration: slot.AuthGeneration, TokenRevision: slot.TokenRevision, OAuthError: e.OAuthError, ErrorClass: ErrorRefreshBlocked, BlockedAt: s.now().UTC()}
			slot.RefreshInflight = nil
			if _, err := tx.Commit(); err != nil {
				return err
			}
			return authError(ErrorRefreshBlocked)
		}
	}
	slot.Status = CredentialRefreshOutcomeUnknown
	if _, err := tx.Commit(); err != nil {
		return err
	}
	return authError(ErrorRefreshOutcomeUnknown)
}

func clientSecret(t Target, registration *CredentialRegistration, method string, now time.Time) (string, error) {
	if method == config.MCPOAuthAuthMethodNone {
		return "", nil
	}
	if t.Config.OAuth != nil && t.Config.OAuth.ClientSecretEnv != "" {
		secret := os.Getenv(t.Config.OAuth.ClientSecretEnv)
		if secret == "" {
			return "", authError(ErrorRefreshBlocked)
		}
		return secret, nil
	}
	if registration != nil && registration.ClientSecret != "" && (registration.SecretExpiresAt.IsZero() || now.Before(registration.SecretExpiresAt)) {
		return registration.ClientSecret, nil
	}
	return "", authError(ErrorRefreshBlocked)
}
func (s *AuthService) tokenRequest(ctx context.Context, endpoint string, values url.Values, method, clientID, secret string) ([]byte, error) {
	var raw json.RawMessage
	var err error
	switch method {
	case config.MCPOAuthAuthMethodNone:
		err = s.http.PostForm(ctx, endpoint, values, &raw)
	case config.MCPOAuthAuthMethodClientSecretPost:
		values.Set("client_secret", secret)
		err = s.http.PostForm(ctx, endpoint, values, &raw)
	case config.MCPOAuthAuthMethodClientSecretBasic:
		err = s.http.PostFormWithBasic(ctx, endpoint, values, clientID, secret, &raw)
	default:
		return nil, authError(ErrorProtocol)
	}
	return raw, err
}
func (s *AuthService) savedToken(response tokenResponse, requested []string) *CredentialToken {
	now := s.now().UTC()
	token := &CredentialToken{AccessToken: response.accessToken, RefreshToken: response.refreshToken, IssuedAt: now, LifetimeSeconds: response.expiresIn, RequestedScopes: slices.Clone(requested), GrantedScopes: slices.Clone(response.grantedScopes)}
	if response.expiresIn > 0 {
		token.ExpiresAt = now.Add(time.Duration(response.expiresIn) * time.Second)
	}
	return token
}

func unionScopes(groups ...[]string) []string {
	var out []string
	for _, group := range groups {
		out = append(out, group...)
	}
	slices.Sort(out)
	out = slices.Compact(out)
	return slices.DeleteFunc(out, func(scope string) bool { return strings.TrimSpace(scope) == "" })
}
