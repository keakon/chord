package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestPKCE(t *testing.T) {
	state, verifier, challenge, err := newPKCE(strings.NewReader(strings.Repeat("a", 32) + strings.Repeat("b", 32)))
	if err != nil || len(state) != 43 || len(verifier) != 43 || state == verifier {
		t.Fatalf("PKCE generation: %v", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("incorrect S256 challenge")
	}
}

func TestCallbackValidationAndSingleConsumption(t *testing.T) {
	ctx := t.Context()
	c, err := listenCallback(ctx, 0, "expected", "https://auth.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	endpoint := "http://" + c.listener.Addr().String() + "/oauth/callback?"
	valid := url.Values{"state": {"expected"}, "code": {"sample-code"}, "iss": {"https://auth.example.com"}}
	send := func(q url.Values) int {
		t.Helper()
		resp, err := http.Get(endpoint + q.Encode())
		if err != nil {
			t.Error(err)
			return 0
		}
		defer resp.Body.Close()
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Error("callback response may be cached")
		}
		return resp.StatusCode
	}
	for _, mutate := range []func(url.Values){func(q url.Values) { q.Set("state", "wrong") }, func(q url.Values) { q.Del("iss") }, func(q url.Values) { q.Set("iss", "https://other.example.com") }, func(q url.Values) { q.Add("code", "duplicate") }, func(q url.Values) { q.Set("error", "access_denied") }} {
		q := url.Values{}
		for k, v := range valid {
			q[k] = append([]string(nil), v...)
		}
		mutate(q)
		if send(q) != http.StatusBadRequest {
			t.Fatal("invalid callback accepted")
		}
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Go(func() { statuses <- send(valid) })
	}
	wg.Wait()
	close(statuses)
	successful := 0
	for status := range statuses {
		if status == http.StatusOK {
			successful++
		}
	}
	if successful != 1 {
		t.Fatalf("consumed %d callbacks", successful)
	}
	result, err := c.Wait(ctx)
	if err != nil || result.code != "sample-code" || result.denied {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestCancelledCallbackDoesNotConsume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, err := listenCallback(ctx, 0, "expected", "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cancel()
	resp, err := http.Get("http://" + c.listener.Addr().String() + "/oauth/callback?state=expected&code=sample")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || len(c.result) != 0 {
		t.Fatal("cancelled callback consumed authorization")
	}
}
