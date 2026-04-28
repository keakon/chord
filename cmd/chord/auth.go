package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

const (
	openAIOAuthCallbackHost = "127.0.0.1"
	// Keep 1455 as the default to match Codex CLI behavior.
	openAIOAuthCallbackDefaultPort = 1455
	// Use localhost in redirect_uri to align with Codex OAuth allowlist expectations.
	openAIOAuthRedirectHost = "localhost"
	// Codex login requires connector scopes in addition to the base OIDC scopes.
	openAIOAuthScope      = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	openAIOAuthTimeout    = 5 * time.Minute
	openAIOAuthOriginator = "codex_cli_rs"
)

var (
	flagAuthProvider   string
	flagAuthDeviceCode bool
)

type openAIPKCECodes struct {
	Verifier  string
	Challenge string
}

type openAITokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type openAICodexDeviceCodeResponse struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
	Interval     string `json:"interval"`
}

type openAICodexDeviceAuthorizationCodeResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

type authBrowserPromptAction int

const (
	authBrowserPromptActionOpen authBrowserPromptAction = iota
	authBrowserPromptActionCopy
	authBrowserPromptActionCancel
)

type authBrowserPrompt struct {
	actions <-chan authBrowserPromptAction
	close   func()
}

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth [provider]",
		Short: "Manage provider authentication",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runAuthLogin,
	}
	bindAuthLoginFlags(cmd)
	cmd.AddCommand(newAuthLoginCmd())
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "login [provider]",
		Short:  "Sign in with a preset: codex provider",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE:   runAuthLogin,
	}
	bindAuthLoginFlags(cmd)
	return cmd
}

func bindAuthLoginFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&flagAuthProvider, "provider", "", "Provider name to store preset: codex OAuth credentials under")
	cmd.Flags().BoolVar(&flagAuthDeviceCode, "device-code", false, "Use preset: codex device-code login instead of browser callback OAuth")
	_ = cmd.Flags().MarkHidden("provider")
}

func runAuthLogin(cmd *cobra.Command, args []string) error {
	preferred, err := resolveAuthLoginProviderName(cmd.CommandPath(), flagAuthProvider, args)
	if err != nil {
		return err
	}
	providerName, providerCfg, globalProxy, err := resolveAuthLoginProvider(preferred, os.Stdin, os.Stderr)
	if err != nil {
		return err
	}
	if flagAuthDeviceCode {
		return runAuthLoginDevice(providerName, providerCfg, globalProxy)
	}
	return runAuthLoginBrowser(providerName, providerCfg, globalProxy)
}

func runAuthLoginBrowser(providerName string, providerCfg config.ProviderConfig, globalProxy string) error {
	return runAuthLoginBrowserWithIO(os.Stdin, os.Stderr, providerName, providerCfg, globalProxy, openBrowser, clipboard.WriteAll)
}

