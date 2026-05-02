package llm

import (
	"context"
	"fmt"
	"github.com/keakon/golog/log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// NewHTTPClientWithProxy creates an *http.Client with proxy support and granular timeouts.
// proxyURL can be: "http://...", "https://...", "socks5://...", "direct", or "" (default).
//
// Timeout strategy:
//   - totalTimeout: overall request deadline (5 min for non-streaming safety net)
//   - DialContext.Timeout: 60s connection establishment
//   - ResponseHeaderTimeout: 60s waiting for first response byte
//   - TLSHandshakeTimeout: 10s (classed as connection-establishment for key/model routing)
//   - For streaming, the per-chunk read timeout is handled at the SSE reader level,
//     not at the Transport level (Transport has no per-read timeout knob).
//
// When these fire, resulting errors are classified to decide whether to rotate
// keys or skip the provider in the model pool.
// NewHTTPClientWithProxyAndHeaderTimeout creates an *http.Client with proxy support,
// caller-controlled total timeout, and caller-controlled response header timeout.
// Connection-establishment timeout semantics remain unchanged.
func NewHTTPClientWithProxyAndHeaderTimeout(proxyURL string, totalTimeout, responseHeaderTimeout time.Duration) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   60 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
	}

	if proxyURL != "" {
		if proxyURL == "direct" {
			transport.Proxy = nil
		} else {
			parsed, err := url.Parse(proxyURL)
			if err != nil {
				return nil, fmt.Errorf("parse proxy URL %q: %w", proxyURL, err)
			}
			scheme := strings.ToLower(parsed.Scheme)
			switch scheme {
			case "http", "https":
				transport.Proxy = http.ProxyURL(parsed)
			case "socks5":
				dialer, err := proxy.FromURL(parsed, proxy.Direct)
				if err != nil {
					return nil, fmt.Errorf("create SOCKS5 dialer from %q: %w", proxyURL, err)
				}
				transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					if cd, ok := dialer.(proxy.ContextDialer); ok {
						return cd.DialContext(ctx, network, addr)
					}
					conn, err := dialer.Dial(network, addr)
					return conn, err
				}
			default:
				return nil, fmt.Errorf("unsupported proxy scheme %q", scheme)
			}
			log.Infof("LLM HTTP client using proxy scheme=%v", scheme)
		}
	}

	return &http.Client{Timeout: totalTimeout, Transport: transport}, nil
}

func NewHTTPClientWithProxy(proxyURL string, totalTimeout time.Duration) (*http.Client, error) {
	return NewHTTPClientWithProxyAndHeaderTimeout(proxyURL, totalTimeout, 60*time.Second)
}

// ProxyScheme returns the scheme of proxyURL ("http", "https", "socks5") or "" if none.
// Used for request-level logging to confirm the client is using a proxy.
func ProxyScheme(proxyURL string) string {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" || proxyURL == "direct" {
		return ""
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return ""
	}
	s := strings.ToLower(parsed.Scheme)
	if s == "http" || s == "https" || s == "socks5" {
		return s
	}
	return ""
}

// ResolveEffectiveProxy determines the effective proxy URL for a provider.
// Per-provider proxy (non-nil pointer) takes precedence over the global proxy.
//   - providerProxy == nil → use globalProxy (may be "")
//   - providerProxy != nil && *providerProxy == "" → "direct" (explicitly disabled, bypass env vars)
//   - providerProxy != nil && *providerProxy != "" → use *providerProxy
//
// An empty return value means "no proxy configured" (default transport, env vars
// still respected). The sentinel "direct" means "explicitly no proxy, override
// env vars".
func ResolveEffectiveProxy(providerProxy *string, globalProxy string) string {
	if providerProxy != nil {
		if *providerProxy == "" {
			return "direct" // sentinel: explicitly disabled by user
		}
		return *providerProxy
	}
	return globalProxy
}
