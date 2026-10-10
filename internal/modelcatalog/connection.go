package modelcatalog

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Connection is a documented API recipe, not a verified binding. It supplies
// a URL and credential variable only after the user explicitly selects this
// catalog model. It does not supply route-specific variants or send rules.
type Connection struct {
	RequestURL  string   `json:"request_url" yaml:"request_url"`
	WireModelID string   `json:"wire_model_id" yaml:"wire_model_id"`
	EnvVar      string   `json:"env_var" yaml:"env_var"`
	Sources     []Source `json:"sources" yaml:"-"`
}

func validateModelMetadata(m ModelFacts) error {
	if m.Released != "" {
		if _, err := time.Parse(time.DateOnly, m.Released); err != nil {
			return fmt.Errorf("model %q: released must be a calendar day", m.ID)
		}
	}
	for _, s := range m.CodingSources {
		if err := validateMetadataSource(s); err != nil {
			return fmt.Errorf("model %q coding evidence: %w", m.ID, err)
		}
	}
	if m.Cost != nil {
		if err := validateMetadataSource(m.Cost.Source); err != nil {
			return fmt.Errorf("model %q cost evidence: %w", m.ID, err)
		}
		if _, err := time.Parse(time.DateOnly, m.Cost.Checked); err != nil {
			return fmt.Errorf("model %q: cost checked must be a calendar day", m.ID)
		}
	}
	if m.Connection == nil {
		return nil
	}
	c := m.Connection
	u, err := url.Parse(c.RequestURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("model %q: connection requires an https API URL without credentials, query or fragment", m.ID)
	}
	if !strings.HasSuffix(u.Path, "/chat/completions") && !strings.HasSuffix(u.Path, "/responses") && !strings.HasSuffix(u.Path, "/messages") && !isGenerateContentRoot(u) {
		return fmt.Errorf("model %q: connection URL has no supported protocol suffix", m.ID)
	}
	if strings.TrimSpace(c.WireModelID) == "" || strings.ContainsAny(c.WireModelID, "@ \t\r\n") || !validEnvVar(c.EnvVar) || len(c.Sources) == 0 {
		return fmt.Errorf("model %q: connection requires a wire model ID, credential variable and sources", m.ID)
	}
	for _, s := range c.Sources {
		if err := validateMetadataSource(s); err != nil {
			return fmt.Errorf("model %q connection: %w", m.ID, err)
		}
	}
	return nil
}

func isGenerateContentRoot(u *url.URL) bool {
	path := strings.TrimRight(u.Path, "/")
	return strings.HasSuffix(path, "/v1beta") || strings.HasSuffix(path, "/v1alpha") || strings.EqualFold(u.Hostname(), "generativelanguage.googleapis.com") && path == "/v1"
}

func validateMetadataSource(s Source) error {
	if err := validateProvenanceURL(s.URL); err != nil {
		return err
	}
	if _, err := time.Parse(time.DateOnly, s.Checked); err != nil {
		return fmt.Errorf("source checked must be a calendar day")
	}
	return nil
}

func validateProvenanceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("source must be an https URL with a host")
	}
	return nil
}

func validEnvVar(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
