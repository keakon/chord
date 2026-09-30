package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

const (
	// webSearchMaxDomains caps each domain filter list. 100 is OpenAI's
	// documented filters limit; Anthropic does not document one, so the shared
	// contract takes the stricter value.
	webSearchMaxDomains = 100
	// webSearchMaxUses caps server-side searches inside one sub-request.
	webSearchMaxUses = 8
)

// BuiltinHostedToolSpecs returns the built-in hosted tool catalog. Every call
// returns independent maps so catalog overrides never mutate another
// resolution.
func BuiltinHostedToolSpecs() map[string]HostedToolSpec {
	return map[string]HostedToolSpec{
		NameWebSearch: webSearchHostedSpec(),
	}
}

// webSearchHostedSpec declares the built-in web_search entry: the declaration
// versions below are default wire contracts that configuration may override.
// Known traps for
// future upgrades: Anthropic web_search_20260209 defaults the callers to
// code_execution and needs allowed_callers:["direct"] for non-programmatic
// models, and newer versions move options inside the declaration object.
func webSearchHostedSpec() HostedToolSpec {
	return HostedToolSpec{
		Name: NameWebSearch,
		Description: `Search the public web and return a summary with numbered sources (URL and title).

Use this when the answer depends on current information beyond your knowledge, and cite the numbered sources in your reply. The search runs server-side on the configured provider; results are a summary, not the full page, so use web_fetch when you need a specific page's content.

Domain filters are optional and mutually exclusive: allowed_domains restricts results to the listed domains, blocked_domains excludes them. Each list accepts up to 100 bare domains (e.g. "example.com"), without scheme or path.`,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Natural-language search query.",
				},
				"allowed_domains": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Restrict results to these bare domains. Mutually exclusive with blocked_domains.",
				},
				"blocked_domains": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Exclude results from these bare domains. Mutually exclusive with allowed_domains.",
				},
			},
			"required": []string{"query"},
		},
		Prompt:          "Perform a web search for the query: {query}",
		ReadOnly:        true,
		ConcurrencySafe: true,
		RetrySafe:       true,
		Declarations: map[string]config.HostedToolDeclarationConfig{
			config.ProviderTypeMessages: {
				Tool: map[string]any{
					"type":     "web_search_20250305",
					"name":     NameWebSearch,
					"max_uses": webSearchMaxUses,
					"allowed_domains": map[string]any{
						"$arg": "allowed_domains",
					},
					"blocked_domains": map[string]any{
						"$arg": "blocked_domains",
					},
				},
				Force: map[string]any{
					"type": "tool",
					"name": NameWebSearch,
				},
			},
			config.ProviderTypeResponses: {
				Tool: map[string]any{
					"type": NameWebSearch,
					// Responses supports both allowlists and blocklists:
					// https://developers.openai.com/api/docs/guides/tools-web-search
					"filters": map[string]any{
						"allowed_domains": map[string]any{
							"$arg": "allowed_domains",
						},
						"blocked_domains": map[string]any{
							"$arg": "blocked_domains",
						},
					},
				},
				Force:   "required",
				Include: []string{"web_search_call.action.sources"},
			},
		},
		Validate: validateWebSearchArgs,
		Format:   formatWebSearchObservation,
	}
}

// validateWebSearchArgs checks the built-in web_search arguments and
// normalizes them in place (trimmed query, trimmed bare domains) so the wire
// declaration and the prompt see the same canonical values.
func validateWebSearchArgs(args map[string]any) error {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return fmt.Errorf("query is required")
	}
	args["query"] = query
	allowed, err := normalizeWebSearchDomains("allowed_domains", args["allowed_domains"])
	if err != nil {
		return err
	}
	blocked, err := normalizeWebSearchDomains("blocked_domains", args["blocked_domains"])
	if err != nil {
		return err
	}
	if len(allowed) > 0 && len(blocked) > 0 {
		return fmt.Errorf("allowed_domains and blocked_domains are mutually exclusive")
	}
	setHostedArgsList(args, "allowed_domains", allowed)
	setHostedArgsList(args, "blocked_domains", blocked)
	return nil
}

func setHostedArgsList(args map[string]any, key string, values []string) {
	if len(values) == 0 {
		delete(args, key)
		return
	}
	args[key] = values
}