func runAuthLoginBrowserWithIO(
	in *os.File,
	out io.Writer,
	providerName string,
	providerCfg config.ProviderConfig,
	globalProxy string,
	openFn func(string) error,
	copyFn func(string) error,
) error {
	pkce, err := generateOpenAIPKCE()
	if err != nil {
		return fmt.Errorf("generate PKCE: %w", err)
	}
	state, err := generateOpenAIState()
	if err != nil {
		return fmt.Errorf("generate OAuth state: %w", err)
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	server, redirectURI, err := startOpenAICallbackServer(state, openAIOAuthCallbackDefaultPort, codeCh, errCh)
	if err != nil {
		return err
	}
	authURL := buildOpenAIAuthorizeURL(pkce, state, redirectURI)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(out, "OpenAI OAuth URL:\n%s\n\n", authURL)

	prompt, err := startAuthBrowserPrompt(in)
	if err != nil {
		fmt.Fprintf(out, "Interactive browser shortcuts unavailable; continue manually: %v\n", err)
	}
	var promptActions <-chan authBrowserPromptAction
	if prompt != nil {
		defer prompt.close()
		promptActions = prompt.actions
		fmt.Fprint(out, "Press Enter to open the URL in your default browser.\r\n")
		fmt.Fprint(out, "Press y or Space to copy the URL to the clipboard.\r\n")
		fmt.Fprint(out, "Or open the URL manually in another browser or an incognito window.\r\n")
		fmt.Fprint(out, "Press Ctrl+C to cancel.\r\n\r\n")
	}
	fmt.Fprintf(out, "Waiting for browser callback at %s ...\n", redirectURI)

	var code string
	timer := time.NewTimer(openAIOAuthTimeout)
	defer timer.Stop()
	for {
		select {
		case code = <-codeCh:
			if prompt != nil {
				prompt.close()
			}
			goto exchange
		case err := <-errCh:
			if prompt != nil {
				prompt.close()
			}
			return err
		case <-timer.C:
			if prompt != nil {
				prompt.close()
			}
			return fmt.Errorf("timed out waiting for OAuth callback (%s)", openAIOAuthTimeout)
		case action, ok := <-promptActions:
			if !ok {
				prompt = nil
				promptActions = nil
				continue
			}
			switch action {
			case authBrowserPromptActionOpen:
				if err := openFn(authURL); err != nil {
					fmt.Fprintf(out, "Could not open the browser automatically. Open the URL above manually: %v\r\n", err)
					continue
				}
				fmt.Fprint(out, "Opened the URL in your default browser.\r\n")
			case authBrowserPromptActionCopy:
				if err := copyFn(authURL); err != nil {
					fmt.Fprintf(out, "Could not copy the URL to the clipboard. Copy it manually: %v\r\n", err)
					continue
				}
				fmt.Fprint(out, "Copied the URL to the clipboard.\r\n")
			case authBrowserPromptActionCancel:
				prompt.close()
				return context.Canceled
			}
		}
	}

exchange:
	client, err := buildOpenAILoginHTTPClient(providerCfg, globalProxy)
	if err != nil {
		return err
	}
	tokens, err := exchangeOpenAICodeForTokens(client, code, pkce.Verifier, redirectURI)
	if err != nil {
		return err
	}

	cred, authPath, err := persistOAuthCredential(providerName, tokens.IDToken, tokens.AccessToken, tokens.RefreshToken, tokens.ExpiresIn)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nLogin successful. Credentials written to %s\n", authPath)
	fmt.Fprintf(out, "provider: %s\n", providerName)
	if cred.Email != "" {
		fmt.Fprintf(out, "email: %s\n", cred.Email)
	}
	if cred.AccountID != "" {
		fmt.Fprintf(out, "account_id: %s\n", cred.AccountID)
	}
	return nil
}

func runAuthLoginDevice(providerName string, providerCfg config.ProviderConfig, globalProxy string) error {
	normalizedCfg, meta, err := config.NormalizeOpenAICodexProvider(providerCfg, false)
	if err != nil {
		return fmt.Errorf("provider %q has invalid OAuth config: %w", providerName, err)
	}
	if !meta.Enabled {
		return fmt.Errorf("provider %q is not configured for preset: codex login; configure `preset: codex`", providerName)
	}

	client, err := buildOpenAILoginHTTPClient(normalizedCfg, globalProxy)
	if err != nil {
		return err
	}
	return runOpenAICodexDeviceLogin(providerName, client, normalizedCfg.TokenURL, normalizedCfg.ClientID)
}

func runOpenAICodexDeviceLogin(
	providerName string,
	client *http.Client,
	tokenURL string,
	clientID string,
) error {
	issuer := strings.TrimSuffix(strings.TrimRight(tokenURL, "/"), "/oauth/token")
	if issuer == "" || issuer == tokenURL {
		return fmt.Errorf("provider %q has unsupported token_url %q for built-in Codex device login", providerName, tokenURL)
	}

	deviceResp, err := requestOpenAICodexDeviceCode(client, issuer, clientID)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Device login URL:\n%s\n", issuer+"/codex/device")
	fmt.Fprintf(os.Stderr, "\nuser_code: %s\n", deviceResp.UserCode)
	fmt.Fprintln(os.Stderr, "Complete authorization on another device that is already signed in.")

	interval := openAICodexDevicePollingInterval(deviceResp.Interval)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	codeResp, err := pollOpenAICodexDeviceAuthorizationCode(
		ctx,
		client,
		issuer,
		deviceResp.DeviceAuthID,
		deviceResp.UserCode,
		interval,
	)
	if err != nil {
		return err
	}

	tokens, err := exchangeOpenAICodeForTokensWithParams(
		client,
		tokenURL,
		issuer+"/deviceauth/callback",
		clientID,
		codeResp.AuthorizationCode,
		codeResp.CodeVerifier,
	)
	if err != nil {
		return err
	}

	cred, authPath, err := persistOAuthCredential(providerName, tokens.IDToken, tokens.AccessToken, tokens.RefreshToken, tokens.ExpiresIn)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nLogin successful. Credentials written to %s\n", authPath)
	fmt.Fprintf(os.Stderr, "provider: %s\n", providerName)
	if cred.Email != "" {
		fmt.Fprintf(os.Stderr, "email: %s\n", cred.Email)
	}
	if cred.AccountID != "" {
		fmt.Fprintf(os.Stderr, "account_id: %s\n", cred.AccountID)
	}
	return nil
}

func resolveAuthLoginProviderName(commandPath string, flagValue string, args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("%s accepts at most one provider argument", commandPath)
	}
	argValue := ""
	if len(args) == 1 {
		argValue = strings.TrimSpace(args[0])
	}
	flagValue = strings.TrimSpace(flagValue)
	if argValue != "" && flagValue != "" && argValue != flagValue {
		return "", fmt.Errorf("provider mismatch between positional argument %q and --provider=%q", argValue, flagValue)
	}
	if argValue != "" {
		return argValue, nil
	}
	return flagValue, nil
}

