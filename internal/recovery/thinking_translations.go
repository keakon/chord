package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/thinkingtranslate"
)

const thinkingTranslationsFileName = "thinking_translations.json"

type thinkingTranslationsSessionLock struct {
	mu sync.Mutex
}

var thinkingTranslationsLocks sync.Map

// ThinkingTranslationEntry is a durable UI-side translation for one thinking
// block. It is stored outside main.jsonl so conversation replay payloads remain
// unchanged while the TUI can restore previously generated translations.
type ThinkingTranslationEntry struct {
	AgentID      string    `json:"agent_id,omitempty"`
	MessageID    string    `json:"message_id"`
	BlockIndex   int       `json:"block_index"`
	TargetLang   string    `json:"target_lang"`
	OriginalHash string    `json:"original_sha256"`
	Translated   string    `json:"translated"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type thinkingTranslationsFile struct {
	Version int                        `json:"version"`
	Entries []ThinkingTranslationEntry `json:"entries,omitempty"`
}

// ThinkingTranslationOriginalHash returns the stable content hash used to bind a
// persisted translation to the exact thinking text it was generated from.
func ThinkingTranslationOriginalHash(original string) string {
	sum := sha256.Sum256([]byte(original))
	return hex.EncodeToString(sum[:])
}

// SaveThinkingTranslation stores or replaces the translation for one thinking
// block in the active session directory.
func SaveThinkingTranslation(sessionDir string, entry ThinkingTranslationEntry) error {
	sessionDir = normalizeThinkingTranslationsSessionDir(sessionDir)
	entry.MessageID = strings.TrimSpace(entry.MessageID)
	entry.TargetLang = strings.TrimSpace(entry.TargetLang)
	entry.Translated = thinkingtranslate.ExtractTranslationEnvelope(entry.Translated)
	if sessionDir == "" || entry.MessageID == "" || entry.BlockIndex < 0 || strings.TrimSpace(entry.Translated) == "" {
		return nil
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now().UTC()
	}

	lock := thinkingTranslationsSessionLockFor(sessionDir)
	lock.mu.Lock()
	defer lock.mu.Unlock()

	file, err := loadThinkingTranslationsFileLocked(sessionDir)
	if err != nil {
		return err
	}
	if file.Version == 0 {
		file.Version = 1
	}

	replaced := false
	for i := range file.Entries {
		if thinkingTranslationSameSlot(file.Entries[i], entry) {
			file.Entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		file.Entries = append(file.Entries, entry)
	}
	return writeThinkingTranslationsFileLocked(sessionDir, file)
}

// LoadThinkingTranslations reads durable thinking translations from a session
// directory. Missing files are treated as empty.
func LoadThinkingTranslations(sessionDir string) ([]ThinkingTranslationEntry, error) {
	sessionDir = normalizeThinkingTranslationsSessionDir(sessionDir)
	if sessionDir == "" {
		return nil, nil
	}

	lock := thinkingTranslationsSessionLockFor(sessionDir)
	lock.mu.Lock()
	defer lock.mu.Unlock()

	file, err := loadThinkingTranslationsFileLocked(sessionDir)
	if err != nil {
		return nil, err
	}
	entries := make([]ThinkingTranslationEntry, len(file.Entries))
	copy(entries, file.Entries)
	return entries, nil
}

func normalizeThinkingTranslationsSessionDir(sessionDir string) string {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return ""
	}
	clean := filepath.Clean(sessionDir)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return clean
	}
	return abs
}

func thinkingTranslationsSessionLockFor(sessionDir string) *thinkingTranslationsSessionLock {
	if sessionDir == "" {
		return nil
	}
	if lock, ok := thinkingTranslationsLocks.Load(sessionDir); ok {
		return lock.(*thinkingTranslationsSessionLock)
	}
	newLock := &thinkingTranslationsSessionLock{}
	actual, _ := thinkingTranslationsLocks.LoadOrStore(sessionDir, newLock)
	return actual.(*thinkingTranslationsSessionLock)
}

func thinkingTranslationSameSlot(a, b ThinkingTranslationEntry) bool {
	return strings.TrimSpace(a.AgentID) == strings.TrimSpace(b.AgentID) &&
		strings.TrimSpace(a.MessageID) == strings.TrimSpace(b.MessageID) &&
		a.BlockIndex == b.BlockIndex
}

func thinkingTranslationsPath(sessionDir string) string {
	return filepath.Join(sessionDir, thinkingTranslationsFileName)
}

func loadThinkingTranslationsFileLocked(sessionDir string) (thinkingTranslationsFile, error) {
	var file thinkingTranslationsFile
	data, err := os.ReadFile(thinkingTranslationsPath(sessionDir))
	if err != nil {
		if os.IsNotExist(err) {
			return thinkingTranslationsFile{Version: 1}, nil
		}
		return file, fmt.Errorf("load thinking translations: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return thinkingTranslationsFile{Version: 1}, nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, fmt.Errorf("parse thinking translations: %w", err)
	}
	if file.Version == 0 {
		file.Version = 1
	}
	return file, nil
}

func writeThinkingTranslationsFileLocked(sessionDir string, file thinkingTranslationsFile) error {
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal thinking translations: %w", err)
	}
	data = append(data, '\n')
	tmpPath := filepath.Join(sessionDir, fmt.Sprintf(".%s.%d.tmp", thinkingTranslationsFileName, time.Now().UnixNano()))
	if err := privatefs.WriteFile(sessionDir, tmpPath, data); err != nil {
		return fmt.Errorf("write thinking translations temp: %w", err)
	}
	if err := os.Rename(tmpPath, thinkingTranslationsPath(sessionDir)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace thinking translations: %w", err)
	}
	return nil
}
