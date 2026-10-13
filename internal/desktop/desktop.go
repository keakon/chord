// Package desktop opens local documents using operating-system associations.
package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Capabilities struct {
	DefaultOpen, Enumerate, NativeChooser bool
	Reason                                string
}
type Application struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}
type Associations struct {
	Apps      []Application
	Truncated bool
}
type Service interface {
	Capabilities(context.Context) (Capabilities, error)
	Associations(context.Context, string) (Associations, error)
	OpenDefault(context.Context, string) error
	OpenWith(context.Context, string, string) error
	ChooseApplication(context.Context, string) error
}

var ErrUnsupported = errors.New("system application selection is unavailable")
var ErrNoDesktop = errors.New("desktop opening is unavailable in SSH or a headless session")
var ErrBusy = errors.New("an open request is already running, or all four launch slots are occupied")

const maxOutput = 64 << 10

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remain := maxOutput - b.Len()
	if len(p) > remain {
		b.exceeded = true
		p = p[:max(0, remain)]
	}
	b.Buffer.Write(p)
	return n, nil
}

type Runner func(context.Context, bool, string, ...string) ([]byte, error)
type System struct {
	run     Runner
	mu      sync.Mutex
	running map[string]bool
	cache   map[string]associationCacheEntry
}

func New() *System { return &System{run: runCommand, running: make(map[string]bool)} }
func runCommand(ctx context.Context, query bool, name string, args ...string) ([]byte, error) {
	var cmd *exec.Cmd
	if query {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cmd = exec.Command(name, args...)
	}
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, errors.New("desktop helper output exceeded 64 KiB")
	}
	if err != nil {
		return nil, fmt.Errorf("desktop helper: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
func remote() bool {
	return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_CLIENT") != "" || os.Getenv("SSH_TTY") != ""
}
func validateDocument(path string) error {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return errors.New("an absolute local file path is required")
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("only regular documents can be opened")
	}
	if st.Mode().Perm()&0111 != 0 {
		return errors.New("external opening of executable files is disabled")
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".desktop", ".exe", ".com", ".bat", ".cmd", ".ps1", ".lnk", ".sh", ".bash", ".zsh", ".fish", ".py", ".pl", ".rb", ".vbs", ".js", ".jse", ".wsf", ".msi", ".app":
		return errors.New("external opening of scripts and executable files is disabled")
	}
	return nil
}
func (s *System) launch(ctx context.Context, path string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateDocument(path); err != nil {
		return err
	}
	caps, err := s.Capabilities(ctx)
	if err != nil {
		return err
	}
	if !caps.DefaultOpen {
		return ErrNoDesktop
	}
	s.mu.Lock()
	if s.running == nil {
		s.running = make(map[string]bool)
	}
	if s.running[path] || len(s.running) >= 4 {
		s.mu.Unlock()
		return ErrBusy
	}
	s.running[path] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.running, path); s.mu.Unlock() }()
	return fn()
}

type associationCacheEntry struct {
	at    time.Time
	value Associations
}

func (s *System) Associations(ctx context.Context, path string) (Associations, error) {
	caps, err := s.Capabilities(ctx)
	if err != nil {
		return Associations{}, err
	}
	if !caps.DefaultOpen {
		return Associations{}, ErrNoDesktop
	}
	if !caps.Enumerate {
		return Associations{}, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := validateDocument(path); err != nil {
		return Associations{}, err
	}
	key := documentMIME(path) + "\x00" + filepath.Ext(path) + "\x00" + os.Getenv("XDG_CURRENT_DESKTOP") + "\x00" + os.Getenv("SSH_CONNECTION")
	s.mu.Lock()
	entry, ok := s.cache[key]
	s.mu.Unlock()
	if ok && time.Since(entry.at) < 30*time.Second {
		entry.value.Apps = append([]Application(nil), entry.value.Apps...)
		return entry.value, nil
	}
	result, err := s.queryAssociations(ctx, path)
	if err != nil {
		return result, err
	}
	s.mu.Lock()
	if len(s.cache) >= 128 {
		s.cache = nil
	}
	if s.cache == nil {
		s.cache = make(map[string]associationCacheEntry)
	}
	cached := result
	cached.Apps = append([]Application(nil), result.Apps...)
	s.cache[key] = associationCacheEntry{at: time.Now(), value: cached}
	s.mu.Unlock()
	return result, nil
}
func (s *System) RefreshAssociations() { s.mu.Lock(); s.cache = nil; s.mu.Unlock() }

func documentMIME(path string) string {
	typ := mime.TypeByExtension(filepath.Ext(path))
	if typ == "" {
		if file, err := os.Open(path); err == nil {
			data, _ := io.ReadAll(io.LimitReader(file, 512))
			file.Close()
			typ = http.DetectContentType(data)
		}
	}
	if i := strings.IndexByte(typ, ';'); i >= 0 {
		typ = typ[:i]
	}
	if typ == "" {
		typ = "application/octet-stream"
	}
	return typ
}
