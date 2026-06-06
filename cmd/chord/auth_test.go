package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestResolvePprofListenAddr(t *testing.T) {
	t.Setenv("CHORD_PPROF_PORT", "")
	got, err := resolvePprofListenAddr()
	if err != nil || got != "" {
		t.Fatalf("empty pprof = %q, %v", got, err)
	}

	t.Setenv("CHORD_PPROF_PORT", ":6060")
	got, err = resolvePprofListenAddr()
	if err != nil || got != "127.0.0.1:6060" {
		t.Fatalf("pprof :6060 = %q, %v", got, err)
	}

	for _, value := range []string{"0", "65536", "abc"} {
		t.Setenv("CHORD_PPROF_PORT", value)
		if _, err := resolvePprofListenAddr(); err == nil {
			t.Fatalf("expected error for CHORD_PPROF_PORT=%q", value)
		}
	}
}

func TestResolveAuthLoginProviderName(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "arg only", args: []string{" codex "}, want: "codex"},
		{name: "flag only", flag: " codex ", want: "codex"},
		{name: "matching arg and flag", flag: "codex", args: []string{"codex"}, want: "codex"},
		{name: "mismatch", flag: "a", args: []string{"b"}, wantErr: "provider mismatch"},
		{name: "too many", args: []string{"a", "b"}, wantErr: "at most one"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAuthLoginProviderName("chord auth", tc.flag, tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveAuthLoginProviderName: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEligibleAuthLoginProviders(t *testing.T) {
	got := eligibleAuthLoginProviders(map[string]config.ProviderConfig{
		"z": {Preset: config.ProviderPresetCodex},
		"a": {Preset: " CODEX "},
		"b": {Preset: ""},
	})
	want := []string{"a", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("eligibleAuthLoginProviders = %#v, want %#v", got, want)
	}
}

func TestLoadAuthLoginProvidersIncludesProjectOverrides(t *testing.T) {
	configHome := t.TempDir()
	projectRoot := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	globalConfigPath := filepath.Join(configHome, "config.yaml")
	if err := os.WriteFile(globalConfigPath, []byte(`providers:
  global:
    preset: codex
    type: responses
    api_url: https://global.example/v1/responses
    models:
      gpt-5:
        limit:
          context: 8192
          output: 1024
proxy: https://global-proxy.example
`), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir project .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".chord", "config.yaml"), []byte(`providers:
  project:
    preset: codex
    type: responses
    api_url: https://project.example/v1/responses
    models:
      gpt-5-mini:
        limit:
          context: 4096
          output: 512
proxy: socks5://project-proxy.example:1080
`), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(cwd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir(projectRoot): %v", err)
	}

	providers, proxy, err := loadAuthLoginProviders()
	if err != nil {
		t.Fatalf("loadAuthLoginProviders: %v", err)
	}
	if proxy != "socks5://project-proxy.example:1080" {
		t.Fatalf("proxy = %q, want project override", proxy)
	}
	if _, ok := providers["global"]; !ok {
		t.Fatal("expected global provider to remain available")
	}
	if _, ok := providers["project"]; !ok {
		t.Fatal("expected project provider to be merged in")
	}
}

func TestLoadAuthLoginProvidersUsesOnlyCurrentWorkingDirectoryProjectConfig(t *testing.T) {
	configHome := t.TempDir()
	projectRoot := t.TempDir()
	nested := filepath.Join(projectRoot, "nested", "child")
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	globalConfigPath := filepath.Join(configHome, "config.yaml")
	if err := os.WriteFile(globalConfigPath, []byte(`providers:
  global:
    preset: codex
    type: responses
    api_url: https://global.example/v1/responses
    models:
      gpt-5:
        limit:
          context: 8192
          output: 1024
proxy: https://global-proxy.example
`), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir project .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".chord", "config.yaml"), []byte(`proxy: socks5://project-root.example:1080
providers:
  root:
    preset: codex
    type: responses
    api_url: https://root.example/v1/responses
    models:
      gpt-5:
        limit:
          context: 4096
          output: 512
`), 0o644); err != nil {
		t.Fatalf("write root project config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(nested, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir nested .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".chord", "config.yaml"), []byte(`proxy: socks5://nested.example:1080
providers:
  nested:
    preset: codex
    type: responses
    api_url: https://nested.example/v1/responses
    models:
      gpt-5-mini:
        limit:
          context: 2048
          output: 256
`), 0o644); err != nil {
		t.Fatalf("write nested project config: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(cwd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("Chdir(nested): %v", err)
	}

	providers, proxy, err := loadAuthLoginProviders()
	if err != nil {
		t.Fatalf("loadAuthLoginProviders: %v", err)
	}
	if proxy != "socks5://nested.example:1080" {
		t.Fatalf("proxy = %q, want nested cwd project override", proxy)
	}
	if _, ok := providers["nested"]; !ok {
		t.Fatal("expected nested cwd project provider to be merged in")
	}
	if _, ok := providers["root"]; ok {
		t.Fatal("did not expect parent-directory project config to be merged")
	}
}

func TestLoadAuthLoginProvidersMissingConfigReturnsInitialSetupError(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	providers, proxy, err := loadAuthLoginProviders()
	if err == nil {
		t.Fatalf("expected missing-config error")
	}
	if providers != nil || proxy != "" {
		t.Fatalf("unexpected providers=%v proxy=%q on error", providers, proxy)
	}
	if err.Error() != initialSetupRequiredMessage {
		t.Fatalf("error = %q, want %q", err, initialSetupRequiredMessage)
	}
}

func TestPromptAuthLoginProvider(t *testing.T) {
	t.Run("single provider", func(t *testing.T) {
		var out bytes.Buffer
		got, err := promptAuthLoginProvider(strings.NewReader(""), &out, []string{"codex"})
		if err != nil || got != "codex" {
			t.Fatalf("got %q, err %v", got, err)
		}
		if !strings.Contains(out.String(), "only available") {
			t.Fatalf("expected single-provider notice, got %q", out.String())
		}
	})

	t.Run("selects by number after invalid input", func(t *testing.T) {
		var out bytes.Buffer
		got, err := promptAuthLoginProvider(strings.NewReader("bad\n2\n"), &out, []string{"a", "b"})
		if err != nil || got != "b" {
			t.Fatalf("got %q, err %v", got, err)
		}
		if !strings.Contains(out.String(), "Invalid selection") {
			t.Fatalf("expected invalid prompt, got %q", out.String())
		}
	})

	t.Run("selects by name", func(t *testing.T) {
		got, err := promptAuthLoginProvider(strings.NewReader("b\n"), io.Discard, []string{"a", "b"})
		if err != nil || got != "b" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})
}

func TestGenerateOpenAIPKCEAndState(t *testing.T) {
	pkce, err := generateOpenAIPKCE()
	if err != nil {
		t.Fatalf("generateOpenAIPKCE: %v", err)
	}
	if len(pkce.Verifier) != 43 {
		t.Fatalf("verifier length = %d, want 43", len(pkce.Verifier))
	}
	for _, r := range pkce.Verifier {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~", r) {
			t.Fatalf("verifier contains invalid character %q in %q", r, pkce.Verifier)
		}
	}
	sum := sha256.Sum256([]byte(pkce.Verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if pkce.Challenge != wantChallenge {
		t.Fatalf("challenge = %q, want %q", pkce.Challenge, wantChallenge)
	}

	state, err := generateOpenAIState()
	if err != nil {
		t.Fatalf("generateOpenAIState: %v", err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		t.Fatalf("state is not raw URL base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("decoded state length = %d, want 32", len(decoded))
	}
}

func TestGenerateOpenAIRandomString(t *testing.T) {
	got, err := generateOpenAIRandomString(64)
	if err != nil {
		t.Fatalf("generateOpenAIRandomString: %v", err)
	}
	if len(got) != 64 {
		t.Fatalf("length = %d, want 64", len(got))
	}
	for _, r := range got {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~", r) {
			t.Fatalf("random string contains invalid character %q in %q", r, got)
		}
	}

	empty, err := generateOpenAIRandomString(0)
	if err != nil {
		t.Fatalf("generateOpenAIRandomString(0): %v", err)
	}
	if empty != "" {
		t.Fatalf("generateOpenAIRandomString(0) = %q, want empty", empty)
	}
}

func TestConstantTimeStringEqual(t *testing.T) {
	if !constantTimeStringEqual("state-123", "state-123") {
		t.Fatal("matching strings should compare equal")
	}
	if constantTimeStringEqual("state-123", "state-124") {
		t.Fatal("different same-length strings should not compare equal")
	}
	if constantTimeStringEqual("state-123", "state-1234") {
		t.Fatal("different-length strings should not compare equal")
	}
}

func TestOpenAIOAuthCallbackAddressHelpers(t *testing.T) {
	if got := openAIOAuthCallbackListenAddr(1455); got != "127.0.0.1:1455" {
		t.Fatalf("listen addr = %q", got)
	}
	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2345}
	port, err := openAIOAuthListenerPort(addr)
	if err != nil || port != 2345 {
		t.Fatalf("openAIOAuthListenerPort = %d, %v", port, err)
	}
	if _, err := openAIOAuthListenerPort(&net.UnixAddr{Name: "sock", Net: "unix"}); err == nil {
		t.Fatal("expected error for non-TCP addr")
	}
	if got := openAIOAuthCallbackRedirectURI(2345); got != "http://localhost:2345/auth/callback" {
		t.Fatalf("redirect URI = %q", got)
	}
}

func TestStartOpenAICallbackServerHandlesCallbackOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		query      url.Values
		wantStatus int
		wantCode   string
		wantErr    string
		wantBody   string
	}{
		{
			name:       "success",
			query:      url.Values{"state": {"state-123"}, "code": {"code-123"}},
			wantStatus: http.StatusOK,
			wantCode:   "code-123",
			wantBody:   "Login successful",
		},
		{
			name:       "oauth error uses description",
			query:      url.Values{"error": {"access_denied"}, "error_description": {"denied by user"}},
			wantStatus: http.StatusBadRequest,
			wantErr:    "OAuth login failed: denied by user",
			wantBody:   "denied by user",
		},
		{
			name:       "state mismatch",
			query:      url.Values{"state": {"wrong"}, "code": {"code-123"}},
			wantStatus: http.StatusBadRequest,
			wantErr:    "OAuth state verification failed",
			wantBody:   "invalid state",
		},
		{
			name:       "missing code",
			query:      url.Values{"state": {"state-123"}},
			wantStatus: http.StatusBadRequest,
			wantErr:    "OAuth callback missing code",
			wantBody:   "missing code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codeCh := make(chan string, 1)
			errCh := make(chan error, 2)
			server, redirectURI, err := startOpenAICallbackServer("state-123", 0, codeCh, errCh)
			if err != nil {
				t.Fatalf("startOpenAICallbackServer: %v", err)
			}
			defer server.Shutdown(context.Background())

			resp, err := http.Get(redirectURI + "?" + tt.query.Encode())
			if err != nil {
				t.Fatalf("GET callback: %v", err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read callback body: %v", err)
			}
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body %q", resp.StatusCode, tt.wantStatus, body)
			}
			if !strings.Contains(string(body), tt.wantBody) {
				t.Fatalf("body = %q, want substring %q", body, tt.wantBody)
			}

			select {
			case got := <-codeCh:
				if got != tt.wantCode {
					t.Fatalf("callback code = %q, want %q", got, tt.wantCode)
				}
			default:
				if tt.wantCode != "" {
					t.Fatalf("callback code missing, want %q", tt.wantCode)
				}
			}
			select {
			case gotErr := <-errCh:
				if tt.wantErr == "" {
					t.Fatalf("unexpected callback error: %v", gotErr)
				}
				if !strings.Contains(gotErr.Error(), tt.wantErr) {
					t.Fatalf("callback error = %v, want substring %q", gotErr, tt.wantErr)
				}
			default:
				if tt.wantErr != "" {
					t.Fatalf("callback error missing, want %q", tt.wantErr)
				}
			}
		})
	}
}

func TestBuildOpenAIAuthorizeURL(t *testing.T) {
	pkce := &openAIPKCECodes{Verifier: "verifier", Challenge: "challenge"}
	redirectURI := openAIOAuthCallbackRedirectURI(openAIOAuthCallbackDefaultPort)
	raw := buildOpenAIAuthorizeURL(pkce, "state-123", redirectURI)
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "auth.openai.com" {
		t.Fatalf("unexpected authorize URL: %s", raw)
	}
	q := parsed.Query()
	if q.Get("client_id") != config.OpenAIOAuthClientID {
		t.Fatalf("unexpected client_id: %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != redirectURI {
		t.Fatalf("unexpected redirect_uri: %q", q.Get("redirect_uri"))
	}
	if q.Get("scope") != openAIOAuthScope {
		t.Fatalf("unexpected scope: %q", q.Get("scope"))
	}
	if q.Get("code_challenge") != "challenge" {
		t.Fatalf("unexpected code_challenge: %q", q.Get("code_challenge"))
	}
	if q.Get("originator") != openAIOAuthOriginator {
		t.Fatalf("unexpected originator: %q", q.Get("originator"))
	}
	if q.Get("codex_cli_simplified_flow") != "true" {
		t.Fatalf("expected codex_cli_simplified_flow=true, got %q", q.Get("codex_cli_simplified_flow"))
	}
}

func TestBuildOpenAILoginHTTPClientRejectsInvalidProxy(t *testing.T) {
	proxyURL := "ftp://proxy.example.invalid:1080"
	client, err := buildOpenAILoginHTTPClient(config.ProviderConfig{Proxy: &proxyURL}, "")
	if err == nil || client != nil {
		t.Fatalf("buildOpenAILoginHTTPClient() = (%v, %v), want nil client and error", client, err)
	}
	if !strings.Contains(err.Error(), "create login HTTP client") || !strings.Contains(err.Error(), "unsupported proxy scheme") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveProviderOAuthSettings_PresetCodexDefaults(t *testing.T) {
	tokenURL, clientID, ok, err := resolveProviderOAuthSettings(config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: config.ProviderPresetCodex,
	}, nil)
	if err != nil {
		t.Fatalf("resolveProviderOAuthSettings returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected resolveProviderOAuthSettings to enable OAuth defaults")
	}
	if tokenURL != config.OpenAIOAuthTokenURL {
		t.Fatalf("unexpected tokenURL: %q", tokenURL)
	}
	if clientID != config.OpenAIOAuthClientID {
		t.Fatalf("unexpected clientID: %q", clientID)
	}
}

func TestResolveProviderOAuthSettings_WithoutPresetDisabled(t *testing.T) {
	tokenURL, clientID, ok, err := resolveProviderOAuthSettings(config.ProviderConfig{
		Type:     config.ProviderTypeChatCompletions,
		TokenURL: config.OpenAIOAuthTokenURL,
		ClientID: config.OpenAIOAuthClientID,
		APIURL:   config.OpenAICodexResponsesURL,
	}, nil)
	if err != nil {
		t.Fatalf("resolveProviderOAuthSettings returned error: %v", err)
	}
	if ok {
		t.Fatalf("expected resolveProviderOAuthSettings to stay disabled without preset, got tokenURL=%q clientID=%q", tokenURL, clientID)
	}
}

func TestRunAuthLoginDevice_RequiresPresetCodex(t *testing.T) {
	err := runAuthLoginDevice("openai", config.ProviderConfig{
		Type:     config.ProviderTypeChatCompletions,
		TokenURL: "https://example.com/oauth/token",
		ClientID: "client-123",
	}, "", context.Background())
	if err == nil {
		t.Fatal("expected runAuthLoginDevice to reject non-preset-codex provider")
	}
	if !strings.Contains(err.Error(), "preset: codex") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAuthLoginDevice_OpenAICodexHeadlessSuccess(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	var pollCalls int
	var serverURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"device_auth_id": "device-auth-123",
				"user_code":      "ABCD-EFGH",
				"interval":       "1",
			})
		case "/api/accounts/deviceauth/token":
			pollCalls++
			if pollCalls < 2 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"authorization_code": "auth-code-123",
				"code_verifier":      "verifier-123",
			})
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if got := r.Form.Get("redirect_uri"); got != serverURL+"/deviceauth/callback" {
				t.Fatalf("unexpected redirect_uri: %q", got)
			}
			if got := r.Form.Get("client_id"); got != "client-123" {
				t.Fatalf("unexpected client_id: %q", got)
			}
			if got := r.Form.Get("code"); got != "auth-code-123" {
				t.Fatalf("unexpected code: %q", got)
			}
			if got := r.Form.Get("code_verifier"); got != "verifier-123" {
				t.Fatalf("unexpected code_verifier: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(openAITokenResponse{
				IDToken:      "header." + base64Payload(`{"chatgpt_account_id":"acc-123"}`) + ".sig",
				AccessToken:  "access-123",
				RefreshToken: "refresh-123",
				ExpiresIn:    3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	serverURL = srv.URL

	err := runOpenAICodexDeviceLoginWithOutput(context.Background(), "codex", srv.Client(), serverURL+"/oauth/token", "client-123", io.Discard)
	if err != nil {
		t.Fatalf("runAuthLoginDevice: %v", err)
	}
	if pollCalls != 2 {
		t.Fatalf("poll calls = %d, want 2", pollCalls)
	}

	authPath := filepath.Join(configHome, "auth.yaml")
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	creds := auth["codex"]
	if len(creds) != 1 || creds[0].OAuth == nil {
		t.Fatalf("expected one oauth credential, got %#v", creds)
	}
	if creds[0].OAuth.Access != "access-123" || creds[0].OAuth.Refresh != "refresh-123" {
		t.Fatalf("unexpected stored credential: %#v", creds[0].OAuth)
	}
}

func TestRunOpenAICodexDeviceLoginWrapperSuccess(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	var serverURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"device_auth_id": "device-auth-123",
				"user_code":      "ABCD-EFGH",
				"interval":       "1",
			})
		case "/api/accounts/deviceauth/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"authorization_code": "auth-code-123",
				"code_verifier":      "verifier-123",
			})
		case "/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(openAITokenResponse{
				IDToken:      "header." + base64Payload(`{"chatgpt_account_id":"acc-123"}`) + ".sig",
				AccessToken:  "access-123",
				RefreshToken: "refresh-123",
				ExpiresIn:    3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	serverURL = srv.URL

	if err := runOpenAICodexDeviceLogin(context.Background(), "codex", srv.Client(), serverURL+"/oauth/token", "client-123"); err != nil {
		t.Fatalf("runOpenAICodexDeviceLogin: %v", err)
	}
	auth, err := config.LoadAuthConfig(filepath.Join(configHome, "auth.yaml"))
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	if creds := auth["codex"]; len(creds) != 1 || creds[0].OAuth == nil || creds[0].OAuth.Refresh != "refresh-123" {
		t.Fatalf("stored credentials = %#v", creds)
	}
}

type recordingRoundTripper struct {
	t          *testing.T
	wantCtxKey any
	wantCtxVal any
	statusCode int
	body       string
}

func (rt recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.t.Helper()
	if got := req.Context().Value(rt.wantCtxKey); got != rt.wantCtxVal {
		rt.t.Fatalf("request context value = %v, want %v", got, rt.wantCtxVal)
	}
	body := rt.body
	if body == "" {
		body = `{}`
	}
	return &http.Response{
		StatusCode: rt.statusCode,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestAuthHTTPRequestsUseProvidedContext(t *testing.T) {
	type ctxKey string
	key := ctxKey("auth-request-context")
	ctx := context.WithValue(context.Background(), key, "ctx-value")
	client := &http.Client{Transport: recordingRoundTripper{t: t, wantCtxKey: key, wantCtxVal: "ctx-value", statusCode: http.StatusOK, body: `{"device_auth_id":"dev-1","user_code":"USER-1","interval":"1"}`}}

	if _, err := requestOpenAICodexDeviceCode(ctx, client, "https://issuer.example", "client-123"); err != nil {
		t.Fatalf("requestOpenAICodexDeviceCode: %v", err)
	}

	client.Transport = recordingRoundTripper{t: t, wantCtxKey: key, wantCtxVal: "ctx-value", statusCode: http.StatusOK, body: `{"authorization_code":"auth-code","code_verifier":"verifier"}`}
	if _, _, err := requestOpenAICodexDeviceAuthorizationCode(ctx, client, "https://issuer.example", "dev-1", "USER-1"); err != nil {
		t.Fatalf("requestOpenAICodexDeviceAuthorizationCode: %v", err)
	}

	client.Transport = recordingRoundTripper{t: t, wantCtxKey: key, wantCtxVal: "ctx-value", statusCode: http.StatusOK, body: `{"access_token":"access","refresh_token":"refresh","expires_in":3600}`}
	if _, err := exchangeOpenAICodeForTokensWithParams(ctx, client, "https://issuer.example/oauth/token", "https://issuer.example/callback", "client-123", "auth-code", "verifier"); err != nil {
		t.Fatalf("exchangeOpenAICodeForTokensWithParams: %v", err)
	}
}

func TestExchangeOpenAICodeForTokensWithParamsSubmitsExpectedForm(t *testing.T) {
	var gotForm url.Values
	var gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Fatalf("Content-Type = %q", got)
		}
		gotUserAgent = r.Header.Get("User-Agent")
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = make(url.Values, len(r.Form))
		for key, values := range r.Form {
			gotForm[key] = append([]string(nil), values...)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openAITokenResponse{
			AccessToken:  "access-123",
			RefreshToken: "refresh-123",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	tokens, err := exchangeOpenAICodeForTokensWithParams(
		context.Background(),
		srv.Client(),
		srv.URL,
		"http://localhost:1455/auth/callback",
		"client-123",
		"code-123",
		"verifier-123",
	)
	if err != nil {
		t.Fatalf("exchangeOpenAICodeForTokensWithParams: %v", err)
	}
	if tokens.AccessToken != "access-123" || tokens.RefreshToken != "refresh-123" {
		t.Fatalf("tokens = %#v", tokens)
	}
	want := map[string]string{
		"grant_type":    "authorization_code",
		"code":          "code-123",
		"redirect_uri":  "http://localhost:1455/auth/callback",
		"client_id":     "client-123",
		"code_verifier": "verifier-123",
	}
	for key, wantValue := range want {
		if got := gotForm.Get(key); got != wantValue {
			t.Fatalf("form[%s] = %q, want %q", key, got, wantValue)
		}
	}
	if gotUserAgent == "" {
		t.Fatal("User-Agent header was empty")
	}
}

func TestExchangeOpenAICodeForTokensUsesDefaultOAuthParams(t *testing.T) {
	var gotURL string
	var gotForm url.Values
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = make(url.Values, len(req.Form))
		for key, values := range req.Form {
			gotForm[key] = append([]string(nil), values...)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"access-123","refresh_token":"refresh-123","expires_in":3600}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	tokens, err := exchangeOpenAICodeForTokens(context.Background(), client, "code-123", "verifier-123", "http://localhost:1455/auth/callback")
	if err != nil {
		t.Fatalf("exchangeOpenAICodeForTokens: %v", err)
	}
	if tokens.AccessToken != "access-123" || tokens.RefreshToken != "refresh-123" {
		t.Fatalf("tokens = %#v", tokens)
	}
	if gotURL != config.OpenAIOAuthTokenURL {
		t.Fatalf("token URL = %q, want %q", gotURL, config.OpenAIOAuthTokenURL)
	}
	if got := gotForm.Get("client_id"); got != config.OpenAIOAuthClientID {
		t.Fatalf("client_id = %q, want default", got)
	}
	if got := gotForm.Get("redirect_uri"); got != "http://localhost:1455/auth/callback" {
		t.Fatalf("redirect_uri = %q", got)
	}
}

func TestExchangeOpenAICodeForTokensWithParamsReportsFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name: "http status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "invalid grant", http.StatusBadRequest)
			},
			wantErr: "token exchange failed: invalid grant",
		},
		{
			name: "invalid json",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("{"))
			},
			wantErr: "parse token exchange response",
		},
		{
			name: "missing refresh",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"access_token":"access-123"}`))
			},
			wantErr: "missing access_token or refresh_token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			_, err := exchangeOpenAICodeForTokensWithParams(context.Background(), srv.Client(), srv.URL, "http://localhost/callback", "client-123", "code-123", "verifier-123")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestExchangeOpenAICodeForTokensWithParamsReportsTransportError(t *testing.T) {
	client := &http.Client{Transport: failingRoundTripper{err: errors.New("network down")}}
	_, err := exchangeOpenAICodeForTokensWithParams(context.Background(), client, "https://issuer.example/oauth/token", "http://localhost/callback", "client-123", "code-123", "verifier-123")
	if err == nil || !strings.Contains(err.Error(), "token exchange request failed") || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("error = %v, want transport failure", err)
	}
}