func loadAuthLoginProviders() (map[string]config.ProviderConfig, string, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load global config: %w", err)
	}
	allProviders := make(map[string]config.ProviderConfig, len(cfg.Providers))
	for name, providerCfg := range cfg.Providers {
		allProviders[name] = providerCfg
	}

	cwd, err := os.Getwd()
	if err == nil {
		projectConfigPath := filepath.Join(cwd, ".chord", "config.yaml")
		if _, statErr := os.Stat(projectConfigPath); statErr == nil {
			projectCfg, loadErr := config.LoadConfigFromPath(projectConfigPath)
			if loadErr != nil {
				return nil, "", fmt.Errorf("load project config: %w", loadErr)
			}
			for name, providerCfg := range projectCfg.Providers {
				allProviders[name] = providerCfg
			}
		}
	}

	return allProviders, cfg.Proxy, nil
}

func eligibleAuthLoginProviders(allProviders map[string]config.ProviderConfig) []string {
	eligible := make([]string, 0, len(allProviders))
	for name, providerCfg := range allProviders {
		if strings.EqualFold(strings.TrimSpace(providerCfg.Preset), config.ProviderPresetCodex) {
			eligible = append(eligible, name)
		}
	}
	sort.Strings(eligible)
	return eligible
}

func promptAuthLoginProvider(in io.Reader, out io.Writer, providers []string) (string, error) {
	if len(providers) == 0 {
		return "", fmt.Errorf("no providers available for selection")
	}
	if len(providers) == 1 {
		fmt.Fprintf(out, "Using the only available preset: codex provider: %s\n", providers[0])
		return providers[0], nil
	}

	fmt.Fprintln(out, "Available preset: codex providers:")
	for i, name := range providers {
		fmt.Fprintf(out, "  %d. %s\n", i+1, name)
	}
	fmt.Fprint(out, "Select a provider by number or name: ")

	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		choice := strings.TrimSpace(scanner.Text())
		if choice == "" {
			fmt.Fprint(out, "Enter a valid provider number or name: ")
			continue
		}
		if idx, convErr := strconv.Atoi(choice); convErr == nil {
			if idx >= 1 && idx <= len(providers) {
				return providers[idx-1], nil
			}
		}
		for _, name := range providers {
			if choice == name {
				return name, nil
			}
		}
		fmt.Fprint(out, "Invalid selection. Enter a provider number or name: ")
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read provider selection: %w", err)
	}
	return "", fmt.Errorf("no provider was selected; rerun with `chord auth <provider>`")
}

