package mcp

import (
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// strictJSON reports whether raw is a single, strictly valid JSON document:
// jsontext validation rejects duplicate object members, invalid UTF-8, and
// trailing values before ordinary decoding could silently accept them. Nesting
// depth is bounded by the v2 decoder default (10000), far above anything OAuth
// payloads use.
func strictJSON(raw []byte) bool {
	return jsontext.Value(raw).IsValid()
}

type tokenResponse struct {
	accessToken   string
	refreshToken  string
	expiresIn     int64
	grantedScopes []string
}

func parseTokenResponse(raw []byte, requested []string) (tokenResponse, error) {
	invalid := func() (tokenResponse, error) { return tokenResponse{}, fmt.Errorf("invalid OAuth token response") }
	if len(raw) > 1<<20 || !strictJSON(raw) {
		return invalid()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return invalid()
	}
	readString := func(key string) (string, bool) {
		b, ok := fields[key]
		if !ok {
			return "", true
		}
		var s string
		if string(b) == "null" || json.Unmarshal(b, &s) != nil {
			return "", false
		}
		return s, true
	}
	access, ok := readString("access_token")
	if !ok || access == "" || strings.ContainsAny(access, "\r\n") {
		return invalid()
	}
	kind, ok := readString("token_type")
	if !ok || !strings.EqualFold(kind, "Bearer") {
		return invalid()
	}
	refresh, ok := readString("refresh_token")
	if !ok {
		return invalid()
	}
	if _, exists := fields["refresh_token"]; exists && refresh == "" {
		return invalid()
	}
	var lifetime int64
	if b, exists := fields["expires_in"]; exists {
		var err error
		lifetime, err = strconv.ParseInt(string(b), 10, 64)
		if err != nil || lifetime <= 0 || lifetime > math.MaxInt64/int64(time.Second) {
			return invalid()
		}
	}
	scopes := slices.Clone(requested)
	if _, exists := fields["scope"]; exists {
		s, ok := readString("scope")
		if !ok {
			return invalid()
		}
		var err error
		scopes, err = splitOAuthScope(s)
		if err != nil {
			return invalid()
		}
	}
	return tokenResponse{accessToken: access, refreshToken: refresh, expiresIn: lifetime, grantedScopes: scopes}, nil
}

func splitOAuthScope(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	for _, r := range raw {
		if r != ' ' && (r < 0x21 || r > 0x7e || r == '"' || r == '\\') {
			return nil, fmt.Errorf("invalid OAuth scope")
		}
	}
	scopes := strings.Split(raw, " ")
	if slices.Contains(scopes, "") {
		return nil, fmt.Errorf("invalid OAuth scope")
	}
	slices.Sort(scopes)
	return slices.Compact(scopes), nil
}
