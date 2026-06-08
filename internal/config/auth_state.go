package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sonicjson "github.com/bytedance/sonic"
)

// OAuthStateKey identifies a persisted OAuth runtime state entry.
// AccountUserID is used directly as the user-in-workspace key below the provider.
// RefreshSHA256 is only used for refresh-only credentials before account_id is known.
type OAuthStateKey struct {
	Provider      string
	AccountUserID string
	AccountID     string
	RefreshSHA256 string
	Email         string
}

// OAuthStateRecord stores dynamic OAuth runtime state shared across processes.
type OAuthStateRecord struct {
	AccountUserID           string                `json:"account_user_id,omitempty"`
	AccountID               string                `json:"account_id,omitempty"`
	Email                   string                `json:"email,omitempty"`
	RefreshSHA256           string                `json:"refresh_sha256,omitempty"`
	Expires                 int64                 `json:"expires,omitempty"`
	Status                  OAuthCredentialStatus `json:"status,omitempty"`
	UpdatedAt               int64                 `json:"updated_at,omitempty"`
	LastWarmupAt            int64                 `json:"last_warmup_at,omitempty"`
	CodexPrimaryUsedPct     float64               `json:"codex_primary_used_pct,omitempty"`
	CodexPrimaryWindowMin   int64                 `json:"codex_primary_window_minutes,omitempty"`
	CodexPrimaryResetAt     int64                 `json:"codex_primary_reset_at,omitempty"`
	CodexSecondaryUsedPct   float64               `json:"codex_secondary_used_pct,omitempty"`
	CodexSecondaryWindowMin int64                 `json:"codex_secondary_window_minutes,omitempty"`
	CodexSecondaryResetAt   int64                 `json:"codex_secondary_reset_at,omitempty"`
	CodexHasCredits         *bool                 `json:"codex_has_credits,omitempty"`
	CodexUnlimited          *bool                 `json:"codex_unlimited,omitempty"`
	CodexBalance            string                `json:"codex_balance,omitempty"`
}

// IsValid reports whether the runtime status is usable.
func (r OAuthStateRecord) IsValid() bool {
	return r.Status.IsValid()
}

// AuthStateFile is the on-disk shared runtime state keyed by provider then account_user_id.
type AuthStateFile map[string]map[string]OAuthStateRecord

func AuthStatePath() (string, error) {
	h, err := ConfigHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "auth.state.json"), nil
}

func OAuthRefreshStateKey(refresh string) string {
	return "refresh_sha256:" + fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(refresh))))
}

func OAuthStateRecordKey(key OAuthStateKey) string {
	if accountUserID := strings.TrimSpace(key.AccountUserID); accountUserID != "" {
		return accountUserID
	}
	if key.RefreshSHA256 != "" {
		return normalizeOAuthRefreshStateKey(key.RefreshSHA256)
	}
	return ""
}

func normalizeOAuthRefreshStateKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "refresh_sha256:") {
		return value
	}
	return "refresh_sha256:" + value
}

func LoadAuthState(path string) (AuthStateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(AuthStateFile), nil
		}
		return nil, err
	}
	return ParseAuthState(data)
}

// ParseAuthState parses raw auth.state JSON bytes. It returns an empty file
// when the payload is blank so callers can treat "no file" and "empty file"
// uniformly without re-reading from disk. Auth state is written by Chord, so it
// uses sonic's default fast decoder rather than a stricter wire-protocol config.
func ParseAuthState(data []byte) (AuthStateFile, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return make(AuthStateFile), nil
	}
	var raw AuthStateFile
	if err := sonicjson.ConfigDefault.Unmarshal(data, &raw); err != nil {
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
	lock, err := lockAuthFile(path)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return saveAuthStateUnlocked(path, state)
}

