package mcp

import "testing"

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
