package tools

import "github.com/hashicorp/golang-lru/v2/expirable"

func clearEncodingCaches() {
	pathDetectionCacheMu.Lock()
	pathDetectionCache = expirable.NewLRU[string, pathCacheEntry](pathCacheEntries, nil, pathCacheTTL)
	pathDetectionCacheMu.Unlock()
	getDecodedCache().Clear()
}
