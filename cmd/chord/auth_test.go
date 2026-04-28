package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	}, "")
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

	err := runOpenAICodexDeviceLogin("codex", srv.Client(), serverURL+"/oauth/token", "client-123")
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

	loginCmd, _, err := cmd.Find([]string{"login"})
	if err != nil {
		t.Fatalf("Find login: %v", err)
	}
	if loginCmd == nil || loginCmd.Name() != "login" {
		t.Fatalf("expected hidden login subcommand, got %#v", loginCmd)
	}
	if !loginCmd.Hidden {
		t.Fatal("expected login subcommand to be hidden")
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

func base64Payload(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}
