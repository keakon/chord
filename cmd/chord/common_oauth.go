package main

import (
	"fmt"
	"sync"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

type oauthMetadataBackfill struct {
	Match     config.OAuthCredentialMatch
	AccountID string
	Email     string
}

func oauthCredentialMap(creds []config.ProviderCredential) (map[string]llm.OAuthKeySetup, []oauthMetadataBackfill) {
	result := make(map[string]llm.OAuthKeySetup)
	var backfills []oauthMetadataBackfill
	keySlot := 0
	for credIdx, cred := range creds {
		if cred.OAuth == nil {
			if cred.APIKey != "" || cred.ExplicitEmpty {
				keySlot++
			}
			continue
		}
		access := cred.OAuth.Access
		accountID := ""
		email := ""
		if access != "" {
			accountID = config.ExtractOAuthAccountIDFromToken(access)
			if accountID == "" || (cred.OAuth.AccountID != "" && cred.OAuth.AccountID != accountID) {
				continue
			}
			email = config.ExtractOAuthEmailFromToken(access)
			if cred.OAuth.Expires == 0 {
				cred.OAuth.Expires = config.ExtractOAuthExpiresAtFromToken(access)
			}
		} else if cred.OAuth.Refresh == "" {
			continue
		}
		if accountID == "" {
			accountID = cred.OAuth.AccountID
		}
		if email == "" {
			email = cred.OAuth.Email
		}
		needsBackfill := false
		if cred.OAuth.AccountID == "" && accountID != "" {
			cred.OAuth.AccountID = accountID
			needsBackfill = true
		}
		if cred.OAuth.Email == "" && email != "" {
			cred.OAuth.Email = email
			needsBackfill = true
		}
		if needsBackfill && accountID != "" {
			backfills = append(backfills, oauthMetadataBackfill{
				Match:     config.OAuthCredentialMatch{AccountID: accountID, Access: access, CredentialIndex: &credIdx},
				AccountID: accountID,
				Email:     email,
			})
		}
		key := access
		if key == "" {
			key = fmt.Sprintf("key_slot:%d", keySlot)
		}
		keySlot++
		result[key] = llm.OAuthKeySetup{
			CredentialIndex:       credIdx,
			AccountID:             accountID,
			Email:                 email,
			Access:                access,
			Expires:               cred.OAuth.Expires,
			Status:                cred.OAuth.Status,
			CodexPrimaryResetAt:   cred.OAuth.CodexPrimaryResetAt,
			CodexSecondaryResetAt: cred.OAuth.CodexSecondaryResetAt,
		}
	}
	return result, backfills
}

func persistOAuthMetadataBackfills(
	authPath string,
	auth *config.AuthConfig,
	authMu *sync.Mutex,
	provider string,
	backfills []oauthMetadataBackfill,
) error {
	for _, backfill := range backfills {
		if backfill.AccountID == "" {
			continue
		}
		updatedAuth, _, changed, err := config.UpdateOAuthCredentialInFile(authPath, provider, backfill.Match, func(cred *config.OAuthCredential) (bool, error) {
			dirty := false
			if cred.AccountID == "" && backfill.AccountID != "" {
				cred.AccountID = backfill.AccountID
				dirty = true
			}
			if cred.Email == "" && backfill.Email != "" {
				cred.Email = backfill.Email
				dirty = true
			}
			return dirty, nil
		})
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
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
	normalized, meta, normalizeErr := config.NormalizeOpenAICodexProvider(cfg, false)
	if normalizeErr != nil {
		return "", "", false, normalizeErr
	}
	if !meta.Enabled || normalized.TokenURL == "" {
		return "", "", false, nil
	}
	return normalized.TokenURL, normalized.ClientID, true, nil
}
