package config

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// CredentialSourceKind classifies how one credential was declared in auth.yaml.
type CredentialSourceKind string

const (
	// CredentialSourceEnv is a $VAR reference. EnvVar holds the first
	// referenced variable name as written (without the leading $).
	CredentialSourceEnv CredentialSourceKind = "env"
	// CredentialSourceLiteral is a literal non-empty API key string.
	CredentialSourceLiteral CredentialSourceKind = "literal"
	// CredentialSourceExplicitEmpty is a literal empty string: the user
	// explicitly declared an empty credential.
	CredentialSourceExplicitEmpty CredentialSourceKind = "explicit_empty"
	// CredentialSourceOAuth is a mapping form (API token or OAuth credential).
	CredentialSourceOAuth CredentialSourceKind = "oauth"
)

// CredentialSource records one declared credential before environment
// expansion and availability filtering. It preserves the declaration intent
// the normalized AuthConfig view intentionally erases: an unset $VAR and a
// filtered-out empty key still count as explicit declarations, so a preset's
// default credential source must not engage for them. It never records the
// secret itself, only where and how the credential was declared.
type CredentialSource struct {
	Kind CredentialSourceKind
	// EnvVar is the first variable name referenced by an env source
	// ("CUSTOM_KEY"), empty for the other kinds.
	EnvVar string
	// File and Line locate the declaration.
	File string
	Line int
}

// ProviderCredentialDeclarations is one provider's raw credential declaration
// as written in auth.yaml, before expansion and filtering.
type ProviderCredentialDeclarations struct {
	// Sources lists the declared credentials in file order.
	Sources []CredentialSource
	// DeclaredEmptyList marks `provider: []`: an explicit declaration carrying
	// no credentials, which must not fall back to preset default sources.
	DeclaredEmptyList bool
}

// CredentialDeclarations maps provider name to its raw credential
// declarations.
type CredentialDeclarations map[string]ProviderCredentialDeclarations

// Declared reports whether the provider explicitly declared a credential
// source, including declarations that resolved to nothing usable (an unset
// $VAR, a filtered empty key, or an explicitly empty list). Only a provider
// with no declaration at all may fall back to preset default sources.
func (d CredentialDeclarations) Declared(provider string) bool {
	decl, ok := d[provider]
	return ok && (decl.DeclaredEmptyList || len(decl.Sources) > 0)
}

// LoadCredentialDeclarations parses an auth.yaml file and returns the raw
// credential declarations per provider, before environment expansion and
// availability filtering. A missing file yields an empty non-nil result,
// mirroring LoadAuthConfig. It is a static, offline check: no network, OAuth,
// or credential validation runs, and no secret value is retained.
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
	out := CredentialDeclarations{}
	if len(root.Content) == 0 {
		return out, nil
	}
	for _, e := range mappingEntries(root.Content[0]) {
		if e.Val == nil || e.Val.Kind != yaml.SequenceNode {
			continue
		}
		var decl ProviderCredentialDeclarations
		decl.DeclaredEmptyList = len(e.Val.Content) == 0
		for _, item := range e.Val.Content {
			resolved := resolveYAMLNode(item)
			if resolved == nil {
				continue
			}
			switch resolved.Kind {
			case yaml.ScalarNode:
				if resolved.Tag == "!!null" {
					// A null entry carries no declaration intent; the
					// normalized view filters it out as well.
					continue
				}
				if strings.HasPrefix(resolved.Value, "$") {
					decl.Sources = append(decl.Sources, CredentialSource{
						Kind:   CredentialSourceEnv,
						EnvVar: firstEnvVarName(resolved.Value),
						File:   path,
						Line:   item.Line,
					})
					continue
				}
				kind := CredentialSourceLiteral
				if resolved.Value == "" {
					kind = CredentialSourceExplicitEmpty
				}
				decl.Sources = append(decl.Sources, CredentialSource{Kind: kind, File: path, Line: item.Line})
			case yaml.MappingNode:
				decl.Sources = append(decl.Sources, CredentialSource{Kind: CredentialSourceOAuth, File: path, Line: item.Line})
			}
		}
		out[e.Key] = decl
	}
	return out, nil
}

// firstEnvVarName extracts the first variable name a credential string
// references, matching what os.ExpandEnv would expand: $NAME or ${NAME}.
func firstEnvVarName(raw string) string {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '$' {
			continue
		}
		if i+1 < len(raw) && raw[i+1] == '{' {
			if end := strings.IndexByte(raw[i+2:], '}'); end >= 0 {
				return raw[i+2 : i+2+end]
			}
			return ""
		}
		j := i + 1
		for j < len(raw) && isEnvNameChar(raw[j]) {
			j++
		}
		if j > i+1 {
			return raw[i+1 : j]
		}
	}
	return ""
}

func isEnvNameChar(c byte) bool {
	return c == '_' ||
		(c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z')
}