func resolveAuthLoginProvider(
	preferred string,
	in io.Reader,
	out io.Writer,
) (string, config.ProviderConfig, string, error) {
	allProviders, globalProxy, err := loadAuthLoginProviders()
	if err != nil {
		return "", config.ProviderConfig{}, "", err
	}
	eligible := eligibleAuthLoginProviders(allProviders)

	if preferred == "" {
		if len(eligible) == 0 {
			return "", config.ProviderConfig{}, "", fmt.Errorf("no providers are configured for preset: codex login; add an OpenAI provider with `preset: codex` to config.yaml")
		}
		selected, promptErr := promptAuthLoginProvider(in, out, eligible)
		if promptErr != nil {
			return "", config.ProviderConfig{}, "", promptErr
		}
		preferred = selected
	}

	providerCfg, ok := allProviders[preferred]
	if !ok {
		if len(eligible) == 0 {
			return "", config.ProviderConfig{}, "", fmt.Errorf("provider %q was not found and no providers are configured for preset: codex login", preferred)
		}
		return "", config.ProviderConfig{}, "", fmt.Errorf("provider %q was not found; available preset: codex providers: %s", preferred, strings.Join(eligible, ", "))
	}

	normalizedCfg, meta, err := config.NormalizeOpenAICodexProvider(providerCfg, false)
	if err != nil {
		return "", config.ProviderConfig{}, "", fmt.Errorf("provider %q has invalid OAuth config: %w", preferred, err)
	}
	if !meta.Enabled {
		return "", config.ProviderConfig{}, "", fmt.Errorf("provider %q is not configured for preset: codex login; configure `preset: codex`", preferred)
	}
	return preferred, normalizedCfg, globalProxy, nil
}

func persistOAuthCredential(providerName, idToken, accessToken, refreshToken string, expiresIn int64) (*config.OAuthCredential, string, error) {
	if accessToken == "" || refreshToken == "" {
		return nil, "", fmt.Errorf("OAuth response missing access_token or refresh_token")
	}

	accountID := config.ExtractOAuthAccountIDFromToken(idToken)
	if accountID == "" {
		accountID = config.ExtractOAuthAccountIDFromToken(accessToken)
	}
	if accountID == "" {
		return nil, "", fmt.Errorf("OAuth response missing account_id claim")
	}
	email := config.ExtractOAuthEmailFromToken(idToken)
	if email == "" {
		email = config.ExtractOAuthEmailFromToken(accessToken)
	}
	cred := &config.OAuthCredential{
		Refresh:   refreshToken,
		Access:    accessToken,
		Expires:   time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli(),
		AccountID: accountID,
		Email:     email,
	}

	configHome, err := config.ConfigHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("resolve config home: %w", err)
	}
	if err := os.MkdirAll(configHome, 0o700); err != nil {
		return nil, "", fmt.Errorf("create config home: %w", err)
	}
	authPath := filepath.Join(configHome, "auth.yaml")
	if _, err := config.UpsertOAuthCredentialInFile(authPath, providerName, cred); err != nil {
		return nil, "", fmt.Errorf("save auth config: %w", err)
	}
	return cred, authPath, nil
}

