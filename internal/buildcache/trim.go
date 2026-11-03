//go:build unix

package buildcache

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type policy struct {
	large, high, low int64
	age              time.Duration
}

var defaultPolicy = policy{large: 10 << 20, high: 30 << 30, low: 20 << 30, age: 7 * time.Hour}

// Result reports logical bytes of ordinary Go cache files, excluding tools,
// fuzz corpora and metadata. Targets are soft: recent files are always retained.
type Result struct {
	Scanned    bool
	Before     int64
	After      int64
	Removed    int
	Users      int
	OverBudget bool
}

type entry struct {
	path string
	size int64
	time time.Time
}

func (c *Cache) trim(now time.Time, p policy) (Result, error) {
	result := Result{Scanned: true}
	var candidates []entry
	for i := range 256 {
		dir := filepath.Join(c.Dir(), fmt.Sprintf("%02x", i))
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return result, fmt.Errorf("inspect cache bucket: %w", err)
		}
		if !info.IsDir() {
			continue
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			return result, fmt.Errorf("read cache bucket: %w", err)
		}
		for _, file := range files {
			if !cacheName(file.Name()) || !strings.HasPrefix(file.Name(), info.Name()) {
				continue
			}
			stat, err := file.Info()
			if err != nil {
				return result, fmt.Errorf("inspect cache entry: %w", err)
			}
			if !stat.Mode().IsRegular() {
				continue
			}
			result.Before += stat.Size()
			if now.Sub(stat.ModTime()) > p.age {
				candidates = append(candidates, entry{filepath.Join(dir, file.Name()), stat.Size(), stat.ModTime()})
			}
		}
	}
	result.After = result.Before
	slices.SortFunc(candidates, func(a, b entry) int {
		if (a.size > p.large) != (b.size > p.large) {
			if a.size > p.large {
				return -1
			}
			return 1
		}
		if order := a.time.Compare(b.time); order != 0 {
			return order
		}
		return cmp.Compare(a.path, b.path)
	})
	pressure := result.Before > p.high
	for _, file := range candidates {
		// Old large archives expire even below the budget. Under pressure,
		// smaller old entries can also be evicted, but never recent ones.
		if file.size <= p.large && (!pressure || result.After <= p.low) {
			continue
		}
		if err := os.Remove(file.path); err != nil {
			return result, fmt.Errorf("remove cache entry: %w", err)
		}
		result.After -= file.size
		result.Removed++
	}
	result.OverBudget = result.After > p.high
	return result, nil
}

func cacheName(name string) bool {
	if len(name) != 66 || (name[64:] != "-a" && name[64:] != "-d") {
		return false
	}
	for _, ch := range name[:64] {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}
