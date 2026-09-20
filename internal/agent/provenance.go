package agent

import (
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// provenanceSourceID identifies Chord-produced messages in stored provenance.
const provenanceSourceID = "chord"

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
	return provenanceFromClient(provenanceSourceID, client, selectedRef, runningRef)
}

// mainAssistantProvenanceForRunningRef builds provenance for a message produced
// by runningRef, which may differ from the sidebar's current identity after a
// request realigned it to the sticky cursor.
func mainAssistantProvenanceForRunningRef(a *MainAgent, runningRef string) *message.MessageProvenance {
	if a == nil {
		return nil
	}
	runningRef = strings.TrimSpace(runningRef)
	if runningRef == "" {
		return mainAssistantProvenance(a)
	}
	a.llmMu.RLock()
	client := a.llmClient
	selectedRef := strings.TrimSpace(a.providerModelRef)
	a.llmMu.RUnlock()
	return provenanceForRunningRef(client, selectedRef, runningRef)
}

// subAssistantProvenanceForRunningRef is the SubAgent form: an interrupted
// partial reply keeps the worker model that actually wrote it.
func subAssistantProvenanceForRunningRef(s *SubAgent, runningRef string) *message.MessageProvenance {
	if s == nil {
		return nil
	}
	runningRef = strings.TrimSpace(runningRef)
	if runningRef == "" {
		return subAssistantProvenance(s)
	}
	client, _ := s.llmSnapshot()
	selectedRef := ""
	if client != nil {
		selectedRef = strings.TrimSpace(client.PrimaryModelRef())
	}
	return provenanceForRunningRef(client, selectedRef, runningRef)
}

// provenanceForRunningRef resolves the wire and native families for a message
// produced by runningRef. An empty selectedRef falls back to the client's
// primary ref, mirroring provenanceFromModelRefs.
func provenanceForRunningRef(client *llm.Client, selectedRef, runningRef string) *message.MessageProvenance {
	runningRef = strings.TrimSpace(runningRef)
	if client == nil {
		return provenanceFromModelRefs(provenanceSourceID, selectedRef, runningRef)
	}
	if strings.TrimSpace(selectedRef) == "" {
		selectedRef = strings.TrimSpace(client.PrimaryModelRef())
	}
	return provenanceFromClient(provenanceSourceID, client, selectedRef, runningRef)
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
	return provenanceFromClient(provenanceSourceID, client, selectedRef, runningRef)
}

// toolProvenanceFromContext is the Snapshot-free form of
// toolProvenanceForCall: it scans the manager backward under its read lock
// instead of copying the whole history at every tool result.
func toolProvenanceFromContext(mgr *ctxmgr.Manager, callID string) *message.MessageProvenance {
	callID = strings.TrimSpace(callID)
	if callID == "" || mgr == nil {
		return nil
	}
	var out *message.MessageProvenance
	mgr.ScanBackward(func(msg *message.Message) bool {
		if msg.Role != message.RoleAssistant || len(msg.ToolCalls) == 0 {
			return false
		}
		for _, tc := range msg.ToolCalls {
			if strings.TrimSpace(tc.ID) == callID {
				out = cloneProvenance(msg.Provenance)
				return true
			}
		}
		return false
	})
	return out
}

func provenanceFromClient(source string, client *llm.Client, selectedRef, runningRef string) *message.MessageProvenance {
	prov := provenanceFromModelRefs(source, selectedRef, runningRef)
	if prov == nil || client == nil {
		return prov
	}
	ref := strings.TrimSpace(prov.ModelRef)
	if providerCfg := client.ProviderForModelRef(ref); providerCfg != nil {
		prov.WireFamily = wireFamilyFromProviderType(providerCfg.Type())
		prov.NativeFamily = providerCfg.NativeFamily(prov.ModelID)
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
