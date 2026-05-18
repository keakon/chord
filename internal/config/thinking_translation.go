package config

// ThinkingTranslationConfig controls post-processing translation for thinking
// blocks (e.g. GPT/Anthropic reasoning/thinking content). This is a best-effort
// UX enhancement and must never affect the main answer flow.
//
// YAML:
//
//	thinking_translation:
//	  target_language: zh-Hans
//	  model_pool: translation
//
// model_pool must refer to a top-level model_pools entry.
type ThinkingTranslationConfig struct {
	TargetLanguage string `json:"target_language,omitempty" yaml:"target_language,omitempty"`
	ModelPool      string `json:"model_pool,omitempty" yaml:"model_pool,omitempty"`
}
