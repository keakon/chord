package llm

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

// Claude Code client emulation for the messages wire.
//
// The client identity (shape captured with claude-code-inspector against
// Claude Code v2.1.308/311, 2026-10-29) is additive on top of Chord's own
// request: the client's request headers, a deterministic session UUID shared by
// the X-Claude-Code-Session-Id header and metadata.user_id, and nothing else —
// the body keeps Chord's layout. A provider/model emulate selector opts into
// the top-level emulation.claude payload, and only a Messages provider acts on it.

// Header names the emulation owns. X-Claude-Code-Session-Id and the identity
// X-Stainless-* values must never be overridable from a hosted declaration, so
// they are mirrored into the hosted header blacklist by their wire names.
const (
	headerAnthropicDangerousDirectBrowserAccess = "Anthropic-Dangerous-Direct-Browser-Access"
	headerXStainlessLang                        = "X-Stainless-Lang"
	headerXStainlessRuntime                     = "X-Stainless-Runtime"
	headerXStainlessOS                          = "X-Stainless-OS"
	headerXStainlessArch                        = "X-Stainless-Arch"
	headerXStainlessPackageVersion              = "X-Stainless-Package-Version"
	headerXStainlessRuntimeVersion              = "X-Stainless-Runtime-Version"
	headerXStainlessRetryCount                  = "X-Stainless-Retry-Count"
	headerXStainlessTimeout                     = "X-Stainless-Timeout"
	headerXClaudeCodeSessionID                  = "X-Claude-Code-Session-Id"
	headerValueAnthropicDangerousDirectBrowser  = "true"
	headerValueStainlessLang                    = "js"
	headerValueStainlessRuntime                 = "node"
	headerValueStainlessRetryCount              = "0"
)

const (
	// claudeEmulationDefaultTimeoutSeconds is the per-request timeout the
	// captured client reports in X-Stainless-Timeout; Chord falls back to it
	// when no stream cap is configured.
	claudeEmulationDefaultTimeoutSeconds = 600
)

// claudeSessionSeedDomain separates the Claude session UUID's hash input from
// the Codex wire identity, which hashes the same local session keys: without
// it, the two derivations would publish overlapping slices of one digest and
// make the two client identities correlatable.
const claudeSessionSeedDomain = "chord-claude-session\x00"

// claudeClientUserAgent renders the User-Agent the Claude Code client sends:
//
//	claude-cli/<version> (external, cli)
//
// The version is the one piece the user may configure; the rest is the fixed
// shape the client prints for an external CLI install.
func claudeClientUserAgent(version string) string {
	return "claude-cli/" + sanitizeUserAgentProductVersion(version) + " (external, cli)"
}

// claudeEmulationActive reports whether requests for this model should carry
// the Claude Code client identity. Only a Messages provider acts on it: the
// imitated headers and metadata belong to that protocol, and the messages wire
// is where third-party relays check the client contract. The emulation payload
// and type are set once at construction, then read-only.
func (p *ProviderConfig) claudeEmulationActive(model string) bool {
	return p != nil && p.emulateFor(model) == config.EmulationClientClaude &&
		p.claudeEmulationPayload().ClaudeEmulationEnabled() &&
		strings.EqualFold(strings.TrimSpace(p.typeName), config.ProviderTypeMessages)
}

// claudeEmulationPayload returns the Claude payload definitions attached at
// load, or nil when there are none.
func (p *ProviderConfig) claudeEmulationPayload() *config.ClaudeEmulationConfig {
	if p.emulation == nil {
		return nil
	}
	return p.emulation.Claude
}

// claudeEmulationUserAgent returns the User-Agent the Claude Code client would
// send for this model, or "" when there is nothing to imitate. The
// emulation.claude and emulation.codex derived agents are mutually exclusive by
// provider type: one gates on Messages, the other on Responses.
func (p *ProviderConfig) claudeEmulationUserAgent(model string) string {
	if !p.claudeEmulationActive(model) {
		return ""
	}
	return claudeClientUserAgent(p.claudeEmulationPayload().Version)
}

// applyClaudeEmulationHeaders sets the request headers the Claude Code client
// sends. streamTotalTimeout is the provider's wall-clock stream cap: the
// client's own header reports its per-request budget, so a configured cap is
// the honest value to report and no cap falls back to the captured client's
// default. A missing session key leaves X-Claude-Code-Session-Id out, matching
// the metadata field the same key drives.
func applyClaudeEmulationHeaders(h http.Header, p *ProviderConfig, sessionKey string, streamTotalTimeout time.Duration) {
	h.Set(headerAccept, headerValueApplicationJSON)
	h.Set(headerAnthropicDangerousDirectBrowserAccess, headerValueAnthropicDangerousDirectBrowser)
	h.Set(headerXStainlessLang, headerValueStainlessLang)
	h.Set(headerXStainlessRuntime, headerValueStainlessRuntime)
	h.Set(headerXStainlessOS, claudeStainlessOS(runtime.GOOS))
	h.Set(headerXStainlessArch, claudeStainlessArch(runtime.GOARCH))
	h.Set(headerXStainlessPackageVersion, p.claudeEmulationPayload().SDKVersion)
	h.Set(headerXStainlessRuntimeVersion, p.claudeEmulationPayload().RuntimeVersion)
	h.Set(headerXStainlessRetryCount, headerValueStainlessRetryCount)
	h.Set(headerXStainlessTimeout, strconv.Itoa(claudeStainlessTimeoutSeconds(streamTotalTimeout)))
	if sessionID := claudeSessionID(sessionKey); sessionID != "" {
		h.Set(headerXClaudeCodeSessionID, sessionID)
	}
}

