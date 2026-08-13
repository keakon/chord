package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/keakon/chord/internal/config"
)

func applyRequestBodyOverrides(body []byte, overrides config.RequestOverridesConfig) ([]byte, error) {
	if len(overrides.Body) == 0 && len(overrides.RenameBodyFields) == 0 {
		return body, nil
	}

	var patched map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&patched); err != nil {
		return nil, fmt.Errorf("decode request body for overrides: %w", err)
	}
	if err := mergeRequestBodyOverrides(patched, overrides); err != nil {
		return nil, err
	}
	patchedBody, err := json.Marshal(patched)
	if err != nil {
		return nil, fmt.Errorf("encode request body overrides: %w", err)
	}
	return patchedBody, nil
}

func mergeRequestBodyOverrides(patched map[string]any, overrides config.RequestOverridesConfig) error {
	renamed := make(map[string]any, len(overrides.RenameBodyFields))
	for source, target := range overrides.RenameBodyFields {
		value, ok := patched[source]
		if !ok {
			continue
		}
		delete(patched, source)
		if target != nil {
			if _, exists := renamed[*target]; exists {
				return fmt.Errorf("rename request body fields: duplicate target %q", *target)
			}
			renamed[*target] = value
		}
	}
	maps.Copy(patched, renamed)
	mergeRequestBody(patched, overrides.Body)
	return nil
}

func applyRequestHeaderOverrides(header http.Header, overrides config.RequestOverridesConfig) {
	for name, value := range overrides.Headers {
		if value == nil {
			header.Del(name)
		} else {
			header.Set(name, *value)
		}
	}
}

func requestOverridesEmpty(overrides config.RequestOverridesConfig) bool {
	return len(overrides.Body) == 0 && len(overrides.RenameBodyFields) == 0 && len(overrides.Headers) == 0
}

// openAIChatReasoningBodyKeys are the request-override body fields that enable
// reasoning/thinking on OpenAI-compatible chat endpoints. Both the request-
// tuning probe (openAIChatReasoningEnabled) and the disable path
// (withoutReasoningRequestOverrides) must treat the same set of keys.
var openAIChatReasoningBodyKeys = []string{"thinking", "reasoning", "reasoning_effort"}

// requestOverridesEnableReasoning reports whether the given request body
// overrides enable thinking/reasoning server-side. Request-shaping guards use
// this because a compatible gateway can turn on thinking through a body
// override even when the tuning carries no explicit reasoning effort.
func requestOverridesEnableReasoning(overrides config.RequestOverridesConfig) bool {
	for _, key := range openAIChatReasoningBodyKeys {
		if value, ok := overrides.Body[key]; ok && requestOverrideReasoningValueEnabled(key, value) {
			return true
		}
	}
	return false
}

// effectiveRequestReasoningActive applies overrides to the small set of
// generated reasoning fields without copying the full request body.
func effectiveRequestReasoningActive(base map[string]any, overrides config.RequestOverridesConfig) (bool, error) {
	final := make(map[string]any, len(base)+len(overrides.Body))
	reasoningFields := make(map[string]string, len(openAIChatReasoningBodyKeys))
	for key, value := range base {
		final[key] = cloneRequestOverrideValue(value)
		for _, reasoningKey := range openAIChatReasoningBodyKeys {
			if key == reasoningKey {
				reasoningFields[key] = key
				break
			}
		}
	}
	for source, target := range overrides.RenameBodyFields {
		semanticKey, ok := reasoningFields[source]
		if !ok {
			continue
		}
		delete(reasoningFields, source)
		if target != nil {
			reasoningFields[*target] = semanticKey
		}
	}
	if err := mergeRequestBodyOverrides(final, overrides); err != nil {
		return false, fmt.Errorf("apply reasoning request probe overrides: %w", err)
	}
	if requestOverridesEnableReasoning(config.RequestOverridesConfig{Body: final}) {
		return true, nil
	}
	for wireKey, semanticKey := range reasoningFields {
		if value, ok := final[wireKey]; ok && requestOverrideReasoningValueEnabled(semanticKey, value) {
			return true, nil
		}
	}
	return false, nil
}

func openAIReasoningEffortActive(effort string) bool {
	return reasoningOverrideStringEnabled(effort)
}

func requestOverrideReasoningValueEnabled(key string, value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return reasoningOverrideStringEnabled(v)
	case map[string]any:
		return requestOverrideReasoningMapEnabled(key, v)
	case map[string]string:
		converted := make(map[string]any, len(v))
		for field, fieldValue := range v {
			converted[field] = fieldValue
		}
		return requestOverrideReasoningMapEnabled(key, converted)
	default:
		return true
	}
}

func requestOverrideReasoningMapEnabled(key string, value map[string]any) bool {
	if len(value) == 0 {
		return false
	}
	switch key {
	case "thinking":
		if thinkingType, ok := value["type"]; ok {
			return requestOverrideReasoningValueEnabled("thinking.type", thinkingType)
		}
		if enabled, ok := value["enabled"]; ok {
			return requestOverrideReasoningValueEnabled("thinking.enabled", enabled)
		}
		for _, field := range []string{"effort", "mode"} {
			if fieldValue, ok := value[field]; ok {
				return requestOverrideReasoningValueEnabled(field, fieldValue)
			}
		}
		return true
	case "reasoning":
		for _, field := range []string{"enabled", "effort", "mode", "type"} {
			if fieldValue, ok := value[field]; ok {
				return requestOverrideReasoningValueEnabled(field, fieldValue)
			}
		}
		if summary, ok := value["summary"]; ok {
			return requestOverrideReasoningValueEnabled("summary", summary)
		}
		return true
	default:
		return true
	}
}

func reasoningOverrideStringEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "disabled", "disable", "off", "false":
		return false
	default:
		return true
	}
}

func withoutReasoningRequestOverrides(overrides config.RequestOverridesConfig) config.RequestOverridesConfig {
	overrides.Body = maps.Clone(overrides.Body)
	for _, key := range openAIChatReasoningBodyKeys {
		delete(overrides.Body, key)
	}
	overrides.RenameBodyFields = maps.Clone(overrides.RenameBodyFields)
	for source, target := range overrides.RenameBodyFields {
		if source == "reasoning" || source == "reasoning_effort" ||
			target != nil && (*target == "thinking" || *target == "reasoning" || *target == "reasoning_effort") {
			delete(overrides.RenameBodyFields, source)
		}
	}
	return overrides
}

func mergeRequestBody(target, patch map[string]any) {
	for key, value := range patch {
		if value == nil {
			delete(target, key)
			continue
		}
		patchNested, patchOK := value.(map[string]any)
		if !patchOK {
			target[key] = value
			continue
		}
		targetNested, targetOK := target[key].(map[string]any)
		if !targetOK {
			targetNested = make(map[string]any)
			target[key] = targetNested
		}
		mergeRequestBody(targetNested, patchNested)
	}
}
