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
	high, low int64
}

// Frequent builds must not grow the shared cache without bound. Eviction is
// least-recently-modified first, so the newest entries survive; recent work
// only pays for rebuilding what fell outside the budget.
var defaultPolicy = policy{high: 8 << 30, low: 6 << 30}

// Result reports logical bytes of Go cache entries, including executable
// directories but excluding tools, fuzz corpora and metadata.
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
	dir  bool
}

func (c *Cache) trim(p policy) (Result, error) {
	result := Result{Scanned: true}
	var entries []entry
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
			path := filepath.Join(dir, file.Name())
			size, ok, err := cacheEntrySize(path, stat)
			if err != nil {
				return result, err
			}
			if !ok {
				continue
			}
			result.Before += size
			entries = append(entries, entry{path, size, stat.ModTime(), stat.IsDir()})
		}
	}
	result.After = result.Before
	if result.Before <= p.high {
		return result, nil
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if order := a.time.Compare(b.time); order != 0 {
			return order
		}
		return cmp.Compare(a.path, b.path)
	})
	for _, file := range entries {
		if result.After <= p.low {
			break
		}
		var err error
		if file.dir {
			err = os.RemoveAll(file.path)
		} else {
			err = os.Remove(file.path)
		}
		if err != nil {
			return result, fmt.Errorf("remove cache entry: %w", err)
		}
		result.After -= file.size
		result.Removed++
	}
	result.OverBudget = result.After > p.high
	return result, nil
}

func cacheEntrySize(path string, info os.FileInfo) (int64, bool, error) {
	if info.Mode().IsRegular() {
		return info.Size(), true, nil
	}
	if !info.IsDir() || !strings.HasSuffix(info.Name(), "-d") {
		return 0, false, nil
	}
	// Go stores an executable as a single regular file inside its output-ID
	// directory. Other shapes are not ours to remove.
	files, err := os.ReadDir(path)
	if err != nil {
		return 0, false, fmt.Errorf("read cached executable: %w", err)
	}
	if len(files) != 1 {
		return 0, false, nil
	}
	executable, err := files[0].Info()
	if err != nil {
		return 0, false, fmt.Errorf("inspect cached executable: %w", err)
	}
	return executable.Size(), executable.Mode().IsRegular(), nil
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
