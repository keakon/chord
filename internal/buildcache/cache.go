//go:build unix

// Package buildcache manages the shared cache of supervised project builds.
package buildcache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/keakon/chord/internal/jsonx"
)

// Cache keeps build data separate from persistent usage records.
type Cache struct {
	root string
}

// Open creates a private cache at root. All worktrees must use the same root.
func Open(root string) (*Cache, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("build cache path must be absolute: %s", root)
	}
	for _, dir := range []string{root, filepath.Join(root, "go-build"), filepath.Join(root, "users")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create build cache: %w", err)
		}
	}
	if err := validateDirectories(root); err != nil {
		return nil, err
	}
	return &Cache{root: root}, nil
}

// OpenExisting opens a cache without creating or changing any files.
func OpenExisting(root string) (*Cache, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("build cache path must be absolute: %s", root)
	}
	if err := validateDirectories(root); err != nil {
		return nil, err
	}
	return &Cache{root: root}, nil
}

func validateDirectories(root string) error {
	for _, dir := range []string{root, filepath.Join(root, "go-build"), filepath.Join(root, "users")} {
		info, err := os.Lstat(dir)
		if err != nil {
			return fmt.Errorf("inspect build cache: %w", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
			return fmt.Errorf("build cache directory is not private: %s", dir)
		}
	}
	return nil
}

func (c *Cache) Dir() string { return filepath.Join(c.root, "go-build") }

// Begin records use before starting any process that can access Dir. The
// record carries the boot identity so Trim can reap it after a restart even
// if the supervisor died before End proved command completion.
func (c *Cache) Begin(id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid build cache user: %q", id)
	}
	lock, err := c.lock(false)
	if err != nil {
		return err
	}
	defer lock.Close()
	data, err := jsonx.Marshal(userRecord{BootID: systemBootID()})
	if err != nil {
		return fmt.Errorf("encode build cache user: %w", err)
	}
	path := filepath.Join(c.root, "users", id)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("register build cache user: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		// No command can own this record before Begin succeeds.
		_ = os.Remove(path)
		return fmt.Errorf("register build cache user: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close build cache user: %w", err)
	}
	return nil
}

// End may only be called after the entire command's cleanup is proven, or
// after a launch failure that could not have started a command.
func (c *Cache) End(id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid build cache user: %q", id)
	}
	lock, err := c.lock(false)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := os.Remove(filepath.Join(c.root, "users", id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("release build cache user: %w", err)
	}
	return nil
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, ch := range id {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func (c *Cache) lock(nonblocking bool) (*os.File, error) {
	fd, err := syscall.Open(filepath.Join(c.root, ".lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open build cache lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), "build-cache-lock")
	flags := syscall.LOCK_EX
	if nonblocking {
		flags |= syscall.LOCK_NB
	}
	if err := syscall.Flock(fd, flags); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock build cache: %w", err)
	}
	return file, nil
}

// Trim enforces the idle cache budget. Records from a different boot can be
// reaped, but current or unknown owners prevent eviction: Go may reopen a
// cached path any time before the command exits.
func (c *Cache) Trim() (Result, error) {
	lock, err := c.lock(true)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return Result{RegistryBusy: true}, nil
	}
	if err != nil {
		return Result{}, err
	}
	defer lock.Close()
	live, reaped, err := c.reapUsers(systemBootID())
	if err != nil {
		return Result{}, err
	}
	if live > 0 {
		return Result{Users: live, Reaped: reaped}, nil
	}
	result, err := c.trim(defaultPolicy)
	result.Reaped = reaped
	return result, err
}

// TrimPreviousBoot reaps provably stale usage records and scans the cache
// only when at least one such record was found.
func (c *Cache) TrimPreviousBoot() (Result, error) {
	return c.trimPreviousBoot(systemBootID())
}

func (c *Cache) trimPreviousBoot(bootID string) (Result, error) {
	lock, err := c.lock(true)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return Result{RegistryBusy: true}, nil
	}
	if err != nil {
		return Result{}, err
	}
	defer lock.Close()
	live, reaped, err := c.reapUsers(bootID)
	result := Result{Users: live, Reaped: reaped}
	if err != nil || reaped == 0 || live > 0 {
		return result, err
	}
	result, err = c.trim(defaultPolicy)
	result.Reaped = reaped
	return result, err
}
