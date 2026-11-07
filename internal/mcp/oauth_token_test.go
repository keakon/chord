package mcp

import (
	"strings"
	"testing"
)

func TestTokenResponseStrictFields(t *testing.T) {
	for _, raw := range []string{
		`{"access_token":"sample","token_type":"Bearer","expires_in":1.5}`,
		`{"access_token":"sample","token_type":"Bearer","expires_in":0}`,
		`{"access_token":"sample","token_type":"Bearer","expires_in":"60"}`,
		`{"access_token":"sample","token_type":"Bearer","expires_in":9223372036854775807}`,
		`{"access_token":"sample","access_token":"other","token_type":"Bearer"}`,
		`{"access_token":"sample","token_type":"MAC"}`,
		`{"access_token":"sample","token_type":"Bearer","refresh_token":""}`,
		`{"access_token":"sample","token_type":"Bearer","scope":null}`,
		`{"access_token":"sample","token_type":"Bearer","scope":"read\twrite"}`,
		`null`,
		// Invalid UTF-8 is rejected by the strict JSON gate, not just field checks.
		`{"access_token":"` + "\xff\xfe" + `","token_type":"Bearer"}`,
	} {
		if _, err := parseTokenResponse([]byte(raw), nil); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	token, err := parseTokenResponse([]byte(`{"access_token":"sample","token_type":"bearer","expires_in":10,"scope":"write read read","extra":{"field":true}}`), []string{"requested"})
	if err != nil || token.expiresIn != 10 || len(token.grantedScopes) != 2 || token.grantedScopes[0] != "read" {
		t.Fatalf("token response: %v", err)
	}
	token, err = parseTokenResponse([]byte(`{"access_token":"sample","token_type":"Bearer"}`), []string{"requested"})
	if err != nil || token.expiresIn != 0 || token.grantedScopes[0] != "requested" {
		t.Fatal("missing expiry/scope defaults changed")
	}
}

// TestStrictJSONBoundary pins the strict JSON gate used by OAuth token, store,
// and metadata decoding. Nesting depth is bounded by the v2 decoder default
// (10000); the handwritten scanner this replaced capped it at 64.
func TestStrictJSONBoundary(t *testing.T) {
	valid := []string{`{}`, `{"a":{"b":[1,null,"x"]}}`}
	for _, raw := range valid {
		if !strictJSON([]byte(raw)) {
			t.Errorf("rejected valid JSON %s", raw)
		}
	}
	for _, raw := range []string{
		`{"a":1,"a":2}`,
		`{"a":{"x":1,"x":2}}`,
		`{"a":1} {"b":2}`,
		"{\"a\":\"\xff\xfe\"}",
		`not json`,
	} {
		if strictJSON([]byte(raw)) {
			t.Errorf("accepted invalid JSON %s", raw)
		}
	}
	deep := []byte(strings.Repeat("[", 10000) + strings.Repeat("]", 10000))
	if !strictJSON(deep) {
		t.Error("depth at the v2 default limit should be accepted")
	}
	tooDeep := []byte(strings.Repeat("[", 10001) + strings.Repeat("]", 10001))
	if strictJSON(tooDeep) {
		t.Error("depth beyond the v2 default limit should be rejected")
	}
}
