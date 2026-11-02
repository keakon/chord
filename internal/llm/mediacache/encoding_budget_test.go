package mediacache

import (
	"encoding/base64"
	"strings"
	"testing"
	"unsafe"
)

func TestOversizedEncodingIsReturnedWithoutCaching(t *testing.T) {
	previous := imageEncodingCache
	imageEncodingCache = newImageLRUCache(4, 128)
	t.Cleanup(func() { imageEncodingCache = previous })
	data := []byte(strings.Repeat("sample document ", 32))
	want := base64.StdEncoding.EncodeToString(data)
	for range 2 {
		if got := EncodeBase64(data); got != want {
			t.Fatal("oversized encoding changed")
		}
		if got := DataURL("application/pdf", data); got != "data:application/pdf;base64,"+want {
			t.Fatal("oversized data URL changed")
		}
	}
	encoded, url := ResidentBytes()
	if encoded != 0 || url != 0 {
		t.Fatalf("oversized encodings stayed resident: %d/%d", encoded, url)
	}
}

func TestMixedWireEncodingSharesBackingString(t *testing.T) {
	for _, urlFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "base64-first", true: "url-first"}[urlFirst], func(t *testing.T) {
			previous := imageEncodingCache
			c := newImageLRUCache(4, 1<<20)
			imageEncodingCache = c
			t.Cleanup(func() {
				c.mu.Lock()
				if c.idleTimer != nil {
					c.idleTimer.Stop()
				}
				c.mu.Unlock()
				imageEncodingCache = previous
			})
			data := []byte(strings.Repeat("sample document ", 32))
			want := base64.StdEncoding.EncodeToString(data)
			if !urlFirst && EncodeBase64(data) != want {
				t.Fatal("initial encoding changed")
			}
			url := DataURL("application/pdf", data)
			encoded := EncodeBase64(data)
			const prefix = "data:application/pdf;base64,"
			if encoded != want || url != prefix+want {
				t.Fatal("mixed wire changed the payload")
			}
			if unsafe.StringData(encoded) != unsafe.StringData(url[len(prefix):]) {
				t.Fatal("base64 result did not borrow the cached URL backing string")
			}
			base64Bytes, urlBytes := ResidentBytes()
			if base64Bytes != 0 || urlBytes != int64(len(Digest(data))+len(url)) || c.order.Len() != 1 {
				t.Fatalf("duplicate resident encoding: base64=%d url=%d entries=%d", base64Bytes, urlBytes, c.order.Len())
			}
			otherURL := DataURL("image/png", data)
			if otherURL != "data:image/png;base64,"+want || url != prefix+want {
				t.Fatal("changing MIME altered an already returned URL or its payload")
			}
			encoded = EncodeBase64(data)
			if unsafe.StringData(encoded) != unsafe.StringData(otherURL[len("data:image/png;base64,"):]) {
				t.Fatal("base64 view did not follow the replacement URL")
			}
			if c.order.Len() != 1 {
				t.Fatal("different MIME retained a second encoding")
			}
		})
	}
}
