package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

type oauthMetadataBackfill struct {
	Match         config.OAuthCredentialMatch
	AccountUserID string
	AccountID     string
	Email         string
	Expires       int64
}

func oauthCredentialIdentityFromAccess(cred *config.OAuthCredential) (accountUserID, accountID, accessAccountID string) {
	if cred == nil || cred.Access == "" {
		return "", "", ""
	}
	accessAccountID = config.ExtractOAuthAccountIDFromToken(cred.Access)
	accessUserID := config.ExtractOAuthUserIDFromToken(cred.Access)
	accountID = accessAccountID
	if accountID == "" {
		accountID = cred.AccountID
	}
	accountUserID = config.ExtractOAuthAccountUserIDFromToken(cred.Access)
	if accountID != "" && accountUserID == accessUserID && accessUserID != "" {
		accountUserID = accessUserID + "__" + accountID
	}
	if accountUserID == "" && cred.AccountUserID != "" {
		accountUserID = cred.AccountUserID
	}
	return accountUserID, accountID, accessAccountID
}

func oauthCredentialMapFast(creds []config.ProviderCredential) map[string]llm.OAuthKeySetup {
	result := make(map[string]llm.OAuthKeySetup)
	for credIdx, cred := range creds {
		if cred.OAuth == nil {
			continue
		}

		access := cred.OAuth.Access
		key := access
		refreshSHA256 := ""
		if key == "" {
			if cred.OAuth.Refresh == "" {
				continue
			}
			refreshSHA256 = oauthRefreshStateKey(cred.OAuth.Refresh)
			key = refreshSHA256
		}

		addOAuthKeySetup(result, key, llm.OAuthKeySetup{
			HasKeySlot:            true,
			KeySlot:               credIdx,
			CredentialIndex:       credIdx,
			AccountUserID:         cred.OAuth.AccountUserID,
			AccountID:             cred.OAuth.AccountID,
			Email:                 cred.OAuth.Email,
			Access:                access,
			Expires:               cred.OAuth.Expires,
			RefreshSHA256:         refreshSHA256,
			Status:                cred.OAuth.Status,
			CodexPrimaryResetAt:   cred.OAuth.CodexPrimaryResetAt,
			CodexSecondaryResetAt: cred.OAuth.CodexSecondaryResetAt,
		})
	}
	return result
}

func addOAuthKeySetup(result map[string]llm.OAuthKeySetup, key string, setup llm.OAuthKeySetup) {
	if key == "" {
		return
	}
	result[key] = setup
	if setup.HasKeySlot {
		result[llm.OAuthKeySetupSlotKey(setup.KeySlot, key)] = setup
	}
}

func startOAuthMetadataBackfill(
	ctx context.Context,
	providerCfg *llm.ProviderConfig,
	authPath string,
	auth *config.AuthConfig,
	authMu *sync.Mutex,
	providerName string,
	creds []config.ProviderCredential,
) {
	if providerCfg == nil || len(creds) == 0 {
		return
	}
	credsSnapshot := cloneProviderCredentials(creds)
	go func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		oauthMap, backfills, err := oauthCredentialMapWithOptions(credsSnapshot, false)
		if err != nil {
			log.Warnf("failed to parse OAuth metadata in background provider=%v error=%v", providerName, err)
			return
		}
		providerCfg.UpdateOAuthMetadata(oauthMap)
		providerCfg.WakeCodexRateLimitPolling()
		if len(backfills) == 0 {
			return
		}
		if err := persistOAuthMetadataBackfills(authPath, auth, authMu, providerName, backfills); err != nil {
			log.Warnf("failed to persist backfilled OAuth metadata provider=%v error=%v", providerName, err)
		}
	}()
}

func cloneProviderCredentials(creds []config.ProviderCredential) []config.ProviderCredential {
	if len(creds) == 0 {
		return nil
	}
	cloned := make([]config.ProviderCredential, len(creds))
	for i, cred := range creds {
		cloned[i] = cred
		if cred.OAuth != nil {
			oauth := *cred.OAuth
			cloned[i].OAuth = &oauth
		}
	}
	return cloned
}

func oauthRefreshStateKey(refresh string) string {
	return config.OAuthRefreshStateKey(refresh)
}

func oauthCredentialMap(creds []config.ProviderCredential) (map[string]llm.OAuthKeySetup, []oauthMetadataBackfill, error) {
	return oauthCredentialMapWithOptions(creds, true)
}

