package config

import "fmt"

const (
	ConfirmAskPolicyWait = "wait"
	ConfirmAskPolicyDeny = "deny"
)

func validConfirmAskPolicy(value string) bool {
	return value == "" || value == ConfirmAskPolicyWait || value == ConfirmAskPolicyDeny
}

func collectConfirmConfigIssues(cfg *Config) []string {
	if cfg.ConfirmAskPolicy == nil || validConfirmAskPolicy(*cfg.ConfirmAskPolicy) {
		return nil
	}
	value := *cfg.ConfirmAskPolicy
	cfg.ConfirmAskPolicy = nil
	return []string{fmt.Sprintf("confirm_ask_policy must be wait or deny; got %q, using inherited/default value", value)}
}

// EffectiveConfirmAskPolicy keeps nil distinct from an explicit empty value
// during config merging; both select wait when no higher layer overrides them.
func (cfg *Config) EffectiveConfirmAskPolicy() string {
	if cfg != nil && cfg.ConfirmAskPolicy != nil && *cfg.ConfirmAskPolicy == ConfirmAskPolicyDeny {
		return ConfirmAskPolicyDeny
	}
	return ConfirmAskPolicyWait
}
