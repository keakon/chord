package llm

import (
	"strings"

	"github.com/keakon/chord/internal/config"
)

// codexShapeEmulated reports whether requests for this model should be written
// the way the Codex client writes them. preset: codex selects the Codex
// transport, endpoint, and zstd request bodies on its own; the emulate selector
// adds the client's own request-body shape and User-Agent on top, so a user who
// only wants the transport keeps Chord's body layout.
//
// The selector only acts on a Responses provider: the Codex body shape — base
// instructions as an input item, the tool fields always present, a User-Agent
// carrying the client version — is part of the Responses contract and would be
// meaningless or rejected on chat-completions, messages, or generate-content.
// A selector referencing an incomplete payload imitates nothing; the loader
// reports such selectors as config errors. The emulation payload and type are
// set once at construction, then read-only.
func (p *ProviderConfig) codexShapeEmulated(model string) bool {
	return p != nil && p.emulateFor(model) == config.EmulationClientCodex &&
		p.codexEmulationVersion() != "" &&
		strings.EqualFold(strings.TrimSpace(p.typeName), config.ProviderTypeResponses)
}

// codexEmulationVersion returns the configured Codex version to imitate, or ""
// when no payload is attached.
func (p *ProviderConfig) codexEmulationVersion() string {
	if p.emulation == nil || p.emulation.Codex == nil {
		return ""
	}
	return p.emulation.Codex.Version
}

// codexEmulationUserAgent returns the User-Agent string the Codex client would
// send for this model, or "" when there is nothing to imitate: no emulate
// selector, a provider that is not Responses, or no configured version.
func (p *ProviderConfig) codexEmulationUserAgent(model string) string {
	if !p.codexShapeEmulated(model) {
		return ""
	}
	return codexClientUserAgent(p.codexEmulationVersion())
}
