package config

import (
	"encoding/json"
	"fmt"
	"testing"
)

var authStateBenchBytes = buildAuthStateBenchBytes(64)

func BenchmarkParseAuthState(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseAuthState(authStateBenchBytes); err != nil {
			b.Fatal(err)
		}
	}
}

func buildAuthStateBenchBytes(accounts int) []byte {
	state := make(AuthStateFile)
	entries := make(map[string]OAuthStateRecord, accounts)
	for i := range accounts {
		key := fmt.Sprintf("user-%03d__acc-%03d", i, i)
		entries[key] = OAuthStateRecord{
			AccountUserID:           key,
			AccountID:               fmt.Sprintf("acc-%03d", i),
			Email:                   fmt.Sprintf("user-%03d@example.com", i),
			RefreshSHA256:           fmt.Sprintf("refresh_sha256:%064d", i),
			Expires:                 2000000000 + int64(i),
			Status:                  OAuthStatusNormal,
			UpdatedAt:               1800000000000 + int64(i),
			LastWarmupAt:            1800000000000 + int64(i),
			CodexPrimaryUsedPct:     float64(i%100) / 100,
			CodexPrimaryWindowMin:   300,
			CodexPrimaryResetAt:     1800001000000 + int64(i),
			CodexSecondaryUsedPct:   float64((i+50)%100) / 100,
			CodexSecondaryWindowMin: 10080,
			CodexSecondaryResetAt:   1800100000000 + int64(i),
		}
	}
	state["openai"] = entries
	data, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	return data
}
