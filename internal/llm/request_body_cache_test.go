package llm

import (
	"bytes"
	gziplib "compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// The body cache is only safe because a request surface is rebuilt rather than
// mutated, so the identity must distinguish the two cases that would otherwise
// reuse a stale body: a slice resliced to a different length (same backing
// array, so the pointer alone matches) and a freshly built slice with equal
// contents (different backing array).
func TestRequestBodyIdentityDistinguishesResliceAndRebuild(t *testing.T) {
	msgs := []message.Message{{Role: message.RoleUser, Content: "a"}, {Role: message.RoleUser, Content: "b"}}
	tools := []message.ToolDefinition{{Name: "read"}}

	base := requestBodyIdentityFor("sys", msgs, tools, 1024, "sid")
	if base != requestBodyIdentityFor("sys", msgs, tools, 1024, "sid") {
		t.Fatal("identity differs for the same slices, want a hit")
	}

	if base == requestBodyIdentityFor("sys", msgs[:1], tools, 1024, "sid") {
		t.Fatal("a resliced prefix matched; the length must be compared alongside the pointer")
	}

	rebuilt := append([]message.Message(nil), msgs...)
	if base == requestBodyIdentityFor("sys", rebuilt, tools, 1024, "sid") {
		t.Fatal("a rebuilt slice with equal contents matched; identity must be the backing array, not the values")
	}

	if base == requestBodyIdentityFor("other", msgs, tools, 1024, "sid") {
		t.Fatal("a different system prompt matched")
	}
	if base == requestBodyIdentityFor("sys", msgs, tools, 2048, "sid") {
		t.Fatal("a different max_tokens matched")
	}
	if base == requestBodyIdentityFor("sys", msgs, nil, 1024, "sid") {
		t.Fatal("dropping the tool surface matched")
	}
	// The session key lands inside the body (Responses prompt_cache_key /
	// client_metadata), so a surface reused after the identity becomes known
	// must rebuild instead of serving the body built before it was known.
	if base == requestBodyIdentityFor("sys", msgs, tools, 1024, "") {
		t.Fatal("a missing session key matched a body built with one")
	}
}

// An empty slice has no first element to point at; two empty surfaces must
// still compare equal rather than depending on whether the caller passed nil
// or an allocated-but-empty slice.
func TestRequestBodyIdentityTreatsEmptySlicesAlike(t *testing.T) {
	if requestBodyIdentityFor("sys", nil, nil, 1024, "") != requestBodyIdentityFor("sys", []message.Message{}, []message.ToolDefinition{}, 1024, "") {
		t.Fatal("nil and empty surfaces produced different identities")
	}
}

func TestRequestBodyReuseCompressBodyMemoizesPerAttempt(t *testing.T) {
	var reuse requestBodyReuse
	// Seed the body cache the way a provider's CompleteStream does.
	identity := requestBodyIdentityFor("system", []message.Message{{Role: message.RoleUser, Content: "sample"}}, nil, 128, "")
	if _, err := reuse.body(identity, func() ([]byte, error) {
		return bytes.Repeat([]byte(`{"content":"sample payload"}`), 64), nil
	}); err != nil {
		t.Fatalf("seed body: %v", err)
	}

	build := func() (*http.Request, []byte) {
		body, err := reuse.body(identity, func() ([]byte, error) { return nil, errors.New("must reuse cached body") })
		if err != nil {
			t.Fatalf("cached body: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "https://example.invalid/v1", bytes.NewReader(body))
		outReq, outBody := reuse.compressBody(identity, req, body, config.RequestCompressionGzip)
		if outReq == req {
			t.Fatal("compression should replace the request")
		}
		if got := outReq.Header.Get("Content-Encoding"); got != config.RequestCompressionGzip {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		return outReq, outBody
	}

	_, firstBody := build()
	secondReq, secondBody := build()
	// A recompression allocates a fresh buffer, so identical backing arrays
	// prove the second attempt reused the memoized bytes.
	if &firstBody[0] != &secondBody[0] {
		t.Fatal("second attempt recompressed the body instead of reusing the memo")
	}
	if got := secondReq.Header.Get("Content-Encoding"); got != config.RequestCompressionGzip {
		t.Fatalf("second attempt Content-Encoding = %q, want gzip", got)
	}

	// A different body identity must not reuse the memo.
	other := requestBodyIdentityFor("system", []message.Message{{Role: message.RoleUser, Content: "other"}}, nil, 128, "")
	otherBody := bytes.Repeat([]byte(`{"content":"other payload"}`), 64)
	if _, err := reuse.body(other, func() ([]byte, error) { return otherBody, nil }); err != nil {
		t.Fatalf("rebuild body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://example.invalid/v1", bytes.NewReader(otherBody))
	outReq, outBody := reuse.compressBody(other, req, otherBody, config.RequestCompressionGzip)
	if outReq == req {
		t.Fatal("rebuilt body should be compressed")
	}
	reader, err := gziplib.NewReader(bytes.NewReader(outBody))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer reader.Close()
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	if !bytes.Equal(decompressed, otherBody) {
		t.Fatal("decompressed body does not match rebuilt body")
	}
}
