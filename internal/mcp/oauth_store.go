package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/keakon/chord/internal/atomicfile"
	"github.com/keakon/chord/internal/config"
)

const credentialFileLimit = 4 << 20

type CredentialStore struct {
	home      string
	mu        sync.Mutex
	confirmed map[string][sha256.Size]byte
	replace   func(string, []byte, os.FileMode) (atomicfile.ReplaceOutcome, error)
}

func NewCredentialStore(configHome string) *CredentialStore {
	return &CredentialStore{home: configHome, confirmed: make(map[string][sha256.Size]byte), replace: atomicfile.ReplaceDurable}
}

func storeFailure(class ErrorClass, outcome atomicfile.ReplaceOutcome) error {
	return &CredentialStoreError{Class: class, Outcome: outcome}
}

func (s *CredentialStore) forget(id string) { s.mu.Lock(); delete(s.confirmed, id); s.mu.Unlock() }

func (s *CredentialStore) remember(id string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.confirmed) >= 256 {
		for key := range s.confirmed {
			delete(s.confirmed, key)
			break
		}
	}
	s.confirmed[id] = sha256.Sum256(data)
}

func (s *CredentialStore) known(id string, data []byte) bool {
	sum := sha256.Sum256(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.confirmed[id]
	return ok && old == sum
}

func (s *CredentialStore) path(t Target) (string, error) {
	b, err := hex.DecodeString(t.TargetID)
	if err != nil || len(b) != sha256.Size || t.TargetID != hex.EncodeToString(b) {
		return "", storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	return filepath.Join(s.home, "mcp", "oauth", t.TargetID+".json"), nil
}

// prepare validates all components beneath ConfigHome. Read-only callers never
// create directories, lock files, or confirmation writes.
func (s *CredentialStore) prepare(write bool) error {
	if s.home == "" {
		return storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	for i, path := range []string{s.home, filepath.Join(s.home, "mcp"), filepath.Join(s.home, "mcp", "oauth")} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) && write {
			if err = atomicfile.EnsurePrivateDirectory(path); err == nil {
				info, err = os.Lstat(path)
			}
		}
		if os.IsNotExist(err) {
			return os.ErrNotExist
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
		}
		if i == 2 {
			if err := atomicfile.ValidatePrivatePath(path, true); err != nil {
				return storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
			}
		} else if err := atomicfile.ValidatePrivateContainer(path); err != nil {
			return storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
		}
	}
	return nil
}

func (s *CredentialStore) read(t Target) (*CredentialFile, []byte, error) {
	path, err := s.path(t)
	if err != nil {
		return nil, nil, err
	}
	if err := s.prepare(false); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if err := atomicfile.ValidatePrivatePath(path, false); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	if err := atomicfile.ValidatePrivatePath(path+".lock", false); err != nil && !os.IsNotExist(err) {
		return nil, nil, storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, credentialFileLimit+1))
	if err != nil {
		return nil, nil, storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	if len(data) > credentialFileLimit || rejectDuplicateJSON(data) != nil {
		return nil, nil, storeFailure(ErrorCredentialStoreCorrupt, atomicfile.NotReplaced)
	}
	var file CredentialFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, nil, storeFailure(ErrorCredentialStoreCorrupt, atomicfile.NotReplaced)
	}
	if err := validateCredentialFile(t, &file); err != nil {
		return nil, nil, err
	}
	return &file, data, nil
}

func (s *CredentialStore) InspectFile(ctx context.Context, t Target) (*CredentialFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, _, err := s.read(t)
	if err != nil {
		s.forget(t.TargetID)
	}
	return f, err
}

type CredentialTransaction struct {
	File   *CredentialFile
	store  *CredentialStore
	target Target
	ctx    context.Context
	active bool
}

func (tx *CredentialTransaction) Commit() (atomicfile.ReplaceOutcome, error) {
	if !tx.active {
		return atomicfile.NotReplaced, storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	if err := tx.ctx.Err(); err != nil {
		tx.store.forget(tx.target.TargetID)
		return atomicfile.NotReplaced, err
	}
	if err := validateCredentialFile(tx.target, tx.File); err != nil {
		tx.store.forget(tx.target.TargetID)
		return atomicfile.NotReplaced, err
	}
	data, err := json.Marshal(tx.File)
	if err != nil || len(data) > credentialFileLimit {
		tx.store.forget(tx.target.TargetID)
		return atomicfile.NotReplaced, storeFailure(ErrorCredentialStoreCorrupt, atomicfile.NotReplaced)
	}
	return tx.store.write(tx.target, data)
}

func (s *CredentialStore) write(t Target, data []byte) (atomicfile.ReplaceOutcome, error) {
	path, err := s.path(t)
	if err != nil {
		s.forget(t.TargetID)
		return atomicfile.NotReplaced, err
	}
	if err := s.prepare(false); err != nil {
		s.forget(t.TargetID)
		return atomicfile.NotReplaced, storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	if err := atomicfile.ValidatePrivatePath(path, false); err != nil && !os.IsNotExist(err) {
		s.forget(t.TargetID)
		return atomicfile.NotReplaced, storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	outcome, err := s.replace(path, data, 0o600)
	if err != nil || outcome != atomicfile.Durable {
		s.forget(t.TargetID)
		return outcome, storeFailure(ErrorCredentialStoreFailed, outcome)
	}
	s.remember(t.TargetID, data)
	return outcome, nil
}

// UpdateLocked confirms existing bytes before exposing them to fn. The callback
// explicitly commits each durable transition, including pre-network markers.
// Missing files start with an empty slot map; the service owns slot creation.
func (s *CredentialStore) UpdateLocked(ctx context.Context, t Target, fn func(*CredentialTransaction) error) (result error) {
	defer func() {
		if result != nil {
			s.forget(t.TargetID)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(t)
	if err != nil {
		return err
	}
	if err := s.prepare(true); err != nil {
		return err
	}
	lockPath := path + ".lock"
	f, err := atomicfile.CreatePrivateFile(lockPath)
	if err == nil {
		err = f.Close()
	} else if os.IsExist(err) {
		err = nil
	}
	if err != nil {
		return storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	if err := atomicfile.ValidatePrivatePath(lockPath, false); err != nil {
		return storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	lockContext, cancelLock := context.WithTimeout(ctx, 25*time.Second)
	lock, err := config.LockConfigMutationContext(lockContext, path)
	cancelLock()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return storeFailure(ErrorCredentialStoreFailed, atomicfile.NotReplaced)
	}
	defer lock.Close()
	file, raw, err := s.read(t)
	if err != nil {
		return err
	}
	if file != nil && !s.known(t.TargetID, raw) {
		if _, err := s.write(t, raw); err != nil {
			return err
		}
	}
	if file == nil {
		file = &CredentialFile{SchemaVersion: 1, Target: CredentialTarget{t.Ref.WorkspaceID, t.Ref.Owner, t.Config.URL}, Slots: make(map[string]*CredentialSlot)}
	}
	tx := &CredentialTransaction{File: file, store: s, target: t, ctx: ctx, active: true}
	defer func() { tx.active = false }()
	return fn(tx)
}

func (s *CredentialStore) ReadForUse(ctx context.Context, t Target) (*CredentialFile, error) {
	var file *CredentialFile
	err := s.UpdateLocked(ctx, t, func(tx *CredentialTransaction) error { file = tx.File; return nil })
	return file, err
}