func generateOpenAIPKCE() (*openAIPKCECodes, error) {
	verifier, err := generateOpenAIRandomString(43)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	return &openAIPKCECodes{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

func generateOpenAIState() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func generateOpenAIRandomString(length int) (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = chars[int(buf[i])%len(chars)]
	}
	return string(buf), nil
}

func buildOpenAIAuthorizeURL(pkce *openAIPKCECodes, state string, redirectURI string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", config.OpenAIOAuthClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", openAIOAuthScope)
	params.Set("code_challenge", pkce.Challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	params.Set("originator", openAIOAuthOriginator)
	params.Set("state", state)
	return config.OpenAIOAuthAuthorizeURL + "?" + params.Encode()
}

func startOpenAICallbackServer(
	expectedState string,
	preferredPort int,
	codeCh chan<- string,
	errCh chan<- error,
) (*http.Server, string, error) {
	mux := http.NewServeMux()
	listenAddr := openAIOAuthCallbackListenAddr(preferredPort)
	server := &http.Server{Addr: listenAddr, Handler: mux}
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
			errDescription := r.URL.Query().Get("error_description")
			if errDescription == "" {
				errDescription = oauthErr
			}
			select {
			case errCh <- fmt.Errorf("OAuth login failed: %s", errDescription):
			default:
			}
			http.Error(w, errDescription, http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("state") != expectedState {
			select {
			case errCh <- fmt.Errorf("OAuth state verification failed"):
			default:
			}
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			select {
			case errCh <- fmt.Errorf("OAuth callback missing code"):
			default:
			}
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		select {
		case codeCh <- code:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><h1>Login successful</h1><p>You can close this page and return to chord.</p></body></html>`))
	})

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, "", fmt.Errorf("start OAuth callback server on %s: %w", listenAddr, err)
	}
	actualPort, err := openAIOAuthListenerPort(listener.Addr())
	if err != nil {
		_ = listener.Close()
		return nil, "", err
	}
	redirectURI := openAIOAuthCallbackRedirectURI(actualPort)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			select {
			case errCh <- fmt.Errorf("OAuth callback server error: %w", serveErr):
			default:
			}
		}
	}()
	return server, redirectURI, nil
}

func openAIOAuthCallbackListenAddr(port int) string {
	return net.JoinHostPort(openAIOAuthCallbackHost, strconv.Itoa(port))
}

func openAIOAuthListenerPort(addr net.Addr) (int, error) {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok || tcpAddr == nil || tcpAddr.Port <= 0 {
		return 0, fmt.Errorf("resolve OAuth callback port from listener address %q", addr.String())
	}
	return tcpAddr.Port, nil
}

func openAIOAuthCallbackRedirectURI(port int) string {
	return fmt.Sprintf("http://%s:%d/auth/callback", openAIOAuthRedirectHost, port)
}

func buildOpenAILoginHTTPClient(providerCfg config.ProviderConfig, globalProxy string) (*http.Client, error) {
	effectiveProxy := llm.ResolveEffectiveProxy(providerCfg.Proxy, globalProxy)
	client, err := llm.NewHTTPClientWithProxy(effectiveProxy, 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("create login HTTP client: %w", err)
	}
	return client, nil
}

func requestOpenAICodexDeviceCode(client *http.Client, issuer, clientID string) (*openAICodexDeviceCodeResponse, error) {
	body, err := json.Marshal(map[string]string{"client_id": clientID})
	if err != nil {
		return nil, fmt.Errorf("marshal device code request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, issuer+"/api/accounts/deviceauth/usercode", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("build device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", openAIOAuthUserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read device code response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("device code login is not enabled for this Codex server")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request device code failed: %s", strings.TrimSpace(string(raw)))
	}

	var payload openAICodexDeviceCodeResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse device code response: %w", err)
	}
	if payload.DeviceAuthID == "" || payload.UserCode == "" {
		return nil, fmt.Errorf("device code response missing device_auth_id or user_code")
	}
	return &payload, nil
}

func pollOpenAICodexDeviceAuthorizationCode(
	ctx context.Context,
	client *http.Client,
	issuer string,
	deviceAuthID string,
	userCode string,
	interval time.Duration,
) (*openAICodexDeviceAuthorizationCodeResponse, error) {
	return pollOpenAICodexDeviceAuthorizationCodeWithWait(ctx, client, issuer, deviceAuthID, userCode, interval, waitForDevicePoll)
}

func pollOpenAICodexDeviceAuthorizationCodeWithWait(
	ctx context.Context,
	client *http.Client,
	issuer string,
	deviceAuthID string,
	userCode string,
	interval time.Duration,
	waitFn func(context.Context, time.Duration) error,
) (*openAICodexDeviceAuthorizationCodeResponse, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		codeResp, statusCode, err := requestOpenAICodexDeviceAuthorizationCode(client, issuer, deviceAuthID, userCode)
		if err != nil {
			return nil, err
		}
		if statusCode == http.StatusOK {
			return codeResp, nil
		}
		if statusCode != http.StatusForbidden && statusCode != http.StatusNotFound {
			return nil, fmt.Errorf("device authorization failed with status %d", statusCode)
		}
		if err := waitFn(ctx, interval); err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("device authorization timed out before completion")
			}
			return nil, fmt.Errorf("poll device authorization: %w", err)
		}
	}
}

func requestOpenAICodexDeviceAuthorizationCode(
	client *http.Client,
	issuer string,
	deviceAuthID string,
	userCode string,
) (*openAICodexDeviceAuthorizationCodeResponse, int, error) {
	body, err := json.Marshal(map[string]string{
		"device_auth_id": deviceAuthID,
		"user_code":      userCode,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("marshal device authorization token request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, issuer+"/api/accounts/deviceauth/token", strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, fmt.Errorf("build device authorization token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", openAIOAuthUserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request device authorization token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, resp.StatusCode, nil
	}

	var payload openAICodexDeviceAuthorizationCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, 0, fmt.Errorf("parse device authorization token response: %w", err)
	}
	if payload.AuthorizationCode == "" || payload.CodeVerifier == "" {
		return nil, 0, fmt.Errorf("device authorization token response missing authorization_code or code_verifier")
	}
	return &payload, resp.StatusCode, nil
}

func openAICodexDevicePollingInterval(intervalValue string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(intervalValue))
	if err != nil || seconds < 1 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}

func waitForDevicePoll(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func exchangeOpenAICodeForTokens(client *http.Client, code string, verifier string, redirectURI string) (*openAITokenResponse, error) {
	return exchangeOpenAICodeForTokensWithParams(
		client,
		config.OpenAIOAuthTokenURL,
		redirectURI,
		config.OpenAIOAuthClientID,
		code,
		verifier,
	)
}

func exchangeOpenAICodeForTokensWithParams(
	client *http.Client,
	tokenURL string,
	redirectURI string,
	clientID string,
	code string,
	verifier string,
) (*openAITokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", openAIOAuthUserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token exchange response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: %s", strings.TrimSpace(string(body)))
	}

	var tokens openAITokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("parse token exchange response: %w", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		return nil, fmt.Errorf("token exchange response missing access_token or refresh_token")
	}
	return &tokens, nil
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if cmd.Stdout == nil {
		cmd.Stdout = io.Discard
	}
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	return cmd.Start()
}

func startAuthBrowserPrompt(in *os.File) (*authBrowserPrompt, error) {
	if in == nil || !term.IsTerminal(in.Fd()) {
		return nil, nil
	}

	state, err := term.MakeRaw(in.Fd())
	if err != nil {
		return nil, fmt.Errorf("enable raw mode: %w", err)
	}
	reader, err := cancelreader.NewReader(in)
	if err != nil {
		_ = term.Restore(in.Fd(), state)
		return nil, fmt.Errorf("create cancelable reader: %w", err)
	}

	actions := make(chan authBrowserPromptAction, 8)
	var once sync.Once
	closePrompt := func() {
		once.Do(func() {
			reader.Cancel()
			_ = reader.Close()
			_ = term.Restore(in.Fd(), state)
		})
	}

	go func() {
		defer close(actions)
		defer closePrompt()
		var buf [1]byte
		for {
			n, err := reader.Read(buf[:])
			if err != nil {
				if errors.Is(err, cancelreader.ErrCanceled) || errors.Is(err, os.ErrClosed) {
					return
				}
				return
			}
			if n == 0 {
				continue
			}
			action, ok := parseAuthBrowserPromptByte(buf[0])
			if !ok {
				continue
			}
			actions <- action
		}
	}()

	return &authBrowserPrompt{
		actions: actions,
		close:   closePrompt,
	}, nil
}

func parseAuthBrowserPromptByte(b byte) (authBrowserPromptAction, bool) {
	switch b {
	case '\r', '\n':
		return authBrowserPromptActionOpen, true
	case 'y', 'Y', ' ':
		return authBrowserPromptActionCopy, true
	case 3:
		return authBrowserPromptActionCancel, true
	default:
		return 0, false
	}
}

func openAIOAuthUserAgent() string {
	return fmt.Sprintf("%s/0.0.1 (%s %s)", openAIOAuthOriginator, runtime.GOOS, runtime.GOARCH)
}
