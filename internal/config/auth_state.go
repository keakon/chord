package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// OAuthStateKey identifies a persisted OAuth runtime state entry.
// AccountID is required for auth.state.yaml records; Email and Access are only
// carried for auth.yaml credential matching paths.
type OAuthStateKey struct {
	Provider  string
	AccountID string
	Email     string
	Access    string
}

// OAuthStateRecord stores dynamic OAuth runtime state shared across processes.
type OAuthStateRecord struct {
	AccountID               string                `yaml:"account_id,omitempty"`
	Email                   string                `yaml:"email,omitempty"`
	Access                  string                `yaml:"access,omitempty"`
	Expires                 int64                 `yaml:"expires,omitempty"`
	Status                  OAuthCredentialStatus `yaml:"status,omitempty"`
	UpdatedAt               int64                 `yaml:"updated_at,omitempty"`
	LastWarmupAt            int64                 `yaml:"last_warmup_at,omitempty"`
	CodexPrimaryUsedPct     float64               `yaml:"codex_primary_used_pct,omitempty"`
	CodexPrimaryWindowMin   int64                 `yaml:"codex_primary_window_minutes,omitempty"`
	CodexPrimaryResetAt     int64                 `yaml:"codex_primary_reset_at,omitempty"`
	CodexSecondaryUsedPct   float64               `yaml:"codex_secondary_used_pct,omitempty"`
	CodexSecondaryWindowMin int64                 `yaml:"codex_secondary_window_minutes,omitempty"`
	CodexSecondaryResetAt   int64                 `yaml:"codex_secondary_reset_at,omitempty"`
	CodexHasCredits         *bool                 `yaml:"codex_has_credits,omitempty"`
	CodexUnlimited          *bool                 `yaml:"codex_unlimited,omitempty"`
	CodexBalance            string                `yaml:"codex_balance,omitempty"`
}

// IsValid reports whether the runtime status is usable.
func (r OAuthStateRecord) IsValid() bool {
	return r.Status.IsValid()
}

// AuthStateFile is the on-disk shared runtime state keyed by provider then unique id.
type AuthStateFile map[string]map[string]OAuthStateRecord

func AuthStatePath() (string, error) {
	h, err := ConfigHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "auth.state.yaml"), nil
}

func OAuthStateRecordKey(key OAuthStateKey) string {
	provider := strings.TrimSpace(key.Provider)
	accountID := strings.TrimSpace(key.AccountID)
	if accountID == "" {
		return ""
	}
	if provider == "" {
		return "account_id:" + accountID
	}
	return provider + ":account_id:" + accountID
}

func LoadAuthState(path string) (AuthStateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(AuthStateFile), nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return make(AuthStateFile), nil
	}
	var raw AuthStateFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return normalizeAuthStateFile(raw), nil
}

func SaveAuthState(path string, state AuthStateFile) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("auth state path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create auth state dir: %w", err)
	}
	data, err := yaml.Marshal(normalizeAuthStateFile(state))
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func UpdateAuthStateFile(path string, mutate func(AuthStateFile) (bool, error)) (AuthStateFile, bool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, false, fmt.Errorf("auth state path is empty")
	}
	if mutate == nil {
		return nil, false, fmt.Errorf("auth state mutate func is nil")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("create auth state dir: %w", err)
	}
	lock, err := lockAuthYAMLFile(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = lock.Close() }()

	state, err := LoadAuthState(path)
	if err != nil {
		return nil, false, fmt.Errorf("load auth state: %w", err)
	}
	changed, err := mutate(state)
	if err != nil {
		return nil, false, err
	}
	state = normalizeAuthStateFile(state)
	if !changed {
		return state, false, nil
	}
	if err := SaveAuthState(path, state); err != nil {
		return nil, false, err
	}
	return state, true, nil
}

