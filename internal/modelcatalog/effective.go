package modelcatalog

import (
	"errors"
	"os"
	"sync/atomic"
)

// effectiveState is the immutable snapshot of everything the process resolves
// against: the catalog, the candidate entries that shipped with it, and how a
// refresh cache participated in the selection. It is replaced only by
// InstallCachedCatalog, which runs once at command startup before config
// resolution, so readers never observe a torn state.
type effectiveState struct {
	catalog    *Catalog
	candidates []Candidate
	cache      *CacheFile // non-nil when the effective catalog came from the refresh cache
	rejection  string     // why a present cache file is not in effect, for diagnostics
}

var effectiveStatePtr = func() *atomic.Pointer[effectiveState] {
	p := new(atomic.Pointer[effectiveState])
	p.Store(&effectiveState{catalog: mustLoadCatalog()})
	return p
}()

func current() *effectiveState { return effectiveStatePtr.Load() }

func effective() *Catalog { return current().catalog }

// Origin describes the catalog the process currently resolves against.
type Origin struct {
	Version string
	// Source records the upstream revision when the catalog knows one: the
	// embedded snapshot records the chord-models tag it was synced from, the
	// refresh cache the tag it was fetched from.
	Source *CatalogSource
	// Cached reports whether the effective catalog came from the refresh
	// cache rather than the embedded snapshot.
	Cached bool
}

// OriginInfo reports the effective catalog's identity for diagnostics
// surfaces such as `chord config show --catalog`.
func OriginInfo() Origin {
	s := current()
	return Origin{Version: s.catalog.Version, Source: s.catalog.Source, Cached: s.cache != nil}
}

// EffectiveCandidates lists the candidate entries currently in effect. They
// arrive only through the refresh cache; without one the list is empty and
// the embedded snapshot never carries candidates.
func EffectiveCandidates() []Candidate {
	return current().candidates
}

// CacheStatus describes how the refresh cache participated in the effective
// catalog selection, for diagnostics surfaces.
type CacheStatus struct {
	Present   bool   // a cache file was found
	Installed bool   // its catalog is the effective one
	Detail    string // why the cache is not in effect, when applicable
}

// CurrentCacheStatus reports the last refresh-cache selection outcome.
func CurrentCacheStatus() CacheStatus {
	s := current()
	if s.cache != nil {
		return CacheStatus{Present: true, Installed: true}
	}
	if s.rejection != "" {
		return CacheStatus{Present: true, Detail: s.rejection}
	}
	return CacheStatus{}
}

// InstallCachedCatalog reads the refresh cache at path and installs it as the
// effective catalog when it parses cleanly and carries a catalog version
// newer than the one in effect. A missing file is the normal no-cache state.
// Anything else that keeps the cache out of effect — stale, corrupt, written
// by an incompatible schema — is recorded for CacheStatus and returned as an
// error; the effective catalog never regresses and the embedded snapshot
// always stays usable.
func InstallCachedCatalog(path string) error {
	cache, err := ReadCacheFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		reject(err.Error())
		return err
	}
	cur := current()
	if CompareVersions(cache.Catalog.Version, cur.catalog.Version) <= 0 {
		reject("cache version " + cache.Catalog.Version + " is not newer than the catalog in effect (" + cur.catalog.Version + ")")
		return nil
	}
	effectiveStatePtr.Store(&effectiveState{catalog: cache.Catalog, candidates: cache.Candidates, cache: cache})
	return nil
}

// reject records why a present cache file is not in effect while keeping the
// currently effective catalog and candidates.
func reject(reason string) {
	cur := current()
	effectiveStatePtr.Store(&effectiveState{catalog: cur.catalog, candidates: cur.candidates, cache: cur.cache, rejection: reason})
}

// CompareVersions orders catalog versions such as "2026-10-01.1". The
// comparison splits each version into digit and non-digit chunks, compares
// digit chunks numerically and the rest case-insensitively, so a later date
// or a higher revision sorts after an earlier one. It is the whole-catalog
// tie-breaker for the refresh cache: versions compare, contents never merge.
func CompareVersions(a, b string) int {
	achunks, bchunks := versionChunks(a), versionChunks(b)
	for i := 0; i < len(achunks) && i < len(bchunks); i++ {
		if n := compareVersionChunks(achunks[i], bchunks[i]); n != 0 {
			return n
		}
	}
	return len(achunks) - len(bchunks)
}

func versionChunks(v string) []string {
	var chunks []string
	start := 0
	digit := func(r byte) bool { return r >= '0' && r <= '9' }
	for i := 0; i < len(v); i++ {
		if i > 0 && digit(v[i]) != digit(v[i-1]) {
			chunks = append(chunks, v[start:i])
			start = i
		}
	}
	if start < len(v) {
		chunks = append(chunks, v[start:])
	}
	return chunks
}

func compareVersionChunks(a, b string) int {
	da, db := isDigitChunk(a), isDigitChunk(b)
	if da && db {
		a, b = trimLeadingZeros(a), trimLeadingZeros(b)
		if len(a) != len(b) {
			return len(a) - len(b)
		}
		return compareLower(a, b)
	}
	return compareLower(a, b)
}

func isDigitChunk(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func trimLeadingZeros(s string) string {
	for len(s) > 1 && s[0] == '0' {
		s = s[1:]
	}
	return s
}

func compareLower(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		ca, cb := lowerByte(a[i]), lowerByte(b[i])
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}

func lowerByte(r byte) byte {
	if r >= 'A' && r <= 'Z' {
		return r + 'a' - 'A'
	}
	return r
}
