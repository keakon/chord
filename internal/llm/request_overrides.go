package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"

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
	renamed := make(map[string]any, len(overrides.RenameBodyFields))
	for source, target := range overrides.RenameBodyFields {
		value, ok := patched[source]
		if !ok {
			continue
		}
		delete(patched, source)
		if target != nil {
			if _, exists := renamed[*target]; exists {
				return nil, fmt.Errorf("rename request body fields: duplicate target %q", *target)
			}
			renamed[*target] = value
		}
	}
	maps.Copy(patched, renamed)
	mergeRequestBody(patched, overrides.Body)
	patchedBody, err := json.Marshal(patched)
	if err != nil {
		return nil, fmt.Errorf("encode request body overrides: %w", err)
	}
	return patchedBody, nil
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
