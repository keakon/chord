package agent

import (
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func mainAssistantProvenance(a *MainAgent) *message.MessageProvenance {
	if a == nil {
		return nil
	}
	a.llmMu.RLock()
	client := a.llmClient
	selectedRef := strings.TrimSpace(a.providerModelRef)
	runningRef := strings.TrimSpace(a.runningModelRef)
	a.llmMu.RUnlock()
	if client == nil {
		return nil
	}
	if runningRef == "" {
		runningRef = strings.TrimSpace(client.RunningModelRef())
	}
	if selectedRef == "" {
		selectedRef = strings.TrimSpace(client.PrimaryModelRef())
	}
	return provenanceFromClient("chord", client, selectedRef, runningRef)
}

func subAssistantProvenance(s *SubAgent) *message.MessageProvenance {
	if s == nil {
		return nil
	}
	client, _ := s.llmSnapshot()
	if client == nil {
		return nil
	}
	selectedRef := strings.TrimSpace(client.PrimaryModelRef())
	runningRef := strings.TrimSpace(client.RunningModelRef())
	if runningRef == "" {
		runningRef = selectedRef
	}
	return provenanceFromClient("chord", client, selectedRef, runningRef)
}

func toolProvenanceForCall(msgs []message.Message, callID string) *message.MessageProvenance {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if strings.TrimSpace(tc.ID) == callID {
				return cloneProvenance(msg.Provenance)
			}
		}
	}
	return nil
}

func provenanceFromClient(source string, client *llm.Client, selectedRef, runningRef string) *message.MessageProvenance {
	prov := provenanceFromModelRefs(source, selectedRef, runningRef)
	if prov == nil || client == nil {
		return prov
	}
	if providerCfg := client.ProviderConfig(); providerCfg != nil {
		prov.WireFamily = wireFamilyFromProviderType(providerCfg.Type())
	}
	return prov
}

func provenanceFromModelRefs(source, selectedRef, runningRef string) *message.MessageProvenance {
	ref := strings.TrimSpace(runningRef)
	if ref == "" {
		ref = strings.TrimSpace(selectedRef)
	}
	if ref == "" {
		return nil
	}
	providerID, modelID, variant := splitModelRef(ref)
	return &message.MessageProvenance{
		Source:     strings.TrimSpace(source),
		ProviderID: providerID,
		ModelID:    modelID,
		Variant:    variant,
		ModelRef:   ref,
		WireFamily: wireFamilyFromProviderID(providerID),
	}
}

func splitModelRef(ref string) (providerID, modelID, variant string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", ""
	}
	base := ref
	if at := strings.LastIndex(base, "@"); at >= 0 {
		variant = strings.TrimSpace(base[at+1:])
		base = strings.TrimSpace(base[:at])
	}
	if before, after, ok := strings.Cut(base, "/"); ok {
		providerID = strings.TrimSpace(before)
		modelID = strings.TrimSpace(after)
	} else {
		modelID = base
	}
	return providerID, modelID, variant
}

func wireFamilyFromProviderType(providerType string) string {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case config.ProviderTypeMessages:
		return "anthropic"
	case config.ProviderTypeChatCompletions:
		return "openai-chat"
	case config.ProviderTypeResponses:
		return "openai-responses"
	case config.ProviderTypeGenerateContent:
		return "gemini"
	default:
		return "unknown"
	}
}

func wireFamilyFromProviderID(providerID string) string {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	switch {
	case strings.Contains(providerID, "anthropic") || strings.Contains(providerID, "claude"):
		return "anthropic"
	case strings.Contains(providerID, "gemini") || strings.Contains(providerID, "google"):
		return "gemini"
	case strings.Contains(providerID, "openai") || strings.Contains(providerID, "codex"):
		return "openai-responses"
	default:
		return "unknown"
	}
}

func cloneProvenance(in *message.MessageProvenance) *message.MessageProvenance {
	if in == nil {
		return nil
	}
	copy := *in
	return &copy
}
