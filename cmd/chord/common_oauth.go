package main

import (
	"sync"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

type oauthMetadataBackfill struct {
	AccountID string
	Email     string
}

func oauthCredentialMap(creds []config.ProviderCredential) (map[string]llm.OAuthKeySetup, []oauthMetadataBackfill) {
	result := make(map[string]llm.OAuthKeySetup)
	var backfills []oauthMetadataBackfill
	for credIdx, cred := range creds {
		if cred.OAuth == nil || cred.OAuth.Access == "" {
			continue
		}
		accountID := cred.OAuth.AccountID
		if accountID == "" {
			accountID = config.ExtractOAuthAccountIDFromToken(cred.OAuth.Access)
		}
		email := cred.OAuth.Email
		if email == "" {
			email = config.ExtractOAuthEmailFromToken(cred.OAuth.Access)
		}
		// Write back parsed fields to the credential so they get persisted on next save.
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
				AccountID: accountID,
				Email:     email,
			})
		}
		result[cred.OAuth.Access] = llm.OAuthKeySetup{
			CredentialIndex: credIdx,
			AccountID:       accountID,
			Email:           email,
			Expires:         cred.OAuth.Expires,
			Status:          cred.OAuth.Status,
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
		match := config.OAuthCredentialMatch{AccountID: backfill.AccountID}
		updatedAuth, _, changed, err := config.UpdateOAuthCredentialInFile(authPath, provider, match, func(cred *config.OAuthCredential) (bool, error) {
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
