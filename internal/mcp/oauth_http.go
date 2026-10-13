package mcp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	oauthRequestTimeout       = 20 * time.Second
	oauthMaxBodyBytes         = 1 << 20
	oauthMaxHeaderBytes       = 64 << 10
	oauthMaxMetadataRedirects = 3
)

// OAuthHTTPOptions permits deterministic DNS, TLS and dial testing. The same
// public-address validation is applied to injected and system DNS results.
type OAuthHTTPOptions struct {
	ResourceEndpoint string
	LookupIP         func(context.Context, string) ([]net.IPAddr, error)
	DialContext      func(context.Context, string, string) (net.Conn, error)
	RootCAs          *x509.CertPool
}

// OAuthHTTPClient uses direct, pinned-IP connections independently of the MCP
// business transport's proxy configuration. It never retries control POSTs.
type OAuthHTTPClient struct{ options OAuthHTTPOptions }

func NewOAuthHTTPClient(options OAuthHTTPOptions) *OAuthHTTPClient {
	if options.LookupIP == nil {
		options.LookupIP = net.DefaultResolver.LookupIPAddr
	}
	if options.DialContext == nil {
		options.DialContext = (&net.Dialer{Timeout: oauthRequestTimeout}).DialContext
	}
	if options.RootCAs != nil {
		options.RootCAs = options.RootCAs.Clone()
	}
	return &OAuthHTTPClient{options: options}
}

// ForResource returns an immutable per-target policy without broadening the
// authorization server rules or mutating the shared client.
func (c *OAuthHTTPClient) ForResource(endpoint string) *OAuthHTTPClient {
	options := c.options
	options.ResourceEndpoint = endpoint
	return &OAuthHTTPClient{options: options}
}

// OAuthHTTPError exposes bounded protocol facts, never the response body or URL.
type OAuthHTTPError struct {
	StatusCode   int
	ErrorClass   ErrorClass
	OAuthError   string
	SendEvidence SendEvidence
	RetryAfter   time.Duration
	cause        error
}

func (e *OAuthHTTPError) Error() string { return "mcp oauth: " + string(e.ErrorClass) }
func (e *OAuthHTTPError) Unwrap() error { return e.cause }

func oauthPolicyError() error {
	return &OAuthHTTPError{ErrorClass: ErrorNetworkPolicyDenied, SendEvidence: SendNotSent}
}

// ValidateEndpoint applies the authorization-server policy without any local
// resource exception, including to URLs which will be opened in a browser.
func (c *OAuthHTTPClient) ValidateEndpoint(ctx context.Context, raw string) error {
	_, _, err := c.resolveEndpoint(ctx, raw, false)
	return err
}

func parseOAuthEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") || strings.Contains(u.Host, "%") {
		return nil, oauthPolicyError()
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, oauthPolicyError()
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, oauthPolicyError()
		}
	}
	for _, r := range raw {
		if r < 0x21 || r == 0x7f {
			return nil, oauthPolicyError()
		}
	}
	return u, nil
}

func exactOAuthOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func (c *OAuthHTTPClient) localResourceException(u *url.URL) bool {
	resource, err := parseOAuthEndpoint(c.options.ResourceEndpoint)
	if err != nil || resource.Scheme != "http" || !exactOAuthOrigin(resource, u) {
		return false
	}
	host := resource.Hostname()
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}

func (c *OAuthHTTPClient) resolveEndpoint(ctx context.Context, raw string, allowResource bool) (*url.URL, []net.IPAddr, error) {
	u, err := parseOAuthEndpoint(raw)
	if err != nil {
		return nil, nil, err
	}
	local := allowResource && c.localResourceException(u)
	if u.Scheme != "https" && !local {
		return nil, nil, oauthPolicyError()
	}
	host := u.Hostname()
	var ips []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		// IPv4-mapped literals can hide their actual address family.
		if strings.Contains(host, ":") && ip.To4() != nil {
			return nil, nil, oauthPolicyError()
		}
		ips = []net.IPAddr{{IP: ip}}
	} else {
		ips, err = c.options.LookupIP(ctx, host)
		if err != nil {
			return nil, nil, &OAuthHTTPError{ErrorClass: ErrorNetwork, SendEvidence: SendNotSent, cause: err}
		}
	}
	if len(ips) == 0 {
		return nil, nil, &OAuthHTTPError{ErrorClass: ErrorNetwork, SendEvidence: SendNotSent}
	}
	for _, ip := range ips {
		if ip.Zone != "" || ip.IP == nil {
			return nil, nil, oauthPolicyError()
		}
		if local {
			if !ip.IP.IsLoopback() {
				return nil, nil, oauthPolicyError()
			}
			continue
		}
		if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsUnspecified() {
			return nil, nil, oauthPolicyError()
		}
	}
	return u, ips, nil
}

// GetJSON follows at most three metadata redirects, validating each destination.
func (c *OAuthHTTPClient) GetJSON(ctx context.Context, endpoint string, out any) error {
	return c.requestJSON(ctx, http.MethodGet, endpoint, "", nil, nil, out)
}
func (c *OAuthHTTPClient) PostForm(ctx context.Context, endpoint string, values url.Values, out any) error {
	return c.requestJSON(ctx, http.MethodPost, endpoint, "application/x-www-form-urlencoded", []byte(values.Encode()), nil, out)
}

