//go:build unix

// Package buildcache manages the shared cache of supervised project builds.
package buildcache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
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
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, fmt.Errorf("inspect build cache: %w", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
			return nil, fmt.Errorf("build cache directory is not private: %s", dir)
		}
	}
	return &Cache{root: root}, nil
}

func (c *Cache) Dir() string { return filepath.Join(c.root, "go-build") }

// Begin records use before starting any process that can access Dir.
// A crashed owner leaves its record until its supervisor proves completion.
func (c *Cache) Begin(id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid build cache user: %q", id)
	}
	lock, err := c.lock(false)
	if err != nil {
		return err
	}
	defer lock.Close()
	file, err := os.OpenFile(filepath.Join(c.root, "users", id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("register build cache user: %w", err)
	}
	if err := file.Close(); err != nil {
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

// Trim tries an idle-only scan, at most once an hour. Usage records survive
// process death: acquiring the lock alone is never proof that the cache is idle.
func (c *Cache) Trim(now time.Time) (Result, error) {
	lock, err := c.lock(true)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return Result{}, nil
	}
	if err != nil {
		return Result{}, err
	}
	defer lock.Close()
	users, err := os.ReadDir(filepath.Join(c.root, "users"))
	if err != nil {
		return Result{}, fmt.Errorf("read build cache users: %w", err)
	}
	if len(users) > 0 {
		return Result{Users: len(users)}, nil
	}
	stamp := filepath.Join(c.root, "last-trim")
	if info, err := os.Stat(stamp); err == nil {
		if age := now.Sub(info.ModTime()); age >= 0 && age < time.Hour {
			return Result{}, nil
		}
	}
	result, err := c.trim(now, defaultPolicy)
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(stamp, nil, 0o600); err != nil {
		return result, fmt.Errorf("record build cache trim: %w", err)
	}
	if err := os.Chtimes(stamp, now, now); err != nil {
		return result, fmt.Errorf("date build cache trim: %w", err)
	}
	return result, nil
}
