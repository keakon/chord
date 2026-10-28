package llm

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
)

const testSessionKey = "20261028120000123"

func TestDeriveResponsesWireIdentityIsStablePerLocalKey(t *testing.T) {
	first := deriveResponsesWireIdentity(testSessionKey, 0, false)
	second := deriveResponsesWireIdentity(testSessionKey, 0, false)
	if first != second {
		t.Fatalf("identity differs across calls for one key: %+v vs %+v", first, second)
	}
	other := deriveResponsesWireIdentity("20261028120000124", 0, false)
	if other.sessionID == first.sessionID {
		t.Fatalf("two session keys mapped to the same id %q", first.sessionID)
	}
	if !isUUIDv7(first.sessionID) {
		t.Fatalf("session id %q is not a UUIDv7", first.sessionID)
	}
	if strings.Contains(first.sessionID, testSessionKey) {
		t.Fatalf("session id %q still carries the local key", first.sessionID)
	}
}

func TestDeriveResponsesWireIdentityUsesSessionTimestamp(t *testing.T) {
	ms, ok := sidUnixMillis(testSessionKey)
	if !ok {
		t.Fatalf("session key %q does not parse as a session id", testSessionKey)
	}
	id := deriveResponsesWireIdentity(testSessionKey, 0, false)
	raw, err := hex.DecodeString(strings.ReplaceAll(id.sessionID, "-", ""))
	if err != nil {
		t.Fatalf("session id %q: %v", id.sessionID, err)
	}
	// The 48-bit UUIDv7 timestamp carries the session's own creation instant, so
	// ids a relay logs can be ordered alongside the sessions they belong to.
	if got := int64(binary.BigEndian.Uint64(append([]byte{0, 0}, raw[:6]...))); got != ms {
		t.Fatalf("uuid timestamp = %d, want the session id timestamp %d", got, ms)
	}
}

func TestDeriveResponsesWireIdentityWindowAndThreadSource(t *testing.T) {
	base := deriveResponsesWireIdentity(testSessionKey, 0, false)
	if base.windowID != base.sessionID+":0" {
		t.Fatalf("window id = %q, want %q", base.windowID, base.sessionID+":0")
	}
	if base.threadSource != codexThreadSourceUser {
		t.Fatalf("thread source = %q, want %q", base.threadSource, codexThreadSourceUser)
	}
	sub := deriveResponsesWireIdentity(testSessionKey+":sub:builder-3", 0, true)
	if sub.threadSource != codexThreadSourceSubagent {
		t.Fatalf("subagent thread source = %q, want %q", sub.threadSource, codexThreadSourceSubagent)
	}
	if sub.sessionID == base.sessionID {
		t.Fatalf("subagent shares the parent thread id %q", sub.sessionID)
	}

	compacted := deriveResponsesWireIdentity(testSessionKey, 3, false)
	if compacted.windowID != base.sessionID+":3" {
		t.Fatalf("window id after compaction = %q, want %q", compacted.windowID, base.sessionID+":3")
	}
	if compacted.sessionID != base.sessionID {
		t.Fatalf("compaction changed the thread id: %q vs %q", compacted.sessionID, base.sessionID)
	}
	if compacted.contextWindowID == base.contextWindowID {
		t.Fatalf("context window id did not advance with the window number: %q", compacted.contextWindowID)
	}
	if compacted.cacheKey() == base.cacheKey() {
		t.Fatal("cache identity ignores the window number, so a compacted request could reuse the stale body")
	}
}

func TestDeriveResponsesWireIdentityWithoutSessionKey(t *testing.T) {
	if got := deriveResponsesWireIdentity("  ", 2, false); got != (responsesWireIdentity{}) {
		t.Fatalf("blank key produced an identity: %+v", got)
	}
}

func TestBaseInstructionsItemIDIsStable(t *testing.T) {
	thread := deriveResponsesWireIdentity(testSessionKey, 0, false).sessionID
	first := baseInstructionsItemID(thread, "you are a helpful assistant")
	if second := baseInstructionsItemID(thread, "you are a helpful assistant"); second != first {
		t.Fatalf("item id drifts for one thread and text: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "msg_") || !isUUIDv5Like(strings.TrimPrefix(first, "msg_")) {
		t.Fatalf("item id = %q, want msg_ plus a UUIDv5", first)
	}
	if got := baseInstructionsItemID(thread, "other instructions"); got == first {
		t.Fatal("different instruction text produced the same item id")
	}
	if got := baseInstructionsItemID("11111111-1111-4111-8111-111111111111", "you are a helpful assistant"); got == first {
		t.Fatal("item id does not depend on the thread it belongs to")
	}
}

func TestLoadInstallationIDPrefersCodexFile(t *testing.T) {
	dir := t.TempDir()
	codexPath := filepath.Join(dir, ".codex", config.CodexInstallationIDFileName)
	chordPath := filepath.Join(dir, "chord", config.CodexDeviceIDFileName)
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPath, []byte("codex-install\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadInstallationID(codexPath, chordPath); got != "codex-install" {
		t.Fatalf("installation id = %q, want the one the Codex CLI already stores", got)
	}
	if _, err := os.Stat(chordPath); !os.IsNotExist(err) {
		t.Fatalf("chord installation file written next to a Codex one: %v", err)
	}
}

func TestLoadInstallationIDCreatesAndReusesChordFile(t *testing.T) {
	dir := t.TempDir()
	chordPath := filepath.Join(dir, "chord", config.CodexDeviceIDFileName)
	first := loadInstallationID(filepath.Join(dir, "missing", config.CodexInstallationIDFileName), chordPath)
	if !isUUIDv4Like(first) {
		t.Fatalf("installation id = %q, want a generated UUID", first)
	}
	data, err := os.ReadFile(chordPath)
	if err != nil {
		t.Fatalf("installation id was not persisted: %v", err)
	}
	if strings.TrimSpace(string(data)) != first {
		t.Fatalf("persisted id = %q, want %q", strings.TrimSpace(string(data)), first)
	}
	info, err := os.Stat(chordPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("installation id file mode = %v, want -rw-------", info.Mode().Perm())
	}
	if got := loadInstallationID("", chordPath); got != first {
		t.Fatalf("second resolution = %q, want the stored %q", got, first)
	}
}

// The claude device id loader has the same race; both must converge on one
// machine-level id when two processes first initialize together.
func TestLoadInstallationIDAdoptsConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", config.CodexDeviceIDFileName)
	results := make([]string, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			results[i] = loadInstallationID("", path)
		})
	}
	wg.Wait()
	if !isUUIDv4Like(results[0]) {
		t.Fatalf("installation id = %q, want a generated UUID", results[0])
	}
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Fatalf("concurrent loader %d got %q, want the winner's id %q", i, results[i], results[0])
		}
	}
}

func TestNewRandomUUIDv7Shape(t *testing.T) {
	id := newRandomUUIDv7()
	if !isUUIDv7(id) {
		t.Fatalf("turn id %q is not a UUIDv7", id)
	}
	if other := newRandomUUIDv7(); other == id {
		t.Fatal("two turn ids came out identical")
	}
}

func isUUIDv7(id string) bool {
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(raw) == 16 && raw[6]&0xf0 == 0x70 && raw[8]&0xc0 == 0x80
}

func isUUIDv4Like(id string) bool {
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(raw) == 16 && raw[6]&0xf0 == 0x40 && raw[8]&0xc0 == 0x80
}

func isUUIDv5Like(id string) bool {
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(raw) == 16 && raw[6]&0xf0 == 0x50 && raw[8]&0xc0 == 0x80
}
