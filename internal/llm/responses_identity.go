package llm

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

// Codex wire identity.
//
// Chord's own session key is a local identifier (the session directory name,
// plus a ":sub:<instance>" suffix for SubAgents). The Codex client identity that
// goes on the wire is a UUIDv7, so each local key is mapped deterministically:
// the same key always yields the same UUID across requests, resumes and process
// restarts, which is what keeps provider-side prompt-cache affinity from
// breaking mid-session.
const sidTimeLayout = "20060102150405"

const (
	codexThreadSourceUser     = "user"
	codexThreadSourceSubagent = "subagent"
)

// responsesWireIdentity is the resolved client identity for one request.
type responsesWireIdentity struct {
	sessionID       string // UUIDv7 derived from the local session key
	windowID        string // "{sessionID}:{windowNumber}", Codex's x-codex-window-id shape
	windowNumber    uint64
	contextWindowID string // UUIDv7 for this context window
	threadSource    string // "user" | "subagent"
}

// deriveResponsesWireIdentity maps a local session key to the Codex-shaped
// client identity. windowNumber is the count of durable context compactions
// already applied in this session, so the window identity only changes when the
// history is actually rewritten. subAgent sessions report the "subagent" thread
// source; every other session is a root "user" thread.
func deriveResponsesWireIdentity(localKey string, windowNumber uint64, subAgent bool) responsesWireIdentity {
	localKey = strings.TrimSpace(localKey)
	if localKey == "" {
		return responsesWireIdentity{}
	}
	threadSource := codexThreadSourceUser
	if subAgent {
		threadSource = codexThreadSourceSubagent
	}
	seed := sha256.Sum256([]byte(localKey))
	unixMs, ok := sidUnixMillis(localKey)
	if !ok {
		// Not a Chord session id (a test fixture, an externally supplied id):
		// still mint a stable UUIDv7, taking the 48-bit timestamp from the hash
		// so the value stays deterministic without consulting a clock.
		unixMs = int64(binary.BigEndian.Uint64(seed[:8]) & ((1 << 48) - 1))
	}
	sessionID := deterministicUUIDv7(unixMs, seed[:])
	windowSeed := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", sessionID, windowNumber)))
	return responsesWireIdentity{
		sessionID:       sessionID,
		windowID:        fmt.Sprintf("%s:%d", sessionID, windowNumber),
		windowNumber:    windowNumber,
		contextWindowID: deterministicUUIDv7(unixMs, windowSeed[:]),
		threadSource:    threadSource,
	}
}

// cacheKey distinguishes request bodies that would otherwise differ only inside
// client_metadata: the cached body carries this identity, so a compaction that
// advances the window cannot be served a body stamped with the previous window.
func (id responsesWireIdentity) cacheKey() string {
	return strings.Join([]string{id.sessionID, id.windowID, id.threadSource}, "\x00")
}

// sidUnixMillis reads the local timestamp out of a Chord session id
// ("20060102150405" + millisecond digits), which is how the session directory
// is named. ok=false when the key does not start with a session id.
//
// The key has no time zone. Interpret its digits as UTC for deterministic
// identity, not as the session's actual creation instant.
func sidUnixMillis(key string) (int64, bool) {
	if len(key) < len(sidTimeLayout)+3 {
		return 0, false
	}
	base, err := time.Parse(sidTimeLayout, key[:len(sidTimeLayout)])
	if err != nil {
		return 0, false
	}
	ms, err := strconv.Atoi(key[len(sidTimeLayout) : len(sidTimeLayout)+3])
	if err != nil || ms < 0 || ms > 999 {
		return 0, false
	}
	return base.UnixMilli() + int64(ms), true
}

