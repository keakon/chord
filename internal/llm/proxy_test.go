package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewHTTPClientWithProxyUsesDocumentedTimeouts(t *testing.T) {
	client, err := NewHTTPClientWithProxy("", 0)
	if err != nil {
		t.Fatalf("NewHTTPClientWithProxy returned error: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != 25*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 25s", transport.ResponseHeaderTimeout)
	}
	if transport.TLSHandshakeTimeout != 10*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %v, want 10s", transport.TLSHandshakeTimeout)
	}
	if transport.ExpectContinueTimeout != time.Second {
		t.Fatalf("ExpectContinueTimeout = %v, want 1s", transport.ExpectContinueTimeout)
	}
	if transport.DialContext == nil {
		t.Fatal("transport.DialContext = nil, want configured dialer")
	}
}

func TestNewStreamingHTTPClientWithProxyDisablesTotalTimeout(t *testing.T) {
	client, err := NewStreamingHTTPClientWithProxy("", 45*time.Second)
	if err != nil {
		t.Fatalf("NewStreamingHTTPClientWithProxy returned error: %v", err)
	}
	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != 45*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 45s", transport.ResponseHeaderTimeout)
	}
}

func TestDoRequestUntilHeadersTimesOutWhileWritingRequestBody(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		buf := make([]byte, 1)
		_, _ = req.Body.Read(buf)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test", strings.NewReader("request body"))
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	started := time.Now()
	_, err = doRequestUntilHeaders(client, req, 20*time.Millisecond)
	if err == nil {
		t.Fatal("doRequestUntilHeaders error = nil, want timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("doRequestUntilHeaders error = %v, want context deadline exceeded", err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("doRequestUntilHeaders error = %v, want net.Error timeout", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("doRequestUntilHeaders took %v, want prompt timeout", time.Since(started))
	}
}

func TestDoRequestUntilHeadersDoesNotTimeoutStreamingBody(t *testing.T) {
	responseBody := newBlockingResponseBody()
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: responseBody, Header: make(http.Header)}, nil
	})}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	resp, err := doRequestUntilHeaders(client, req, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("doRequestUntilHeaders: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	select {
	case <-responseBody.closed:
		t.Fatal("response body closed after pre-response timeout elapsed")
	default:
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("response body Close: %v", err)
	}
	select {
	case <-responseBody.closed:
	default:
		t.Fatal("response body was not closed")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type blockingResponseBody struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingResponseBody() *blockingResponseBody {
	return &blockingResponseBody{closed: make(chan struct{})}
}

func (b *blockingResponseBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *blockingResponseBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}
