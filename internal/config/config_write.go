package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/keakon/chord/internal/atomicfile"
)

type configMutationLock struct {
	file *os.File
}

func (l *configMutationLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	// Keep the lock inode: waiters may already have opened it. Unlinking
	// would let another process acquire a different inode concurrently.
	_ = unlockConfigMutationFile(l.file)
	err := l.file.Close()
	l.file = nil
	return err
}

func LockConfigMutation(targetPath string) (*configMutationLock, error) {
	return LockConfigMutationContext(context.Background(), targetPath)
}

func LockConfigMutationContext(ctx context.Context, targetPath string) (*configMutationLock, error) {
	if targetPath == "" {
		return nil, fmt.Errorf("config path is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lockPath := targetPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create config lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open config lock file: %w", err)
	}
	if err := lockConfigMutationFileContext(ctx, f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock config file: %w", err)
	}
	return &configMutationLock{file: f}, nil
}

// CreateOrUpdateConfigFileLocked supports command-driven first setup. Missing
// files are presented as empty YAML under the same lock as existing edits.
func CreateOrUpdateConfigFileLocked(path string, produce func(current []byte) ([]byte, error)) error {
	lock, err := LockConfigMutation(path)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	current, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	out, err := produce(current)
	if err != nil {
		return err
	}
	return writeConfigFileAtomicallyReplace(path, out, 0o600)
}

func WriteConfigFileAtomically(path string, data []byte, mode os.FileMode) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}
	path, err := configWriteTarget(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create config temp file: %w", err)
	}
	tmpPath := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("set config temp permissions: %w", err)
	}
	if n, err := f.Write(data); err != nil {
		return fmt.Errorf("write config temp file: %w", err)
	} else if n != len(data) {
		return fmt.Errorf("write config temp file: %w", io.ErrShortWrite)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync config temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close config temp file: %w", err)
	}
	// A hard link installs the complete file without replacing a destination
	// another writer created after this write began.
	if err := os.Link(tmpPath, path); err != nil {
		if os.IsExist(err) {
			return os.ErrExist
		}
		return fmt.Errorf("install config file: %w", err)
	}
	return nil
}

func writeConfigFileAtomicallyReplace(path string, data []byte, mode os.FileMode) error {
	path, err := configWriteTarget(path)
	if err != nil {
		return err
	}
	return atomicfile.Replace(path, data, mode)
}

// configWriteTarget resolves a config file the user deliberately installed as a
// symlink — a dotfiles repository or a shared checkout beside the project. That
// link is a deliberate choice made at the location Chord reads the file from, so
// the rewrite goes to the file it names; renaming a temp file over the link would
// replace it with a regular file and leave the real source stale. A dangling
// link still names the file the user meant to keep, even when the last target
// of a link chain does not exist yet.
func configWriteTarget(path string) (string, error) {
	volume := filepath.VolumeName(path)
	resolved := volume + string(os.PathSeparator)
	if !filepath.IsAbs(path) {
		var err error
		resolved, err = filepath.Abs(volume + ".")
		if err != nil {
			return "", fmt.Errorf("resolve config directory: %w", err)
		}
		if strings.HasPrefix(filepath.ToSlash(path[len(volume):]), "/") {
			resolved = filepath.VolumeName(resolved) + string(os.PathSeparator)
		}
	}
	// Walk components before cleaning "..": the preceding component may be a
	// directory symlink. EvalSymlinks alone cannot resolve a missing target.
	pending := strings.Split(filepath.ToSlash(path[len(volume):]), "/")
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, part)
		info, err := os.Lstat(next)
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect config target %s: %w", next, err)
		}
		if os.IsNotExist(err) || info.Mode()&os.ModeSymlink == 0 {
			if err == nil && !info.IsDir() && len(pending) > 0 {
				return "", fmt.Errorf("resolve config target: %w", &os.PathError{Op: "lstat", Path: next, Err: syscall.ENOTDIR})
			}
			resolved = next
			continue
		}
		links++
		if links > 255 {
			return "", fmt.Errorf("resolve config target %s: too many symbolic links", path)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", fmt.Errorf("read config link %s: %w", next, err)
		}
		volume = filepath.VolumeName(target)
		if filepath.IsAbs(target) {
			resolved = volume + string(os.PathSeparator)
		} else if volume != "" {
			resolved, err = filepath.Abs(volume + ".")
			if err != nil {
				return "", fmt.Errorf("resolve config directory: %w", err)
			}
		} else if strings.HasPrefix(filepath.ToSlash(target), "/") {
			resolved = filepath.VolumeName(resolved) + string(os.PathSeparator)
		}
		pending = append(strings.Split(filepath.ToSlash(target[len(volume):]), "/"), pending...)
	}
	return resolved, nil
}
