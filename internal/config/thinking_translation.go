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
//	  max_chars: 1000
//
// model_pool must refer to a top-level model_pools entry. max_chars limits the
// source thinking preview sent for translation; values <= 0 use the built-in
// default.
type ThinkingTranslationConfig struct {
	TargetLanguage string `json:"target_language,omitempty" yaml:"target_language,omitempty"`
	ModelPool      string `json:"model_pool,omitempty" yaml:"model_pool,omitempty"`
	MaxChars       int    `json:"max_chars,omitempty" yaml:"max_chars,omitempty"`
}