// deterministicUUIDv7 builds a UUIDv7-shaped identifier: the 48-bit
// millisecond timestamp, then version/variant bits, with the remaining 74 bits
// taken from seed. Unlike a random UUID it is reproducible from its inputs, so
// a resumed or freshly started process recomputes the same client identity.
func deterministicUUIDv7(unixMs int64, seed []byte) string {
	var b [16]byte
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(unixMs))
	copy(b[:6], ts[2:])
	b[6] = 0x70 | (seed[0] & 0x0f)
	b[7] = seed[1]
	b[8] = 0x80 | (seed[2] & 0x3f)
	copy(b[9:], seed[3:10])
	return formatUUID(b)
}

// uuidv5NamespaceOID is RFC 4122's OID namespace, the one Codex uses to derive
// stable identifiers for the request prefix items.
var uuidv5NamespaceOID = mustDecodeUUID("6ba7b812-9dad-11d1-80b4-00c04fd430c8")

// baseInstructionsItemID is the id Codex gives the base-instructions input item
// that opens a request: "msg_" and a UUIDv5 chained from the thread id over the
// instruction text. Rebuilding it from the same text keeps the id stable across
// retries and resumed sessions, so the cached prefix stays byte-identical.
func baseInstructionsItemID(threadID, text string) string {
	namespace := uuidv5(uuidv5NamespaceOID, []byte(threadID))
	return "msg_" + formatUUID(uuidv5(namespace, []byte(text)))
}

// uuidv5 implements RFC 4122 version 5 (SHA-1 name-based) UUIDs over a raw
// namespace.
func uuidv5(namespace [16]byte, name []byte) [16]byte {
	sum := sha1.Sum(append(namespace[:], name...))
	var uuid [16]byte
	copy(uuid[:], sum[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x50
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return uuid
}

func mustDecodeUUID(s string) [16]byte {
	raw, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(raw) != 16 {
		panic("llm: invalid UUID constant " + s)
	}
	var b [16]byte
	copy(b[:], raw)
	return b
}

func formatUUID(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// newRandomUUID mints a UUIDv4 for identifiers that must differ per machine or
// per turn rather than be reproducible from a name.
func newRandomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

// newRandomUUIDv7 mints a time-ordered UUIDv7, the shape Codex gives its turn
// ids. An empty result means the system CSPRNG failed; callers treat it as "no
// identity yet" rather than substituting a clock-derived stand-in, which would
// sort differently from the UUIDs minted on other machines.
func newRandomUUIDv7() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(time.Now().UnixMilli()))
	copy(b[:6], ts[2:])
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

// installationID is the machine-level client installation id Codex reports in
// x-codex-installation-id. It is read once and reused for every session: a
// per-session value would make one machine look like many distinct clients.
//
// The Codex CLI's own file wins when it exists so both clients on the machine
// report the same id; otherwise Chord persists its own under the config home
// (codex_device_id, a name no other agent writes).
var installationID = sync.OnceValue(func() string {
	chordPath, err := config.CodexDeviceIDPath()
	if err != nil {
		log.Warnf("codex identity: cannot resolve the device id path error=%v", err)
		return ""
	}
	codexPath, err := config.CodexInstallationIDPath()
	if err != nil {
		log.Warnf("codex identity: cannot resolve the Codex installation id path error=%v", err)
		codexPath = ""
	}
	return loadInstallationID(codexPath, chordPath)
})

// loadInstallationID reads the installation id from codexPath, then from
// chordPath, and persists a freshly minted one to chordPath when neither file
// carries it yet.
func loadInstallationID(codexPath, chordPath string) string {
	if id := readDeviceIDFile(codexPath); id != "" {
		return id
	}
	if id := readDeviceIDFile(chordPath); id != "" {
		return id
	}
	return persistInstallationFile(chordPath)
}

// persistInstallationFile writes a freshly minted id, tolerating a concurrent
// writer: O_EXCL loses the race instead of clobbering the id another request
// just stored.
func persistInstallationFile(path string) string {
	id := newRandomUUID()
	if id == "" {
		log.Warnf("codex identity: cannot generate an installation id, sending requests without it")
		return ""
	}
	id, err := persistDeviceID(path, id)
	if err != nil {
		log.Warnf("codex identity: cannot persist the installation id, using it for this process only path=%v error=%v", path, err)
	}
	return id
}
