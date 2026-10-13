package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuthCustomMetadataLoginAndNewProcess(t *testing.T) {
	service, target, _ := loginFixture(t)
	var metadataRequests, anonymousRequests atomic.Int32
	control, _, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Sample-Key") != "" {
			t.Error("resource header leaked to control endpoint")
		}
		switch r.URL.Path {
		case "/custom/resource":
			metadataRequests.Add(1)
			_, _ = io.WriteString(w, `{"resource":"https://example.com/mcp","authorization_servers":["https://example.com"],"scopes_supported":["broad"]}`)
		case "/.well-known/oauth-authorization-server":
			_, _ = io.WriteString(w, `{"issuer":"https://example.com","authorization_endpoint":"https://example.com/authorize","token_endpoint":"https://example.com/token","response_types_supported":["code"],"token_endpoint_auth_methods_supported":["none"],"code_challenge_methods_supported":["S256"]}`)
		case "/token":
			_, _ = io.WriteString(w, `{"access_token":"sample-access","token_type":"Bearer","expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	business := &http.Client{Transport: probeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: r}
		if r.Header.Get("Authorization") == "" {
			anonymousRequests.Add(1)
			response.StatusCode = http.StatusUnauthorized
			response.Header.Set("WWW-Authenticate", `Bearer resource_metadata="https://example.com/custom/resource", scope="read"`)
			response.Body = io.NopCloser(strings.NewReader(""))
			return response, nil
		}
		var request JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		result := `{}`
		switch request.Method {
		case "initialize":
			result = `{"protocolVersion":"` + protocolVersion + `"}`
		case "notifications/initialized":
			response.StatusCode = http.StatusAccepted
		case "tools/list":
			result = `{"tools":[]}`
		default:
			t.Errorf("unexpected method %s", request.Method)
		}
		response.Header.Set("Content-Type", "application/json")
		response.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, request.ID, result)))
		return response, nil
	})}
	service.Close()
	opts := AuthOptions{Store: service.store, HTTPClient: control, MCPHTTPClient: business, ClientInfo: testClientInfo}
	service = NewAuthService(opts)
	defer service.Close()
	target.Config.Headers = map[string]string{"X-Sample-Key": "sample-value"}
	result, err := service.Login(t.Context(), target, LoginOptions{}, func(p LoginProgress) {
		if p.AuthorizationURL != "" {
			u, _ := url.Parse(p.AuthorizationURL)
			if u.Query().Get("scope") != "read" {
				t.Errorf("scope=%q", u.Query().Get("scope"))
			}
		}
		deliverLoginCallback(t, p)
	})
	if err != nil || !result.CredentialSaved || !result.Validation.OK {
		t.Fatalf("login: saved=%t err=%v", result.CredentialSaved, err)
	}
	if metadataRequests.Load() != 1 || anonymousRequests.Load() != 1 {
		t.Fatalf("cache missed: metadata=%d anonymous=%d", metadataRequests.Load(), anonymousRequests.Load())
	}
	restarted := NewAuthService(opts)
	defer restarted.Close()
	identity := ExpectedIdentity{BindingID: result.Version.BindingID, AuthGeneration: result.Version.AuthGeneration}
	if _, err := restarted.Acquire(t.Context(), target, identity); err != nil {
		t.Fatal(err)
	}
	if metadataRequests.Load() != 2 || anonymousRequests.Load() != 2 {
		t.Fatal("new service did not rediscover the custom resource")
	}
	restarted.discoverer.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if _, err := restarted.Acquire(t.Context(), target, identity); err != nil {
		t.Fatal(err)
	}
	if metadataRequests.Load() != 3 || anonymousRequests.Load() != 3 {
		t.Fatal("expired metadata did not repeat the resource challenge")
	}
	if err := restarted.ObserveRejection(t.Context(), target, RequestStamp{CredentialVersion: result.Version}, Challenge{StatusCode: http.StatusUnauthorized}); err != nil {
		t.Fatal(err)
	}
	restarted.discoverer.mu.Lock()
	cached := len(restarted.discoverer.cache)
	restarted.discoverer.mu.Unlock()
	if cached != 0 {
		t.Fatal("current token rejection retained discovery metadata")
	}
	for _, stage := range result.Validation.Stages {
		if stage.Stage == StageDiscovery && stage.State != ProbeOK {
			t.Fatalf("discovery stage=%+v", stage)
		}
	}
}

func TestOAuthResourceChallengeRejectsRedirectAndMalformedHeader(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			service := NewAuthService(AuthOptions{MCPHTTPClient: &http.Client{Transport: probeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://other.example.invalid/mcp"}, "Www-Authenticate": {`Bearer resource_metadata="unterminated`}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})}})
			defer service.Close()
			_, err := service.resourceChallenge(t.Context(), refreshTestTarget())
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
