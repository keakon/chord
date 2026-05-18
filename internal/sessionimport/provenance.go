package sessionimport

import "github.com/keakon/chord/internal/message"

func importedCodexProvenance() *message.MessageProvenance {
	return &message.MessageProvenance{
		Source:     "import:codex",
		ProviderID: "openai",
		WireFamily: "openai-responses",
		Imported:   true,
	}
}

func importedClaudeProvenance() *message.MessageProvenance {
	return &message.MessageProvenance{
		Source:     "import:claude",
		Imported:   true,
		WireFamily: "anthropic",
	}
}
