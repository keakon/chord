package main

import (
	"os"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
)

// resolveProviderAPIKeys returns the API keys an LLM client for the provider
// is built with. Credentials declared in auth.yaml always win. Only a
// provider that declared no credential source at all falls back to its
// preset's default environment variable; a declaration that exists but
// resolves to nothing usable (an unset $VAR, an explicit empty list or empty
// key) still counts as declared and never engages the fallback.
func resolveProviderAPIKeys(providerName string, providerCfg config.ProviderConfig, auth config.AuthConfig, decls config.CredentialDeclarations) []string {
	apiKeys := config.ExtractAPIKeys(auth[providerName])
	if len(apiKeys) > 0 || decls.Declared(providerName) {
		return apiKeys
	}
	endpoint, ok := modelcatalog.EndpointContract(strings.ToLower(strings.TrimSpace(providerCfg.Preset)))
	if !ok || endpoint.EnvVar == "" {
		return apiKeys
	}
	if value := strings.TrimSpace(os.Getenv(endpoint.EnvVar)); value != "" {
		log.Infof("provider %q declares no credential source; using %s from the %q preset", providerName, endpoint.EnvVar, endpoint.PresetID)
		return append(apiKeys, value)
	}
	return apiKeys
}
