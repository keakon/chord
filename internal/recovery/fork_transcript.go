package recovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
)

// SeedForkTranscript publishes a complete fork after every attachment and
// message has been prepared. The caller holds the destination session lock and
// saves its metadata first. Failed or interrupted preparation never exposes a
// partial main.jsonl to session discovery.
func SeedForkTranscript(sessionDir string, count int, prepare func(int) (message.Message, error)) ([]message.Message, error) {
	path := filepath.Join(sessionDir, identity.MainSessionLogFilename)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		if err == nil {
			return nil, fmt.Errorf("fork transcript already exists")
		}
		return nil, fmt.Errorf("check fork transcript: %w", err)
	}
	tmp := path + ".tmp"
	f, err := privatefs.OpenFile(sessionDir, tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return nil, fmt.Errorf("create fork transcript: %w", err)
	}
	defer os.Remove(tmp)
	defer f.Close()
	manager := NewRecoveryManager(sessionDir)
	defer manager.Close()
	encoder := json.NewEncoder(f)
	messages := make([]message.Message, count)
	for i := range count {
		msg, err := prepare(i)
		if err != nil {
			return nil, fmt.Errorf("prepare fork message %d: %w", i, err)
		}
		msg, err = manager.persistBinaryParts(msg, true)
		if err != nil {
			return nil, fmt.Errorf("persist fork attachment %d: %w", i, err)
		}
		if err := encoder.Encode(msg); err != nil {
			return nil, fmt.Errorf("encode fork message %d: %w", i, err)
		}
		messages[i] = msg
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("sync fork transcript: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close fork transcript: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("publish fork transcript: %w", err)
	}
	if err := privatefs.SyncDir(sessionDir); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("sync fork directory: %w", err)
	}
	return messages, nil
}
