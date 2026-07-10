package sessionimport

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/keakon/chord/internal/privatefs"
)

const (
	ReasoningOff     = "off"
	ReasoningVisible = "visible"
	ReasoningStrict  = "strict"
)

func normalizeReasoningMode(raw string) (string, error) {
	mode := strings.TrimSpace(raw)
	if mode == "" {
		return ReasoningStrict, nil
	}
	switch mode {
	case ReasoningOff, ReasoningVisible, ReasoningStrict:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid --reasoning %q (allowed: off, visible, strict)", mode)
	}
}

func newImportingDirName(sessionID string) string {
	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	return ".importing-" + sessionID + "-" + hex.EncodeToString(suffix)
}

// allocateSessionID generates a session ID in the same timestamp+millis format
// used by recovery.CreateNewSessionDir, without touching the filesystem.
func allocateSessionID(last string) (id string, nextLast string) {
	now := time.Now().UTC()
	id = now.Format("20060102150405") + fmt.Sprintf("%03d", now.Nanosecond()/int(time.Millisecond))
	if id == last {
		time.Sleep(time.Millisecond)
		now = time.Now().UTC()
		id = now.Format("20060102150405") + fmt.Sprintf("%03d", now.Nanosecond()/int(time.Millisecond))
	}
	return id, id
}

func writeJSONAtomic(root, path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := privatefs.WriteFile(root, tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
