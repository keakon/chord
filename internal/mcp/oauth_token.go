package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// rejectDuplicateJSON validates every object, including nested registration and
// credential records, before ordinary decoding can silently accept duplicate keys.
func rejectDuplicateJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return fmt.Errorf("JSON nesting limit exceeded")
		}
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("invalid JSON")
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return fmt.Errorf("invalid JSON")
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return fmt.Errorf("duplicate JSON field")
				}
				seen[s] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid JSON")
		}
		_, err = dec.Token()
		if err != nil {
			return fmt.Errorf("invalid JSON")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON content")
	}
	return nil
}

type tokenResponse struct {
	accessToken   string
	refreshToken  string
	expiresIn     int64
	grantedScopes []string
}

func parseTokenResponse(raw []byte, requested []string) (tokenResponse, error) {
	invalid := func() (tokenResponse, error) { return tokenResponse{}, fmt.Errorf("invalid OAuth token response") }
	if len(raw) > 1<<20 || rejectDuplicateJSON(raw) != nil {
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