type failingRoundTripper struct {
	err error
}

func (rt failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, rt.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestPersistOAuthCredential_RequiresAccountID(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	_, _, err := persistOAuthCredential(
		"openai",
		"header."+base64Payload(`{"email":"user@example.com"}`)+".sig",
		"header."+base64Payload(`{"https://api.openai.com/profile":{"email":"user@example.com"}}`)+".sig",
		"refresh-123",
		3600,
	)
	if err == nil {
		t.Fatal("expected persistOAuthCredential to reject missing account_id")
	}
	if !strings.Contains(err.Error(), "missing account_id claim") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPersistOAuthCredential_PreservesExistingComments(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	authPath := filepath.Join(configHome, "auth.yaml")
	if err := os.WriteFile(authPath, []byte(`# existing auth comment
openai:
  # legacy oauth comment
  - refresh: old-refresh
    access: old-access
    expires: 100
anthropic:
  # keep provider comment
  - sk-ant-test
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, err := persistOAuthCredential(
		"openai",
		"header."+base64Payload(`{"chatgpt_account_id":"acc-123","email":"user@example.com"}`)+".sig",
		"access-123",
		"refresh-123",
		3600,
	)
	if err != nil {
		t.Fatalf("persistOAuthCredential: %v", err)
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"# existing auth comment",
		"# legacy oauth comment",
		"# keep provider comment",
		"access: access-123",
		"refresh: refresh-123",
		"account_id: acc-123",
		"email: user@example.com",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected auth.yaml to contain %q, got:\n%s", want, text)
		}
	}
}

func TestPromptAuthLoginProvider_SelectByIndex(t *testing.T) {
	in := strings.NewReader("2\n")
	var out bytes.Buffer

	got, err := promptAuthLoginProvider(in, &out, []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("promptAuthLoginProvider: %v", err)
	}
	if got != "beta" {
		t.Fatalf("expected beta, got %q", got)
	}
	if !strings.Contains(out.String(), "Available preset: codex providers:") {
		t.Fatalf("unexpected prompt output: %q", out.String())
	}
}

func TestPromptAuthLoginProvider_SingleOption(t *testing.T) {
	var out bytes.Buffer

	got, err := promptAuthLoginProvider(strings.NewReader(""), &out, []string{"codex"})
	if err != nil {
		t.Fatalf("promptAuthLoginProvider: %v", err)
	}
	if got != "codex" {
		t.Fatalf("expected codex, got %q", got)
	}
}

func TestNewAuthCmd_DefaultsToDirectLogin(t *testing.T) {
	cmd := newAuthCmd()
	if cmd.Use != "auth [provider]" {
		t.Fatalf("unexpected use: %q", cmd.Use)
	}
	if cmd.RunE == nil {
		t.Fatal("expected auth command to run login flow directly")
	}
	if flag := cmd.Flags().Lookup("device-code"); flag == nil {
		t.Fatal("expected --device-code flag on auth command")
	}
	if flag := cmd.Flags().Lookup("device"); flag != nil {
		t.Fatal("did not expect removed --device alias")
	}
	if flag := cmd.Flags().Lookup("no-browser"); flag != nil {
		t.Fatal("did not expect removed --no-browser alias")
	}

	for _, sub := range cmd.Commands() {
		if sub.Name() == "login" {
			t.Fatalf("expected removed login subcommand, got %#v", sub)
		}
	}
}

func TestAuthStateListOnlyPrintsInvalidEntries(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	statePath, err := config.AuthStatePath()
	if err != nil {
		t.Fatalf("AuthStatePath: %v", err)
	}
	_, _, _, err = config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountID: "acc-normal", Email: "normal@example.com"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpsertOAuthStateRecord(normal): %v", err)
	}
	_, _, _, err = config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountID: "acc-expired", Email: "expired@example.com"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusExpired
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpsertOAuthStateRecord(expired): %v", err)
	}

	cmd := newAuthCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"state", "list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	output := out.String()
	got := string(output)
	if !strings.Contains(got, "Found 1 invalid OAuth accounts") || !strings.Contains(got, "expired@example.com") {
		t.Fatalf("expected only expired state in output, got %q", got)
	}
	if strings.Contains(got, "normal@example.com") {
		t.Fatalf("normal state should not be listed, got %q", got)
	}
}

func TestAuthStateCleanPrintsRemovedEmail(t *testing.T) {
	configHome := t.TempDir()
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	if err := os.Setenv("XDG_CONFIG_HOME", configHome); err != nil {
		t.Fatalf("Setenv(XDG_CONFIG_HOME): %v", err)
	}
	defer func() {
		if oldXDG == "" {
			_ = os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			_ = os.Setenv("XDG_CONFIG_HOME", oldXDG)
		}
	}()

	statePath, err := config.AuthStatePath()
	if err != nil {
		t.Fatalf("AuthStatePath: %v", err)
	}
	_, _, _, err = config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountID: "acc-1", Email: "expired@example.com"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusExpired
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	cmd := newAuthCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"state", "clean"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	output := out.String()
	if !strings.Contains(string(output), "expired@example.com") {
		t.Fatalf("expected output to contain removed email, got %q", string(output))
	}
}

func TestAuthStateCleanRemovesCredentialMarkedExpiredAfterMissingRefreshToken(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	authPath, err := config.AuthPath()
	if err != nil {
		t.Fatalf("AuthPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(auth dir): %v", err)
	}
	if err := os.WriteFile(authPath, []byte(`openai:
  - access: stale-access
    account_id: acc-1
  - access: good-access
    account_id: acc-2
`), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	statePath, err := config.AuthStatePath()
	if err != nil {
		t.Fatalf("AuthStatePath: %v", err)
	}
	_, _, _, err = config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountID: "acc-1"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.AccountID = "acc-1"
		record.Status = config.OAuthStatusExpired
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	cmd := newAuthCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"state", "clean"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	output := out.String()
	if !strings.Contains(string(output), "Removed 1 invalid OAuth credentials") {
		t.Fatalf("expected clean to remove one auth credential, got %q", string(output))
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	if got := len(auth["openai"]); got != 1 {
		t.Fatalf("remaining openai credentials = %d, want 1", got)
	}
	if got := auth["openai"][0].OAuth; got == nil || got.AccountID != "acc-2" {
		t.Fatalf("remaining OAuth credential = %#v, want acc-2", got)
	}
	state, err := config.LoadAuthState(statePath)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	if _, ok := config.FindOAuthStateRecord(state, config.OAuthStateKey{Provider: "openai", AccountID: "acc-1"}); ok {
		t.Fatal("expired state record should be removed")
	}
}

func TestAuthStateCleanKeepsMatchedRefreshOnlyState(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	authPath, err := config.AuthPath()
	if err != nil {
		t.Fatalf("AuthPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(auth dir): %v", err)
	}
	if err := os.WriteFile(authPath, []byte(`openai:
  - refresh: refresh-only
`), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	statePath, err := config.AuthStatePath()
	if err != nil {
		t.Fatalf("AuthStatePath: %v", err)
	}
	refreshStateKey := config.OAuthRefreshStateKey("refresh-only")
	_, _, _, err = config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", RefreshSHA256: refreshStateKey}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	cmd := newAuthCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"state", "clean"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	output := out.String()
	if !strings.Contains(string(output), "Removed 0 invalid/orphan OAuth state entries") {
		t.Fatalf("expected clean to keep matched refresh-only state, got %q", string(output))
	}
	state, err := config.LoadAuthState(statePath)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	if _, ok := config.FindOAuthStateRecord(state, config.OAuthStateKey{Provider: "openai", RefreshSHA256: refreshStateKey}); !ok {
		t.Fatal("matched refresh-only state record should remain")
	}
}

func TestAuthStateCleanRemovesOrphanState(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	authPath, err := config.AuthPath()
	if err != nil {
		t.Fatalf("AuthPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(auth dir): %v", err)
	}
	if err := os.WriteFile(authPath, []byte(`openai:
  - access: good-access
    account_id: acc-2
`), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	statePath, err := config.AuthStatePath()
	if err != nil {
		t.Fatalf("AuthStatePath: %v", err)
	}
	_, _, _, err = config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountID: "acc-orphan", Email: "orphan@example.com"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	cmd := newAuthCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"state", "clean"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	output := out.String()
	if !strings.Contains(string(output), "Removed 1 invalid/orphan OAuth state entries") {
		t.Fatalf("expected clean to remove one orphan state, got %q", string(output))
	}
	state, err := config.LoadAuthState(statePath)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	if _, ok := config.FindOAuthStateRecord(state, config.OAuthStateKey{Provider: "openai", AccountID: "acc-orphan"}); ok {
		t.Fatal("orphan state record should be removed")
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	if got := len(auth["openai"]); got != 1 {
		t.Fatalf("auth credentials removed = %d entries remaining, want 1", got)
	}
}

func TestParseAuthBrowserPromptByte(t *testing.T) {
	tests := []struct {
		name  string
		input byte
		want  authBrowserPromptAction
		ok    bool
	}{
		{name: "enter carriage return", input: '\r', want: authBrowserPromptActionOpen, ok: true},
		{name: "enter newline", input: '\n', want: authBrowserPromptActionOpen, ok: true},
		{name: "yank lower", input: 'y', want: authBrowserPromptActionCopy, ok: true},
		{name: "yank upper", input: 'Y', want: authBrowserPromptActionCopy, ok: true},
		{name: "space alias", input: ' ', want: authBrowserPromptActionCopy, ok: true},
		{name: "ctrl-c cancel", input: 3, want: authBrowserPromptActionCancel, ok: true},
		{name: "ignore others", input: 'x', ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseAuthBrowserPromptByte(tt.input)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("action = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOpenBrowserCommandPlansPerPlatform(t *testing.T) {
	tests := []struct {
		goos     string
		wantName string
		wantArgs []string
	}{
		{goos: "darwin", wantName: "open", wantArgs: []string{"https://example.test"}},
		{goos: "windows", wantName: "rundll32", wantArgs: []string{"url.dll,FileProtocolHandler", "https://example.test"}},
		{goos: "linux", wantName: "xdg-open", wantArgs: []string{"https://example.test"}},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			name, args := openBrowserCommand(tt.goos, "https://example.test")
			if name != tt.wantName {
				t.Fatalf("name = %q, want %q", name, tt.wantName)
			}
			if !reflect.DeepEqual(args, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", args, tt.wantArgs)
			}
		})
	}
}

func TestOAuthCredentialMatchesStateEntry(t *testing.T) {
	entry := config.RemovedOAuthStateEntry{AccountID: "acct-1", Email: "user@example.com"}
	tests := []struct {
		name string
		cred *config.OAuthCredential
		want bool
	}{
		{name: "nil", cred: nil, want: false},
		{name: "account id", cred: &config.OAuthCredential{AccountID: "acct-1"}, want: true},
		{name: "email only does not match", cred: &config.OAuthCredential{Email: "user@example.com"}, want: false},
		{name: "different account with same email does not match", cred: &config.OAuthCredential{AccountID: "acct-2", Email: "user@example.com"}, want: false},
		{name: "empty fields do not match", cred: &config.OAuthCredential{}, want: false},
		{name: "different", cred: &config.OAuthCredential{AccountID: "acct-2", Email: "other@example.com", Access: "other"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oauthCredentialMatchesStateEntry(tt.cred, entry); got != tt.want {
				t.Fatalf("oauthCredentialMatchesStateEntry() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOAuthCredentialMapBackfillsMetadataFromAccessToken(t *testing.T) {
	access := testUnsignedJWT(`{"https://api.openai.com/auth": {"chatgpt_account_id": "acct-token"}, "email": "token@example.com"}`)
	got, backfills, err := oauthCredentialMap([]config.ProviderCredential{{OAuth: &config.OAuthCredential{Access: access}}})
	if err != nil {
		t.Fatalf("oauthCredentialMap: %v", err)
	}

	setup, ok := got[access]
	if !ok {
		t.Fatalf("OAuth map missing access token key: %#v", got)
	}
	if setup.AccountID != "acct-token" || setup.Email != "token@example.com" {
		t.Fatalf("setup metadata = (%q, %q), want token metadata", setup.AccountID, setup.Email)
	}
	if len(backfills) != 1 || backfills[0].AccountID != "acct-token" || backfills[0].Email != "token@example.com" {
		t.Fatalf("backfills = %#v, want parsed metadata", backfills)
	}
}

func TestOAuthCredentialMapRejectsAccessTokenWithoutAccountID(t *testing.T) {
	access := testUnsignedJWT(`{"exp":4102444800,"email":"token@example.com"}`)
	got, backfills, err := oauthCredentialMap([]config.ProviderCredential{{OAuth: &config.OAuthCredential{Access: access, Email: "stored@example.com"}}})
	if err == nil {
		t.Fatalf("oauthCredentialMap succeeded with map=%#v backfills=%#v, want missing account_id error", got, backfills)
	}
	if !strings.Contains(err.Error(), "missing account_id") {
		t.Fatalf("error = %v, want missing account_id", err)
	}
}

func TestOAuthCredentialMapRejectsMismatchedAccountID(t *testing.T) {
	access := testUnsignedJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-token"}}`)
	got, backfills, err := oauthCredentialMap([]config.ProviderCredential{{OAuth: &config.OAuthCredential{Access: access, AccountID: "acct-other"}}})
	if err == nil {
		t.Fatalf("oauthCredentialMap succeeded with map=%#v backfills=%#v, want mismatch error", got, backfills)
	}
	if !strings.Contains(err.Error(), "does not match configured account_id") {
		t.Fatalf("error = %v, want mismatch", err)
	}
}

func TestOAuthCredentialMapRefreshOnlyUsesRefreshStateKey(t *testing.T) {
	got, backfills, err := oauthCredentialMap([]config.ProviderCredential{
		{APIKey: "", ExplicitEmpty: true},
		{OAuth: &config.OAuthCredential{Refresh: "refresh-only", AccountID: "acc-refresh"}},
	})
	if err != nil {
		t.Fatalf("oauthCredentialMap: %v", err)
	}
	if _, ok := got["key_slot:0"]; ok {
		t.Fatalf("explicit empty API key slot should not be mapped as OAuth: %#v", got)
	}
	stateKey := config.OAuthRefreshStateKey("refresh-only")
	setup, ok := got[stateKey]
	if !ok {
		t.Fatalf("refresh-only OAuth setup missing %s: %#v", stateKey, got)
	}
	if setup.CredentialIndex != 1 || setup.AccountID != "acc-refresh" || setup.RefreshSHA256 != stateKey {
		t.Fatalf("unexpected refresh-only setup: %#v", setup)
	}
	if len(backfills) != 0 {
		t.Fatalf("backfills = %#v, want none", backfills)
	}
}

func TestOAuthCredentialMapDoesNotBackfillWhenMetadataAlreadyPresent(t *testing.T) {
	access := testUnsignedJWT(`{"https://api.openai.com/auth": {"chatgpt_account_id": "acct-existing"}, "email": "token@example.com"}`)
	got, backfills, err := oauthCredentialMap([]config.ProviderCredential{{OAuth: &config.OAuthCredential{Access: access, AccountID: "acct-existing", Email: "existing@example.com"}}})
	if err != nil {
		t.Fatalf("oauthCredentialMap: %v", err)
	}

	setup := got[access]
	if setup.AccountID != "acct-existing" || setup.Email != "token@example.com" {
		t.Fatalf("setup metadata = (%q, %q), want token-compatible metadata", setup.AccountID, setup.Email)
	}
	if len(backfills) != 0 {
		t.Fatalf("backfills = %#v, want none", backfills)
	}
}

func TestPersistOAuthMetadataBackfillsUpdatesAuthFileAndMemory(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	access := testUnsignedJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-token"},"email":"token@example.com"}`)
	auth := config.AuthConfig{
		"codex": {
			{OAuth: &config.OAuthCredential{Access: access, Refresh: "refresh-123"}},
		},
	}
	if err := config.SaveAuthConfig(authPath, auth); err != nil {
		t.Fatalf("SaveAuthConfig: %v", err)
	}
	_, backfills, err := oauthCredentialMap(auth["codex"])
	if err != nil {
		t.Fatalf("oauthCredentialMap: %v", err)
	}
	var mu sync.Mutex
	if err := persistOAuthMetadataBackfills(authPath, &auth, &mu, "codex", backfills); err != nil {
		t.Fatalf("persistOAuthMetadataBackfills: %v", err)
	}
	if got := auth["codex"][0].OAuth.AccountID; got != "acct-token" {
		t.Fatalf("in-memory account_id = %q", got)
	}
	if got := auth["codex"][0].OAuth.Email; got != "token@example.com" {
		t.Fatalf("in-memory email = %q", got)
	}
	loaded, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	if got := loaded["codex"][0].OAuth.AccountID; got != "acct-token" {
		t.Fatalf("persisted account_id = %q", got)
	}
	if got := loaded["codex"][0].OAuth.Email; got != "token@example.com" {
		t.Fatalf("persisted email = %q", got)
	}
	if err := persistOAuthMetadataBackfills(authPath, &auth, &mu, "codex", []oauthMetadataBackfill{{Email: "ignored@example.com"}}); err != nil {
		t.Fatalf("persistOAuthMetadataBackfills empty account id: %v", err)
	}
}

func testUnsignedJWT(payload string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

func base64Payload(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}