func oauthCredentialMapWithOptions(creds []config.ProviderCredential, strict bool) (map[string]llm.OAuthKeySetup, []oauthMetadataBackfill, error) {
	result := make(map[string]llm.OAuthKeySetup)
	var backfills []oauthMetadataBackfill
	for credIdx, cred := range creds {
		if cred.OAuth == nil {
			continue
		}
		access := cred.OAuth.Access
		accountUserID := ""
		accountID := ""
		email := ""
		accessAccountID := ""
		hadExpires := cred.OAuth.Expires != 0
		if access != "" {
			accountUserID, accountID, accessAccountID = oauthCredentialIdentityFromAccess(cred.OAuth)
			if accessAccountID == "" && cred.OAuth.AccountID != "" {
				log.Debugf("OAuth access token at credential %d is missing account_id claim; using configured account_id", credIdx)
			} else if accessAccountID == "" {
				log.Debugf("OAuth access token at credential %d is missing account_id claim; using without workspace identity", credIdx)
			}
			if accountUserID == "" {
				log.Warnf("OAuth access token at credential %d is missing account_user_id claims; skipping this credential", credIdx)
				if !strict {
					addOAuthKeySetup(result, access, llm.OAuthKeySetup{HasKeySlot: true, KeySlot: credIdx, CredentialIndex: credIdx, Access: access, Status: config.OAuthStatusInvalidated})
				}
				continue
			}
			if cred.OAuth.AccountID != "" && accessAccountID != "" && cred.OAuth.AccountID != accessAccountID {
				err := fmt.Errorf("OAuth access token account_id %q does not match configured account_id %q at credential %d", accessAccountID, cred.OAuth.AccountID, credIdx)
				if strict {
					return nil, nil, err
				}
				log.Warnf("skipping OAuth credential provider metadata parse error=%v", err)
				addOAuthKeySetup(result, access, llm.OAuthKeySetup{HasKeySlot: true, KeySlot: credIdx, CredentialIndex: credIdx, Access: access, Status: config.OAuthStatusInvalidated})
				continue
			}
			if cred.OAuth.AccountUserID != "" && cred.OAuth.AccountUserID != accountUserID {
				err := fmt.Errorf("OAuth access token account_user_id %q does not match configured account_user_id %q at credential %d", accountUserID, cred.OAuth.AccountUserID, credIdx)
				if strict {
					return nil, nil, err
				}
				log.Warnf("skipping OAuth credential provider metadata parse error=%v", err)
				addOAuthKeySetup(result, access, llm.OAuthKeySetup{HasKeySlot: true, KeySlot: credIdx, CredentialIndex: credIdx, Access: access, Status: config.OAuthStatusInvalidated})
				continue
			}
			email = config.ExtractOAuthEmailFromToken(access)
			if cred.OAuth.Expires == 0 {
				cred.OAuth.Expires = config.ExtractOAuthExpiresAtFromToken(access)
			}
		} else {
			if cred.OAuth.Refresh == "" {
				continue
			}
			accountUserID = cred.OAuth.AccountUserID
			accountID = cred.OAuth.AccountID
		}
		if email == "" {
			email = cred.OAuth.Email
		}
		needsBackfill := false
		if cred.OAuth.AccountUserID == "" && accountUserID != "" {
			cred.OAuth.AccountUserID = accountUserID
			needsBackfill = true
		}
		if cred.OAuth.AccountID == "" && accountID != "" {
			cred.OAuth.AccountID = accountID
			needsBackfill = true
		}
		if cred.OAuth.Email == "" && email != "" {
			cred.OAuth.Email = email
			needsBackfill = true
		}
		if !hadExpires && cred.OAuth.Expires != 0 {
			needsBackfill = true
		}
		if needsBackfill && accountUserID != "" {
			backfills = append(backfills, oauthMetadataBackfill{
				Match:         config.OAuthCredentialMatch{AccountUserID: accountUserID, Access: access, CredentialIndex: &credIdx},
				AccountUserID: accountUserID,
				AccountID:     accountID,
				Email:         email,
				Expires:       cred.OAuth.Expires,
			})
		}
		key := access
		if key == "" {
			key = oauthRefreshStateKey(cred.OAuth.Refresh)
		}
		refreshSHA256 := ""
		if cred.OAuth.Refresh != "" {
			refreshSHA256 = oauthRefreshStateKey(cred.OAuth.Refresh)
		}
		addOAuthKeySetup(result, key, llm.OAuthKeySetup{
			HasKeySlot:            true,
			KeySlot:               credIdx,
			CredentialIndex:       credIdx,
			AccountUserID:         accountUserID,
			AccountID:             accountID,
			Email:                 email,
			Access:                access,
			RefreshSHA256:         refreshSHA256,
			Expires:               cred.OAuth.Expires,
			Status:                cred.OAuth.Status,
			CodexPrimaryResetAt:   cred.OAuth.CodexPrimaryResetAt,
			CodexSecondaryResetAt: cred.OAuth.CodexSecondaryResetAt,
		})
	}
	return result, backfills, nil
}

func persistOAuthMetadataBackfills(
	authPath string,
	auth *config.AuthConfig,
	authMu *sync.Mutex,
	provider string,
	backfills []oauthMetadataBackfill,
) error {
	updates := make([]config.OAuthCredentialMetadataUpdate, 0, len(backfills))
	for _, backfill := range backfills {
		if backfill.AccountUserID == "" && backfill.AccountID == "" && backfill.Email == "" && backfill.Expires == 0 {
			continue
		}
		updates = append(updates, config.OAuthCredentialMetadataUpdate{
			Match:         backfill.Match,
			AccountUserID: backfill.AccountUserID,
			AccountID:     backfill.AccountID,
			Email:         backfill.Email,
			Expires:       backfill.Expires,
		})
	}
	if len(updates) == 0 {
		return nil
	}
	updatedAuth, changed, err := config.UpdateOAuthCredentialMetadataInFile(authPath, provider, updates)
	if err != nil {
		return err
	}
	if changed > 0 {
		authMu.Lock()
		*auth = updatedAuth
		authMu.Unlock()
	}
	return nil
}

func resolveProviderOAuthSettings(
	cfg config.ProviderConfig,
	_ []config.ProviderCredential,
) (tokenURL, clientID string, ok bool, err error) {
	normalized, meta, normalizeErr := config.NormalizeProviderPreset(cfg)
	if normalizeErr != nil {
		return "", "", false, normalizeErr
	}
	if !meta.Enabled || normalized.TokenURL == "" {
		return "", "", false, nil
	}
	return normalized.TokenURL, normalized.ClientID, true, nil
}
