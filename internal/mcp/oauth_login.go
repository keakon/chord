package mcp

import (
	"context"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type LoginOptions struct{ Reauth bool }

// LoginProgress is ephemeral local UI data. AuthorizationURL must never be
// recorded in transcripts, logs, request dumps, or headless status events.
type LoginProgress struct {
	State            OperationState
	AuthorizationURL string
}
type LoginResult struct {
	CredentialSaved bool
	Version         CredentialVersion
	Validation      ProbeResult
}

// Login runs the full OAuth 2.0 authorization-code flow with PKCE for one
// target:
//
//  0. Shortcut (no Reauth): probe the existing credential; a ready or
//     refreshable token is kept when valid and no extra scopes are required,
//     so a routine login is a no-op unless the token is broken.
//  1. Setup: discover the authorization-server metadata, generate the PKCE
//     pair, and start the local callback listener before any user round-trip.
//  2. Client registration: reuse a registration whose issuer, redirect URI
//     and secret still match the discovered metadata; otherwise register and
//     persist it — as the live registration when the slot has no token yet,
//     or as a pending registration that only replaces the old one at commit
//     time, so a half-finished login cannot break a working credential.
//  3. Authorization: build the authorization URL (scopes = configured ∪
//     previously requested ∪ server-advertised ∪ pending scope requirements,
//     plus offline_access when supported) and wait for the browser callback.
//  4. Exchange: swap the authorization code for tokens.
//  5. Commit: replace the slot atomically. The auth-generation check aborts
//     the commit when another flow re-bound the slot meanwhile; the in-service
//     login record is re-validated so a closed service or a superseded login
//     cannot land tokens.
//  6. Validate: probe the saved credential before reporting success.
func (s *AuthService) Login(ctx context.Context, t Target, options LoginOptions, progress func(LoginProgress)) (result LoginResult, err error) {
	if !managedAuth(t) {
		return result, authError(ErrorAuthRequired)
	}
	key := targetKey(t)
	opCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return result, context.Canceled
	}
	if s.logins[key] != nil {
		s.mu.Unlock()
		return result, authError(ErrorProtocol)
	}
	op := &loginOperation{cancel: cancel, target: t}
	s.logins[key] = op
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.logins, key); s.mu.Unlock(); s.wg.Done() }()
	emit := func(state OperationState, link string) {
		if progress != nil {
			progress(LoginProgress{State: state, AuthorizationURL: link})
		}
	}
	if !options.Reauth {
		view, inspectErr := s.Inspect(opCtx, t)
		if inspectErr != nil {
			return result, inspectErr
		}
		if view.Blocked != nil {
			if _, err := s.RetryBlockedRefresh(opCtx, t, *view.Blocked); err != nil {
				return result, err
			}
		}
		if view.Auth == AuthReady || view.Auth == AuthRefreshRequired || (view.Blocked != nil && view.Version.BindingID != "") {
			emit(OperationValidating, "")
			result.Validation = s.Probe(opCtx, t, ProbeOptions{UseSavedCredential: true, AllowRefresh: true})
			if result.Validation.OK && len(view.ScopeRequirements) == 0 {
				return result, nil
			}
			if !result.Validation.OK && result.Validation.Diagnostic.ErrorClass != ErrorAuthRequired && result.Validation.Diagnostic.ErrorClass != ErrorInvalidToken && result.Validation.Diagnostic.ErrorClass != ErrorRefreshInvalid && result.Validation.Diagnostic.ErrorClass != ErrorInsufficientScope {
				return result, authError(result.Validation.Diagnostic.ErrorClass)
			}
		}
	}
	var generation uint64
	var previous *CredentialSlot
	err = s.store.UpdateLocked(opCtx, t, func(tx *CredentialTransaction) error {
		slot := fileSlot(tx.File, t)
		if slot == nil {
			slot = &CredentialSlot{Profile: profileForTarget(t), AuthGeneration: 1, Status: CredentialEmpty}
			tx.File.Slots[t.ProfileID] = slot
			if _, err := tx.Commit(); err != nil {
				return err
			}
		}
		generation = slot.AuthGeneration
		previous = slot
		return nil
	})
	if err != nil {
		return result, err
	}
	setupCtx, setupCancel := context.WithTimeout(opCtx, 60*time.Second)
	defer setupCancel()
	emit(OperationDiscovering, "")
	discoveryOpts := DiscoveryOptions{}
	if previous.Binding != nil {
		discoveryOpts.VerifiedIssuer = previous.Binding.Issuer
	}
	metadata, err := s.discoverer.Discover(setupCtx, t, discoveryOpts)
	if err != nil {
		return result, err
	}
	s.randomMu.Lock()
	state, verifier, challenge, err := newPKCE(s.random)
	s.randomMu.Unlock()
	if err != nil {
		return result, err
	}
	profile := profileForTarget(t)
	redirect, _ := url.Parse(profile.RedirectURI)
	port, _ := strconv.Atoi(redirect.Port())
	listener, err := listenCallback(opCtx, port, state, metadata.Issuer, metadata.AuthorizationServer.AuthorizationResponseIssParameterSupported)
	if err != nil {
		return result, authError(ErrorCallbackUnavailable)
	}
	defer listener.Close()
	emit(OperationRegistering, "")
	// A pending registration from an earlier unfinished login only stays
	// valid for the same issuer and the slot generation it was created under;
	// anything else falls back to the committed registration.
	registration := previous.PendingRegistration
	if registration == nil || registration.Issuer != metadata.Issuer || registration.AuthGeneration != generation {
		registration = previous.Registration
	}
	if registration == nil || registration.Issuer != metadata.Issuer || registration.RedirectURI != profile.RedirectURI || (!registration.SecretExpiresAt.IsZero() && !s.now().Before(registration.SecretExpiresAt)) {
		created, err := NewOAuthRegistrar(s.http).Register(setupCtx, t, metadata)
		if err != nil {
			return result, err
		}
		registration = &created
		err = s.store.UpdateLocked(setupCtx, t, func(tx *CredentialTransaction) error {
			slot := fileSlot(tx.File, t)
			if slot == nil || slot.AuthGeneration != generation {
				return authError(ErrorIdentityChanged)
			}
			if err := opCtx.Err(); err != nil {
				return err
			}
			if slot.Token == nil {
				// No working token: the new registration can take over live.
				registration.AuthGeneration = 0
				slot.Registration = registration
			} else {
				// A working token exists: stage the registration as pending so
				// the old one keeps serving refreshes until the new tokens
				// commit; the commit path swaps it in.
				registration.AuthGeneration = generation
				slot.PendingRegistration = registration
			}
			_, err := tx.Commit()
			return err
		})
		if err != nil {
			return result, err
		}
	}
	var configured, requested []string
	if t.Config.OAuth != nil {
		configured = t.Config.OAuth.Scopes
	}
	if previous.Token != nil {
		requested = previous.Token.RequestedScopes
	}
	scopes := unionScopes(configured, requested, metadata.Scopes)
	for _, requirement := range previous.ScopeRequirements {
		scopes = unionScopes(scopes, requirement.Scopes)
	}
	if slices.Contains(metadata.AuthorizationServer.ScopesSupported, "offline_access") {
		scopes = unionScopes(scopes, []string{"offline_access"})
	}
	link, err := url.Parse(metadata.AuthorizationServer.AuthorizationEndpoint)
	if err != nil {
		return result, authError(ErrorProtocol)
	}
	query := link.Query()
	query.Set("response_type", "code")
	query.Set("client_id", registration.ClientID)
	query.Set("redirect_uri", registration.RedirectURI)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("resource", metadata.Resource)
	if len(scopes) > 0 {
		query.Set("scope", strings.Join(scopes, " "))
	}
	link.RawQuery = query.Encode()
	if err := setupCtx.Err(); err != nil {
		return result, err
	}
	setupCancel()
	waitCtx, waitCancel := context.WithTimeout(opCtx, 5*time.Minute)
	defer waitCancel()
	emit(OperationAwaitingCallback, link.String())
	callback, err := listener.Wait(waitCtx)
	if err != nil {
		return result, err
	}
	if callback.denied {
		return result, authError(ErrorAuthorizationDenied)
	}
	finishCtx, finishCancel := context.WithTimeout(opCtx, 60*time.Second)
	defer finishCancel()
	if err := finishCtx.Err(); err != nil {
		return result, err
	}
	emit(OperationExchanging, "")
	secret, err := clientSecret(t, registration, registration.TokenAuthMethod, s.now())
	if err != nil {
		return result, err
	}
	values := url.Values{"grant_type": {"authorization_code"}, "code": {callback.code}, "redirect_uri": {registration.RedirectURI}, "code_verifier": {verifier}, "client_id": {registration.ClientID}, "resource": {metadata.Resource}}
	raw, err := s.tokenRequest(finishCtx, metadata.AuthorizationServer.TokenEndpoint, values, registration.TokenAuthMethod, registration.ClientID, secret)
	if err != nil {
		return result, err
	}
	response, err := parseTokenResponse(raw, scopes)
	if err != nil {
		return result, err
	}
	emit(OperationCommitting, "")
	err = s.store.UpdateLocked(finishCtx, t, func(tx *CredentialTransaction) error {
		slot := fileSlot(tx.File, t)
		// The generation check aborts when another flow re-bound the slot
		// while the user was away at the authorization server.
		if slot == nil || slot.AuthGeneration != generation || generation == math.MaxUint64 {
			return authError(ErrorIdentityChanged)
		}
		// Only a still-registered, still-running login whose target has not
		// been reconfigured may commit; WithoutCancel lets the store write
		// finish even if the caller cancels mid-commit.
		s.mu.Lock()
		valid := s.logins[key] == op && opCtx.Err() == nil && !s.closed && s.targetCurrentLocked(t)
		if valid {
			op.committing = true
			tx.ctx = context.WithoutCancel(finishCtx)
		}
		s.mu.Unlock()
		if !valid {
			return context.Canceled
		}
		binding := &CredentialBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: registration.ClientID, TokenAuthMethod: registration.TokenAuthMethod, RedirectURI: registration.RedirectURI}
		binding.BindingID = credentialBindingID(t.TargetID, t.ProfileID, *binding)
		savedRegistration := *registration
		savedRegistration.AuthGeneration = 0
		slot = &CredentialSlot{Profile: profile, AuthGeneration: generation + 1, TokenRevision: 1, Status: CredentialReady, Binding: binding, Registration: &savedRegistration, Token: s.savedToken(response, scopes)}
		tx.File.Slots[t.ProfileID] = slot
		if _, err := tx.Commit(); err != nil {
			return err
		}
		result.CredentialSaved = true
		result.Version = slotVersion(slot)
		return nil
	})
	if err != nil {
		return result, err
	}
	emit(OperationValidating, "")
	result.Validation = s.Probe(finishCtx, t, ProbeOptions{UseSavedCredential: true, AllowRefresh: true})
	if !result.Validation.OK {
		return result, authError(result.Validation.Diagnostic.ErrorClass)
	}
	return result, nil
}