func UpsertOAuthStateRecord(path string, key OAuthStateKey, mutate func(*OAuthStateRecord) (bool, error)) (AuthStateFile, *OAuthStateRecord, bool, error) {
	if mutate == nil {
		return nil, nil, false, fmt.Errorf("oauth state mutate func is nil")
	}
	recordKey := OAuthStateRecordKey(key)
	if recordKey == "" {
		return nil, nil, false, fmt.Errorf("oauth state key is empty")
	}
	var updated OAuthStateRecord
	state, changed, err := UpdateAuthStateFile(path, func(state AuthStateFile) (bool, error) {
		if state == nil {
			state = make(AuthStateFile)
		}
		provider := strings.TrimSpace(key.Provider)
		if state[provider] == nil {
			state[provider] = make(map[string]OAuthStateRecord)
		}
		rec := state[provider][recordKey]
		rec.AccountID = strings.TrimSpace(key.AccountID)
		rec.Email = ""
		rec.Access = ""
		changed, err := mutate(&rec)
		if err != nil {
			return false, err
		}
		updated = rec
		if !changed {
			return false, nil
		}
		if rec.UpdatedAt == 0 {
			rec.UpdatedAt = time.Now().UnixMilli()
			updated.UpdatedAt = rec.UpdatedAt
		}
		state[provider][recordKey] = rec
		return true, nil
	})
	if err != nil {
		return nil, nil, false, err
	}
	return state, &updated, changed, nil
}

func RemoveOAuthStateRecord(path string, key OAuthStateKey) (AuthStateFile, bool, error) {
	recordKey := OAuthStateRecordKey(key)
	if recordKey == "" {
		return nil, false, fmt.Errorf("oauth state key is empty")
	}
	return UpdateAuthStateFile(path, func(state AuthStateFile) (bool, error) {
		provider := strings.TrimSpace(key.Provider)
		entries := state[provider]
		if len(entries) == 0 {
			return false, nil
		}
		if _, ok := entries[recordKey]; !ok {
			return false, nil
		}
		delete(entries, recordKey)
		if len(entries) == 0 {
			delete(state, provider)
		}
		return true, nil
	})
}

// RemovedOAuthStateEntry describes an invalid runtime-state entry removed from auth.state.yaml.
type RemovedOAuthStateEntry struct {
	Provider  string
	StateKey  string
	AccountID string
	Email     string
	Access    string
	Status    OAuthCredentialStatus
}

func (e RemovedOAuthStateEntry) DisplayName() string {
	if email := strings.TrimSpace(e.Email); email != "" {
		return email
	}
	if accountID := strings.TrimSpace(e.AccountID); accountID != "" {
		return accountID
	}
	if access := strings.TrimSpace(e.Access); access != "" {
		return access
	}
	if stateKey := strings.TrimSpace(e.StateKey); stateKey != "" {
		return stateKey
	}
	return strings.TrimSpace(e.Provider)
}

