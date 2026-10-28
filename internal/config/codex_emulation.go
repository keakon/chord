package config

import (
	"strings"
)

// CodexEmulationConfig defines the payload for presenting as the Codex client
// on Responses providers: the request-body shape that client writes, on top of
// the transport that preset: codex already selects. Providers and models opt in
// by name through their emulate key; defining the payload here enables nothing
// by itself.
type CodexEmulationConfig struct {
	// Version is the Codex client version to imitate, for example "0.50.0".
	// Codex reports its version nowhere else on the wire, so there is no default
	// to fall back on: without a version there is no client to imitate and the
	// whole payload has no effect.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
}

// CodexEmulationEnabled reports whether the payload is complete enough to
// imitate the client: it names the Codex version to imitate. A nil payload is
// unset and therefore disabled. There is no separate switch to turn: an
// incomplete payload is already unusable, so an enabled flag would only
// restate the version.
func (c *CodexEmulationConfig) CodexEmulationEnabled() bool {
	return c != nil && strings.TrimSpace(c.Version) != ""
}

func (c *CodexEmulationConfig) emulationEnabled() bool { return c.CodexEmulationEnabled() }
