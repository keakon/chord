package config

import (
	"strings"
)

// ClaudeEmulationConfig defines the payload for adding the Claude Code client's
// request identity on top of a Messages provider's own wire: the client's
// request headers, a deterministic session UUID, and the metadata.user_id
// payload it sends. Providers and models opt in by name through their emulate
// key; defining the payload here enables nothing by itself.
//
// All three version fields are required and none has a built-in default: the
// values are user-supplied so that updating Chord can never silently change the
// wire identity a relay has already seen, which shipping captured constants
// would do.
type ClaudeEmulationConfig struct {
	// Version is the Claude Code CLI version to imitate, for example "2.1.311".
	// It rides in the User-Agent.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	// SDKVersion is the @anthropic-ai/sdk version the client bundles, reported
	// in X-Stainless-Package-Version.
	SDKVersion string `json:"sdk_version,omitempty" yaml:"sdk_version,omitempty"`
	// RuntimeVersion is the Node.js version the client runs on, reported in
	// X-Stainless-Runtime-Version.
	RuntimeVersion string `json:"runtime_version,omitempty" yaml:"runtime_version,omitempty"`
}

// ClaudeEmulationEnabled reports whether the payload is complete enough to
// imitate the client: all three version fields are filled in. A nil payload is
// unset and therefore disabled. There is no separate switch to turn: an
// incomplete payload is already unusable, so an enabled flag would only
// restate the version payload.
func (c *ClaudeEmulationConfig) ClaudeEmulationEnabled() bool {
	return c != nil && strings.TrimSpace(c.Version) != "" &&
		strings.TrimSpace(c.SDKVersion) != "" && strings.TrimSpace(c.RuntimeVersion) != ""
}

func (c *ClaudeEmulationConfig) emulationEnabled() bool { return c.ClaudeEmulationEnabled() }