func saveAuthStateUnlocked(path string, state AuthStateFile) error {
	data, err := json.MarshalIndent(normalizeAuthStateFile(state), "", "  ")
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
	lock, err := lockAuthFile(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = lock.Close() }()

	beforeData, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("read auth state: %w", err)
	}
	state, err := ParseAuthState(beforeData)
	if err != nil {
		return nil, false, fmt.Errorf("load auth state: %w", err)
	}
	changed, err := mutate(state)
	if err != nil {
		return nil, false, err
	}
	state = normalizeAuthStateFile(state)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, false, err
	}
	if !changed && bytes.Equal(bytes.TrimSpace(beforeData), bytes.TrimSpace(data)) {
		return state, false, nil
	}
	if err := saveAuthStateUnlocked(path, state); err != nil {
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
	provider := strings.TrimSpace(key.Provider)
	if provider == "" {
		return nil, nil, false, fmt.Errorf("oauth state provider is empty")
	}
	var updated OAuthStateRecord
	state, changed, err := UpdateAuthStateFile(path, func(state AuthStateFile) (bool, error) {
		if state == nil {
			state = make(AuthStateFile)
		}
		if state[provider] == nil {
			state[provider] = make(map[string]OAuthStateRecord)
		}
		rec := state[provider][recordKey]
		if strings.TrimSpace(key.AccountUserID) != "" {
			rec.AccountUserID = strings.TrimSpace(key.AccountUserID)
		}
		if strings.TrimSpace(key.AccountID) != "" {
			rec.AccountID = strings.TrimSpace(key.AccountID)
		}
		rec.Email = strings.TrimSpace(key.Email)
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

// RemovedOAuthStateEntry describes an invalid runtime-state entry removed from auth.state.json.
type RemovedOAuthStateEntry struct {
	Provider      string
	StateKey      string
	AccountUserID string
	AccountID     string
	RefreshSHA256 string
	Email         string
	Status        OAuthCredentialStatus
}

func (e RemovedOAuthStateEntry) DisplayName() string {
	if email := strings.TrimSpace(e.Email); email != "" {
		return email
	}
	if accountUserID := strings.TrimSpace(e.AccountUserID); accountUserID != "" {
		return accountUserID
	}
	if accountID := strings.TrimSpace(e.AccountID); accountID != "" {
		return accountID
	}
	if stateKey := strings.TrimSpace(e.StateKey); stateKey != "" {
		return stateKey
	}
	return strings.TrimSpace(e.Provider)
}

func RemovedOAuthStateEntryFromRecord(provider, stateKey string, record OAuthStateRecord) RemovedOAuthStateEntry {
	accountUserID := strings.TrimSpace(record.AccountUserID)
	accountID := strings.TrimSpace(record.AccountID)
	refreshSHA256 := strings.TrimSpace(record.RefreshSHA256)
	if refreshSHA256 == "" && strings.HasPrefix(stateKey, "refresh_sha256:") {
		refreshSHA256 = strings.TrimSpace(stateKey)
	} else if refreshSHA256 != "" {
		refreshSHA256 = normalizeOAuthRefreshStateKey(refreshSHA256)
	}
	if accountUserID == "" && accountID == "" && refreshSHA256 == "" {
		accountUserID = strings.TrimSpace(stateKey)
	}
	return RemovedOAuthStateEntry{
		Provider:      provider,
		StateKey:      stateKey,
		AccountUserID: accountUserID,
		AccountID:     accountID,
		RefreshSHA256: refreshSHA256,
		Email:         record.Email,
		Status:        record.Status,
	}
}

func RemoveOAuthStateRecords(path string, remove func(provider, stateKey string, record OAuthStateRecord) bool) (AuthStateFile, []RemovedOAuthStateEntry, error) {
	if remove == nil {
		return nil, nil, fmt.Errorf("oauth state remove func is nil")
	}
	var removed []RemovedOAuthStateEntry
	state, _, err := UpdateAuthStateFile(path, func(state AuthStateFile) (bool, error) {
		changed := false
		for provider, entries := range state {
			for key, record := range entries {
				if !remove(provider, key, record) {
					continue
				}
				removed = append(removed, RemovedOAuthStateEntryFromRecord(provider, key, record))
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

func RemoveInvalidOAuthStateRecords(path string) (AuthStateFile, []RemovedOAuthStateEntry, error) {
	return RemoveOAuthStateRecords(path, func(_ string, _ string, record OAuthStateRecord) bool {
		return !record.Status.IsValid()
	})
}

func normalizeAuthStateFile(raw AuthStateFile) AuthStateFile {
	state := make(AuthStateFile)
	for provider, entries := range raw {
		provider = strings.TrimSpace(provider)
		if provider == "" || len(entries) == 0 {
			continue
		}
		normalizedEntries := make(map[string]OAuthStateRecord)
		for rawKey, record := range entries {
			recordKey := strings.TrimSpace(rawKey)
			if recordKey == "" {
				continue
			}
			record.Email = strings.TrimSpace(record.Email)
			record.AccountUserID = strings.TrimSpace(record.AccountUserID)
			record.AccountID = strings.TrimSpace(record.AccountID)
			if strings.HasPrefix(recordKey, "refresh_sha256:") {
				record.AccountUserID = ""
				record.AccountID = ""
				record.RefreshSHA256 = recordKey
				normalizedEntries[recordKey] = record
				continue
			}
			if strings.Contains(recordKey, ":") {
				continue
			}
			if record.AccountUserID == "" {
				record.AccountUserID = recordKey
			}
			record.RefreshSHA256 = ""
			normalizedEntries[recordKey] = record
		}
		if len(normalizedEntries) > 0 {
			state[provider] = normalizedEntries
		}
	}
	return state
}

func FindOAuthStateRecord(state AuthStateFile, key OAuthStateKey) (OAuthStateRecord, bool) {
	if len(state) == 0 {
		return OAuthStateRecord{}, false
	}
	provider := strings.TrimSpace(key.Provider)
	entries := state[provider]
	if len(entries) == 0 {
		return OAuthStateRecord{}, false
	}
	recordKey := OAuthStateRecordKey(key)
	if recordKey == "" {
		return OAuthStateRecord{}, false
	}
	record, ok := entries[recordKey]
	if ok {
		record.AccountUserID = strings.TrimSpace(key.AccountUserID)
		record.AccountID = strings.TrimSpace(key.AccountID)
		record.RefreshSHA256 = strings.TrimSpace(key.RefreshSHA256)
	}
	return record, ok
}

func MergeOAuthStateRecord(existing OAuthStateRecord, incoming OAuthStateRecord) OAuthStateRecord {
	if incoming.AccountUserID != "" {
		existing.AccountUserID = incoming.AccountUserID
	}
	if incoming.AccountID != "" {
		existing.AccountID = incoming.AccountID
	}
	if incoming.RefreshSHA256 != "" {
		existing.RefreshSHA256 = incoming.RefreshSHA256
	}
	if incoming.Email != "" {
		existing.Email = incoming.Email
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
	if a.AccountUserID != b.AccountUserID || a.AccountID != b.AccountID || a.RefreshSHA256 != b.RefreshSHA256 || a.Email != b.Email || a.Expires != b.Expires || a.Status != b.Status || a.UpdatedAt != b.UpdatedAt || a.LastWarmupAt != b.LastWarmupAt || a.CodexPrimaryUsedPct != b.CodexPrimaryUsedPct || a.CodexPrimaryWindowMin != b.CodexPrimaryWindowMin || a.CodexPrimaryResetAt != b.CodexPrimaryResetAt || a.CodexSecondaryUsedPct != b.CodexSecondaryUsedPct || a.CodexSecondaryWindowMin != b.CodexSecondaryWindowMin || a.CodexSecondaryResetAt != b.CodexSecondaryResetAt || a.CodexBalance != b.CodexBalance {
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
