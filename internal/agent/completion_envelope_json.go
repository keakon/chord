package agent

import "encoding/json"

func marshalCompletionEnvelope(env *CompletionEnvelope) json.RawMessage {
	env = normalizeCompletionEnvelope(env)
	if env == nil {
		return nil
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil
	}
	return data
}

func unmarshalCompletionEnvelope(data json.RawMessage) *CompletionEnvelope {
	if len(data) == 0 {
		return nil
	}
	var env CompletionEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil
	}
	return normalizeCompletionEnvelope(&env)
}
