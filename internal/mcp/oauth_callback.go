package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type authorizationCallback struct {
	code   string
	denied bool
}

type callbackListener struct {
	server        *http.Server
	listener      net.Listener
	state         string
	issuer        string
	requireIssuer bool
	result        chan authorizationCallback
	once          sync.Once
	ctx           context.Context
}

func newPKCE(random io.Reader) (state, verifier, challenge string, err error) {
	if random == nil {
		random = rand.Reader
	}
	b := make([]byte, 32)
	if _, err = io.ReadFull(random, b); err != nil {
		return "", "", "", fmt.Errorf("generate authorization state: %w", err)
	}
	state = base64.RawURLEncoding.EncodeToString(b)
	if _, err = io.ReadFull(random, b); err != nil {
		return "", "", "", fmt.Errorf("generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	digest := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(digest[:])
	return
}

func listenCallback(ctx context.Context, port int, state, issuer string, requireIssuer bool) (*callbackListener, error) {
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("OAuth callback unavailable")
	}
	c := &callbackListener{listener: listener, state: state, issuer: issuer, requireIssuer: requireIssuer, result: make(chan authorizationCallback, 1), ctx: ctx}
	c.server = &http.Server{Handler: http.HandlerFunc(c.handle), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = c.server.Serve(listener) }()
	return c, nil
}

func (c *callbackListener) Close() { _ = c.server.Close() }
func (c *callbackListener) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	reject := func() { http.Error(w, "Invalid authorization response.", http.StatusBadRequest) }
	if r.Method != http.MethodGet || r.URL.Path != "/oauth/callback" || r.Host != c.listener.Addr().String() || len(r.URL.RawQuery) > 8<<10 {
		reject()
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		reject()
		return
	}
	for _, key := range []string{"state", "code", "iss", "error"} {
		if len(q[key]) > 1 {
			reject()
			return
		}
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(c.state)) != 1 {
		reject()
		return
	}
	issuer, hasIssuer := q["iss"]
	if (hasIssuer && (len(issuer) != 1 || issuer[0] != c.issuer)) || (!hasIssuer && c.requireIssuer) {
		reject()
		return
	}
	_, hasCode := q["code"]
	_, hasError := q["error"]
	if hasCode == hasError || (hasCode && q.Get("code") == "") || (hasError && q.Get("error") == "") {
		reject()
		return
	}
	if c.ctx.Err() != nil {
		reject()
		return
	}
	consumed := false
	c.once.Do(func() { consumed = true; c.result <- authorizationCallback{code: q.Get("code"), denied: hasError} })
	if !consumed {
		reject()
		return
	}
	_, _ = io.WriteString(w, "Authorization response received. You can return to Chord.")
}

func (c *callbackListener) Wait(ctx context.Context) (authorizationCallback, error) {
	select {
	case <-ctx.Done():
		return authorizationCallback{}, ctx.Err()
	case result := <-c.result:
		if err := ctx.Err(); err != nil {
			return authorizationCallback{}, err
		}
		return result, nil
	}
}