type oauthBasicCredentials struct{ clientID, clientSecret string }

// PostFormWithBasic sends token endpoint credentials using OAuth's form-encoded
// HTTP Basic convention. Credentials never cross a redirect.
func (c *OAuthHTTPClient) PostFormWithBasic(ctx context.Context, endpoint string, values url.Values, clientID, clientSecret string, out any) error {
	return c.requestJSON(ctx, http.MethodPost, endpoint, "application/x-www-form-urlencoded", []byte(values.Encode()), &oauthBasicCredentials{clientID: clientID, clientSecret: clientSecret}, out)
}

func (c *OAuthHTTPClient) PostJSON(ctx context.Context, endpoint string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return &OAuthHTTPError{ErrorClass: ErrorProtocol, SendEvidence: SendNotSent, cause: err}
	}
	return c.requestJSON(ctx, http.MethodPost, endpoint, "application/json", body, nil, out)
}

func (c *OAuthHTTPClient) requestJSON(ctx context.Context, method, endpoint, contentType string, body []byte, basic *oauthBasicCredentials, out any) error {
	ctx, cancel := context.WithTimeout(ctx, oauthRequestTimeout)
	defer cancel()
	for redirects := 0; ; redirects++ {
		u, ips, err := c.resolveEndpoint(ctx, endpoint, method == http.MethodGet)
		if err != nil {
			return err
		}
		resp, err := c.roundTrip(ctx, method, u, ips, contentType, body, basic)
		if err != nil {
			return err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if method != http.MethodGet || redirects >= oauthMaxMetadataRedirects || location == "" {
				return &OAuthHTTPError{StatusCode: resp.StatusCode, ErrorClass: ErrorEndpointRedirect, SendEvidence: SendResponseReceived}
			}
			next, err := u.Parse(location)
			if err != nil {
				return oauthPolicyError()
			}
			endpoint = next.String()
			continue
		}
		limit := int64(oauthMaxBodyBytes)
		success := resp.StatusCode >= 200 && resp.StatusCode < 300
		if !success {
			limit = 4096
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		_ = resp.Body.Close()
		if readErr != nil || int64(len(data)) > limit {
			return &OAuthHTTPError{StatusCode: resp.StatusCode, ErrorClass: ErrorProtocol, SendEvidence: SendResponseReceived, cause: readErr}
		}
		if !success {
			e := &OAuthHTTPError{StatusCode: resp.StatusCode, ErrorClass: ErrorProtocol, SendEvidence: SendResponseReceived, RetryAfter: oauthRetryAfter(resp.Header.Get("Retry-After"))}
			var payload struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(data, &payload) == nil && knownOAuthError(payload.Error) {
				e.OAuthError = payload.Error
			}
			return e
		}
		if err := json.Unmarshal(data, out); err != nil {
			return &OAuthHTTPError{StatusCode: resp.StatusCode, ErrorClass: ErrorProtocol, SendEvidence: SendResponseReceived, cause: err}
		}
		return nil
	}
}

func (c *OAuthHTTPClient) roundTrip(ctx context.Context, method string, u *url.URL, ips []net.IPAddr, contentType string, body []byte, basic *oauthBasicCredentials) (*http.Response, error) {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: oauthMaxHeaderBytes,
		TLSHandshakeTimeout: oauthRequestTimeout,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.options.RootCAs, ServerName: u.Hostname()},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			var last error
			for _, ip := range ips {
				conn, err := c.options.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
	}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, &OAuthHTTPError{ErrorClass: ErrorProtocol, SendEvidence: SendNotSent, cause: err}
	}
	// Prevent library retry eligibility for credential-bearing POSTs.
	req.GetBody = nil
	req.Header.Set("Accept", "application/json")
	if basic != nil {
		req.SetBasicAuth(url.QueryEscape(basic.clientID), url.QueryEscape(basic.clientSecret))
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		class := ErrorNetwork
		if errors.Is(err, context.Canceled) {
			class = ErrorCancelled
		}
		if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
			class = ErrorTLS
		}
		if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
			class = ErrorTLS
		}
		return nil, &OAuthHTTPError{ErrorClass: class, SendEvidence: SendOutcomeUnknown, cause: err}
	}
	return resp, nil
}

func knownOAuthError(code string) bool {
	switch code {
	case "invalid_request", "invalid_client", "invalid_grant", "unauthorized_client", "unsupported_grant_type", "invalid_scope", "access_denied", "unsupported_response_type", "server_error", "temporarily_unavailable", "invalid_token", "insufficient_scope", "invalid_redirect_uri", "invalid_client_metadata", "invalid_software_statement", "unapproved_software_statement":
		return true
	}
	return false
}
func oauthRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(value)
	var delay time.Duration
	if err == nil && seconds > 0 {
		if seconds >= 2 {
			return 2 * time.Second
		}
		delay = time.Duration(seconds) * time.Second
	} else if date, err := http.ParseTime(value); err == nil {
		delay = time.Until(date)
	}
	if delay < 0 {
		return 0
	}
	if delay > 2*time.Second {
		return 2 * time.Second
	}
	return delay
}
