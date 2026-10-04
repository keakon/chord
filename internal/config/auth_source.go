package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// CredentialDeclarations records which providers explicitly declared a
// credential in auth.yaml, before environment expansion and availability
// filtering. It preserves the declaration intent the normalized AuthConfig
// view intentionally erases: an unset $VAR and a filtered-out empty key still
// count as explicit declarations, so a preset's default credential source must
// not engage for them. No secret value is retained.
type CredentialDeclarations map[string]bool

// Declared reports whether the provider explicitly declared a credential
// source, including declarations that resolved to nothing usable (an unset
// $VAR, a filtered empty key, or an explicitly empty list). Only a provider
// with no declaration at all may fall back to preset default sources.
func (d CredentialDeclarations) Declared(provider string) bool {
	return d[provider]
}

// LoadCredentialDeclarations parses an auth.yaml file and returns the raw
// credential declarations per provider, before environment expansion and
// availability filtering. A missing file yields an empty non-nil result,
// mirroring LoadAuthConfig. It is a static, offline check: no network, OAuth,
// or credential validation runs, and no secret value is read.
func LoadCredentialDeclarations(path string) (CredentialDeclarations, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CredentialDeclarations{}, nil
		}
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	return credentialDeclarations(&root), nil
}

func credentialDeclarations(root *yaml.Node) CredentialDeclarations {
	out := CredentialDeclarations{}
	if len(root.Content) == 0 {
		return out
	}
	for _, e := range mappingEntries(root.Content[0]) {
		if e.Val == nil || e.Val.Kind != yaml.SequenceNode {
			continue
		}
		// `provider: []` is a declaration carrying no credentials; a list with
		// at least one non-null entry declares as well. A list of bare nulls
		// alone carries no declaration intent, matching the normalized view
		// that filters null entries out.
		declared := len(e.Val.Content) == 0
		for _, item := range e.Val.Content {
			resolved := resolveYAMLNode(item)
			if resolved == nil || (resolved.Kind == yaml.ScalarNode && resolved.Tag == "!!null") {
				continue
			}
			if resolved.Kind == yaml.ScalarNode || resolved.Kind == yaml.MappingNode {
				declared = true
				break
			}
		}
		if declared {
			out[e.Key] = true
		}
	}
	return out
}