func normalizeWebSearchDomains(field string, raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	values, err := hostedArgList(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	if len(values) > webSearchMaxDomains {
		return nil, fmt.Errorf("%s accepts at most %d domains", field, webSearchMaxDomains)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		domain, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s accepts only strings", field)
		}
		domain = strings.TrimSpace(domain)
		if err := validateWebSearchDomain(domain); err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		out = append(out, domain)
	}
	return out, nil
}

func hostedArgList(raw any) ([]any, error) {
	switch value := raw.(type) {
	case []any:
		return value, nil
	case []string:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = item
		}
		return out, nil
	default:
		return nil, fmt.Errorf("must be an array of strings")
	}
}

func validateWebSearchDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("domain must not be empty")
	}
	if strings.Contains(domain, "://") || strings.ContainsAny(domain, "/?#@: ") {
		return fmt.Errorf("invalid domain %q: provide a bare domain without scheme or path", domain)
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return fmt.Errorf("invalid domain %q: empty domain label", domain)
	}
	for _, r := range domain {
		if r == '-' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127 {
			continue
		}
		return fmt.Errorf("invalid domain %q: only letters, digits, hyphens and dots are allowed", domain)
	}
	if !strings.Contains(domain, ".") {
		return fmt.Errorf("invalid domain %q: provide a domain such as \"example.com\"", domain)
	}
	return nil
}

// formatWebSearchObservation renders the built-in web_search result: the
// sub-request model's summary, stable numbered sources, then per-call errors.
// The backend only returns successful observations, so a source-less result
// means the search ran and found nothing.
func formatWebSearchObservation(obs *message.HostedObservation) string {
	var b strings.Builder
	if obs == nil {
		return "(no search results returned)"
	}
	if summary := strings.TrimSpace(obs.Summary); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	sources := webSearchObservationSources(obs)
	if len(sources) == 0 {
		b.WriteString("(no search results returned)\n")
	} else {
		b.WriteString("Sources:\n")
		for i, src := range sources {
			title := strings.TrimSpace(src.Title)
			if title == "" {
				title = src.URL
			}
			fmt.Fprintf(&b, "[%d] %s\n    %s\n", i+1, title, src.URL)
		}
	}
	b.WriteString(formatWebSearchCitationMap(obs, sources))
	if errs := obs.CallErrors(); len(errs) > 0 {
		fmt.Fprintf(&b, "Search errors: %s\n", strings.Join(errs, "; "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// hostedSearchSource is one source entry recovered from a raw result payload.
type hostedSearchSource struct {
	URL   string
	Title string
}

// webSearchObservationSources recovers sources from the raw result payloads:
// Anthropic returns an array of {url,title} entries, Responses returns an
// object whose action.sources carries them. URLs are deduplicated in call
// order.
func webSearchObservationSources(obs *message.HostedObservation) []hostedSearchSource {
	if obs == nil {
		return nil
	}
	var out []hostedSearchSource
	seen := make(map[string]struct{})
	add := func(url, title string) {
		url = strings.TrimSpace(url)
		if url == "" {
			return
		}
		if _, ok := seen[url]; ok {
			return
		}
		seen[url] = struct{}{}
		out = append(out, hostedSearchSource{URL: url, Title: strings.TrimSpace(title)})
	}
	for i := range obs.Calls {
		raw := bytes.TrimSpace(obs.Calls[i].Result)
		if len(raw) == 0 {
			continue
		}
		switch raw[0] {
		case '[':
			var entries []struct {
				URL   string `json:"url"`
				Title string `json:"title"`
			}
			if err := json.Unmarshal(raw, &entries); err != nil {
				continue
			}
			for _, entry := range entries {
				add(entry.URL, entry.Title)
			}
		case '{':
			var entry struct {
				Action struct {
					Sources []struct {
						URL   string `json:"url"`
						Title string `json:"title"`
					} `json:"sources"`
				} `json:"action"`
			}
			if err := json.Unmarshal(raw, &entry); err != nil {
				continue
			}
			for _, src := range entry.Action.Sources {
				add(src.URL, src.Title)
			}
		}
	}
	for _, citation := range webSearchObservationCitations(obs) {
		add(citation.URL, citation.Title)
	}
	return out
}
