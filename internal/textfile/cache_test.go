package textfile

import (
	"path/filepath"
	"testing"
)

func TestInvalidateDropsPathMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	setPathCache(path, pathCacheEntry{Size: 6, Hash: cacheKeyForBytes([]byte("sample"))})
	if _, ok := getPathCache(path); !ok {
		t.Fatal("path mapping was not stored")
	}
	Invalidate(path)
	if _, ok := getPathCache(path); ok {
		t.Fatal("path mapping survived invalidation")
	}
}
