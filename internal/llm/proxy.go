package llm

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"golang.org/x/net/proxy"
)

const (
	defaultHTTPDialTimeout           = 15 * time.Second
	defaultHTTPTLSHandshakeTimeout   = 10 * time.Second
	defaultHTTPResponseHeaderTimeout = 25 * time.Second
)

// NewHTTPClientWithProxyAndHeaderTimeout creates an *http.Client with proxy support,
// caller-controlled total timeout, and caller-controlled response header timeout.
// Connection-establishment timeout semantics remain unchanged.
func NewHTTPClientWithProxyAndHeaderTimeout(proxyURL string, totalTimeout, responseHeaderTimeout time.Duration) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   defaultHTTPDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   defaultHTTPTLSHandshakeTimeout,
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
	return NewHTTPClientWithProxyAndHeaderTimeout(proxyURL, totalTimeout, defaultHTTPResponseHeaderTimeout)
}

func NewStreamingHTTPClientWithProxy(proxyURL string, responseHeaderTimeout time.Duration) (*http.Client, error) {
	if responseHeaderTimeout <= 0 {
		responseHeaderTimeout = defaultHTTPResponseHeaderTimeout
	}
	return NewHTTPClientWithProxyAndHeaderTimeout(proxyURL, 0, responseHeaderTimeout)
}

func providerResponseHeaderTimeout(provider *ProviderConfig) time.Duration {
	if provider == nil {
		return 0
	}
	return provider.ResponseHeaderTimeout()
}

// doRequestUntilHeaders bounds all work before response headers arrive,
// including connection setup and writing the request body. The watchdog is
// stopped once Do returns so long-lived streaming response bodies remain valid.
func doRequestUntilHeaders(client *http.Client, req *http.Request, timeout time.Duration) (*http.Response, error) {
	if timeout <= 0 {
		timeout = defaultHTTPResponseHeaderTimeout
	}

	requestCtx, cancel := context.WithCancel(req.Context())
	request := req.Clone(requestCtx)

	var mu sync.Mutex
	done := false
	timedOut := false
	timer := time.AfterFunc(timeout, func() {
		mu.Lock()
		if !done {
			timedOut = true
			cancel()
		}
		mu.Unlock()
	})

	resp, err := client.Do(request)
	mu.Lock()
	done = true
	timer.Stop()
	requestTimedOut := timedOut
	mu.Unlock()

	if requestTimedOut {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		cancel()
		return nil, &preResponseTimeoutError{timeout: timeout}
	}
	if err != nil {
		cancel()
		return nil, err
	}

	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type preResponseTimeoutError struct {
	timeout time.Duration
}

func (e *preResponseTimeoutError) Error() string {
	return fmt.Sprintf("request timed out before response headers after %s", e.timeout)
}

func (e *preResponseTimeoutError) Unwrap() error { return context.DeadlineExceeded }
func (e *preResponseTimeoutError) Timeout() bool { return true }

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
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