func RemoveInvalidOAuthStateRecords(path string) (AuthStateFile, []RemovedOAuthStateEntry, error) {
	var removed []RemovedOAuthStateEntry
	state, _, err := UpdateAuthStateFile(path, func(state AuthStateFile) (bool, error) {
		changed := false
		for provider, entries := range state {
			for key, record := range entries {
				if record.Status.IsValid() {
					continue
				}
				removed = append(removed, RemovedOAuthStateEntry{
					Provider:  provider,
					StateKey:  key,
					AccountID: record.AccountID,
					Email:     record.Email,
					Access:    record.Access,
					Status:    record.Status,
				})
				delete(entries, key)
				changed = true
			}
			if len(entries) == 0 {
				delete(state, provider)
			}
		}
		return changed, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return state, removed, nil
}

func normalizeAuthStateFile(raw AuthStateFile) AuthStateFile {
	state := make(AuthStateFile)
	for provider, entries := range raw {
		provider = strings.TrimSpace(provider)
		if provider == "" || len(entries) == 0 {
			continue
		}
		normalizedEntries := make(map[string]OAuthStateRecord)
		for _, record := range entries {
			record.AccountID = strings.TrimSpace(record.AccountID)
			record.Email = ""
			record.Access = ""
			recordKey := OAuthStateRecordKey(OAuthStateKey{Provider: provider, AccountID: record.AccountID})
			if recordKey == "" {
				continue
			}
			normalizedEntries[recordKey] = record
		}
		if len(normalizedEntries) > 0 {
			state[provider] = normalizedEntries
		}
	}
	return state
}

func FindOAuthStateRecord(state AuthStateFile, key OAuthStateKey) (OAuthStateRecord, bool) {
	if len(state) == 0 || strings.TrimSpace(key.AccountID) == "" {
		return OAuthStateRecord{}, false
	}
	provider := strings.TrimSpace(key.Provider)
	entries := state[provider]
	if len(entries) == 0 {
		return OAuthStateRecord{}, false
	}
	record, ok := entries[OAuthStateRecordKey(OAuthStateKey{Provider: provider, AccountID: key.AccountID})]
	return record, ok
}

func MergeOAuthStateRecord(existing OAuthStateRecord, incoming OAuthStateRecord) OAuthStateRecord {
	if incoming.AccountID != "" {
		existing.AccountID = incoming.AccountID
	}
	if incoming.Email != "" {
		existing.Email = incoming.Email
	}
	if incoming.Access != "" {
		existing.Access = incoming.Access
	}
	if incoming.Status != "" || existing.Status == "" {
		existing.Status = incoming.Status
	}
	if incoming.Expires != 0 {
		existing.Expires = incoming.Expires
	}
	if incoming.UpdatedAt >= existing.UpdatedAt {
		existing.UpdatedAt = incoming.UpdatedAt
		existing.LastWarmupAt = incoming.LastWarmupAt
		existing.CodexPrimaryUsedPct = incoming.CodexPrimaryUsedPct
		existing.CodexPrimaryWindowMin = incoming.CodexPrimaryWindowMin
		existing.CodexPrimaryResetAt = incoming.CodexPrimaryResetAt
		existing.CodexSecondaryUsedPct = incoming.CodexSecondaryUsedPct
		existing.CodexSecondaryWindowMin = incoming.CodexSecondaryWindowMin
		existing.CodexSecondaryResetAt = incoming.CodexSecondaryResetAt
		existing.CodexHasCredits = incoming.CodexHasCredits
		existing.CodexUnlimited = incoming.CodexUnlimited
		existing.CodexBalance = incoming.CodexBalance
	}
	return existing
}

func EqualOAuthStateRecord(a, b OAuthStateRecord) bool {
	if a.AccountID != b.AccountID || a.Email != b.Email || a.Access != b.Access || a.Expires != b.Expires || a.Status != b.Status || a.UpdatedAt != b.UpdatedAt || a.LastWarmupAt != b.LastWarmupAt || a.CodexPrimaryUsedPct != b.CodexPrimaryUsedPct || a.CodexPrimaryWindowMin != b.CodexPrimaryWindowMin || a.CodexPrimaryResetAt != b.CodexPrimaryResetAt || a.CodexSecondaryUsedPct != b.CodexSecondaryUsedPct || a.CodexSecondaryWindowMin != b.CodexSecondaryWindowMin || a.CodexSecondaryResetAt != b.CodexSecondaryResetAt || a.CodexBalance != b.CodexBalance {
		return false
	}
	if (a.CodexHasCredits == nil) != (b.CodexHasCredits == nil) {
		return false
	}
	if a.CodexHasCredits != nil && b.CodexHasCredits != nil && *a.CodexHasCredits != *b.CodexHasCredits {
		return false
	}
	if (a.CodexUnlimited == nil) != (b.CodexUnlimited == nil) {
		return false
	}
	if a.CodexUnlimited != nil && b.CodexUnlimited != nil && *a.CodexUnlimited != *b.CodexUnlimited {
		return false
	}
	return true
}

func ReadAuthStateMTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return info.ModTime(), nil
}