// claudeStainlessTimeoutSeconds renders X-Stainless-Timeout. An unset cap
// means Chord has no wall-clock budget worth reporting; the client always
// carries one, so the default stands in. A sub-second cap also falls back to
// it: the header reports whole seconds, and a sub-second budget would render
// as 0.
func claudeStainlessTimeoutSeconds(streamTotalTimeout time.Duration) int {
	if streamTotalTimeout > 0 {
		if seconds := int(streamTotalTimeout / time.Second); seconds > 0 {
			return seconds
		}
	}
	return claudeEmulationDefaultTimeoutSeconds
}

// claudeStainlessOS maps GOOS to the X-Stainless-OS value the Stainless JS
// core sends (its normalizePlatform vocabulary).
func claudeStainlessOS(goos string) string {
	switch goos {
	case "darwin":
		return "MacOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return "Unknown"
	}
}

// claudeStainlessArch maps GOARCH to the X-Stainless-Arch value the Stainless
// JS core sends (its normalizeArch vocabulary).
func claudeStainlessArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "x32"
	case "arm":
		return "arm"
	default:
		return "unknown"
	}
}

// deterministicUUIDv4 builds a UUIDv4-shaped identifier from seed: version and
// variant bits are set, the remaining 122 bits come from the SHA-256 of seed.
// Unlike a random UUID it is reproducible from its inputs, so a resumed or
// freshly started process recomputes the same client identity. There is no
// timestamp: a v4 must not carry one, both because real v4s are random and
// because a time-ordered value would leak the session's creation moment.
func deterministicUUIDv4(seed []byte) string {
	sum := sha256.Sum256(seed)
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

// claudeSessionID derives the Claude Code session identity from the local
// session key: deterministic across restarts, resumes and compactions, and
// distinct per SubAgent because the delegated keys already carry a ":sub:"
// suffix. The provider name is deliberately not mixed in, so a fallback to
// another endpoint keeps the same client identity for sticky routing. An empty
// result means the key was missing; the header and the metadata field it feeds
// are both omitted rather than sent in a wrong shape.
func claudeSessionID(sessionKey string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return ""
	}
	return deterministicUUIDv4([]byte(claudeSessionSeedDomain + sessionKey))
}

// claudeDeviceIDPathFn and the mint function are injection points: the sync.
// once cache below would otherwise make both failure paths and the
// persistence-race path untouchable from a test.
var (
	claudeDeviceIDPathFn = func() (string, error) { return config.ClaudeDeviceIDPath() }
	claudeDeviceIDMint   = mintClaudeDeviceID
)

// mintClaudeDeviceID returns a fresh device id: 32 CSPRNG bytes as 64 hex
// characters, the shape of the device_id the captured client reports. An empty
// result means the CSPRNG failed.
func mintClaudeDeviceID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// claudeDeviceID caches the machine-level device id for the emulation: it is
// read once and reused for every request, the way the real client loads its
// persisted userID once. An empty result means no usable id exists and the
// metadata upgrade is skipped.
var claudeDeviceID = sync.OnceValue(func() string {
	path, err := claudeDeviceIDPathFn()
	if err != nil {
		log.Warnf("emulation.claude: cannot resolve the device id path error=%v", err)
		return ""
	}
	return loadClaudeDeviceID(path, claudeDeviceIDMint)
})

// loadClaudeDeviceID returns the device id persisted at path, minting and
// persisting one when the file does not carry it yet. Two degradations are
// tolerated with a warning: a write failure keeps the freshly minted id for
// this process only (the shape stays correct, but it may rotate across
// restarts), and a failed mint returns "" so the caller falls back to the
// baseline metadata instead of sending a broken shape.
func loadClaudeDeviceID(path string, mint func() string) string {
	if id := readDeviceIDFile(path); id != "" {
		return id
	}
	id := mint()
	if id == "" {
		log.Warnf("emulation.claude: cannot generate a device id, keeping the baseline metadata.user_id")
		return ""
	}
	id, err := persistDeviceID(path, id)
	if err != nil {
		log.Warnf("emulation.claude: cannot persist the device id, using an unpersisted id path=%v error=%v", path, err)
	}
	return id
}

// anthropicMetadataUserIDPayload resolves metadata.user_id for one request.
// Without the emulation this is Chord's own stable payload, memoized per
// provider as before. With it, the payload upgrades to the captured client's
// shape: a machine-level device_id independent of the session, and a
// session_id derived from the local session key — omitted when the key is
// missing, because a wrong shape is worse than a short one.
func (a *AnthropicProvider) anthropicMetadataUserIDPayload(model, sessionKey string) string {
	if !a.provider.claudeEmulationActive(model) {
		return stableAnthropicMetadataUserIDPayload(a.provider)
	}
	deviceID := claudeDeviceID()
	if deviceID == "" {
		return stableAnthropicMetadataUserIDPayload(a.provider)
	}
	return claudeEmulatedMetadataUserID(deviceID, sessionKey)
}

// claudeEmulatedMetadataUserID renders the captured client's metadata.user_id
// for one request. The session_id field is dropped rather than sent empty when
// no session key is bound: a payload missing the field is indistinguishable
// from an identity-less client, while a wrong-shaped one is a fingerprint.
func claudeEmulatedMetadataUserID(deviceID, sessionKey string) string {
	if sessionID := claudeSessionID(sessionKey); sessionID != "" {
		return fmt.Sprintf(`{"device_id":"%s","account_uuid":"","session_id":"%s"}`, deviceID, sessionID)
	}
	return fmt.Sprintf(`{"device_id":"%s","account_uuid":""}`, deviceID)
}
