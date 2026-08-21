package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Candidate is one independent memory extracted from a session. Multiple
// candidates are returned per run; each is validated independently. Empty
// candidate lists are a legal no-op.
type Candidate struct {
	Type         Type       `json:"type"`
	Statement    string     `json:"statement"`
	Rationale    string     `json:"rationale"`
	Application  string     `json:"application"`
	Summary      string     `json:"summary"`
	SourceRole   string     `json:"source_role"`
	Confidence   Confidence `json:"confidence"`
	Outcome      Outcome    `json:"outcome"`
	ProjectPaths []string   `json:"project_paths,omitempty"`
	Supersedes   []string   `json:"supersedes,omitempty"`
}

// SourceRole values.
const (
	SourceRoleUser      = "user"
	SourceRoleAssistant = "assistant"
)

// ErrInvalidExtraction marks structured extraction output that cannot be
// parsed as a valid candidate set (bad JSON, missing field, unknown enum).
// It is a failure, never a no-op.
var ErrInvalidExtraction = errors.New("invalid memory extraction output")

var (
	pemBlockRe      = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+ PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]+ PRIVATE KEY-----`)
	urlCredentialRe = regexp.MustCompile(`\b((?:https?|ftp|ssh)://)[^\s/@]+:[^\s/@]+@`)
	authBearerRe    = regexp.MustCompile(`(?i)\b((?:authorization|proxy-authorization)\s*:\s*Bearer\s+)[A-Za-z0-9._~+/=-]+`)
	apiKeyHeaderRe  = regexp.MustCompile(`(?i)\b((?:x-api-key|x-auth-token)\s*:\s*)[A-Za-z0-9._~+/=-]+`)
	keyAssignRe     = regexp.MustCompile(`(?i)\b((?:api[_-]?key|secret|password|passwd|token|client[_-]?secret|access[_-]?token|refresh[_-]?token|private[_-]?key|session[_-]?token)\b\s*[=:]\s*['"]?)[A-Za-z0-9._~+/=-]{8,}`)
	envHighRiskRe   = regexp.MustCompile(`(?i)\b((?:export\s+)?(?:AWS_(?:SECRET_ACCESS_KEY|SESSION_TOKEN)|AZURE_(?:OPENAI|SUBSCRIPTION)_KEY|OPENAI_API_KEY|ANTHROPIC_API_KEY|GITHUB_TOKEN|GITLAB_TOKEN|GOOGLE_API_KEY|HF_TOKEN|HUGGING_FACE_HUB_TOKEN|SLACK_TOKEN|DISCORD_TOKEN|GCP_SERVICE_ACCOUNT)\b\s*=\s*['"]?)[A-Za-z0-9._~+/=-]+`)
	// bareSecretTokenRe catches well-known bare secret token shapes (OpenAI sk-,
	// GitHub ghp_/github_pat_, GitLab glpat-, Slack xox*, AWS AKIA) that are not
	// prefixed by an assignment keyword. The two-letter prefixes require their
	// separator so ordinary identifiers (skipLockedSessions, pkg_resources) are
	// not mistaken for keys and redacted or dropped.
	bareSecretTokenRe = regexp.MustCompile(`\b(?:(?:sk|pk|rk)[-_]|gh[pousr]_|glpat-|xox[baprs]-|AKIA)[A-Za-z0-9_-]{10,}`)
)

// SanitizeText redacts known high-risk secret shapes from text. It is the
// pre-model and pre-write cleanup layer. Each replacement keeps the matched
// prefix (scheme, header name, or assignment) and redacts only the credential
// so the surrounding text stays readable and greppable.
func SanitizeText(text string) string {
	out := pemBlockRe.ReplaceAllString(text, "[REDACTED PEM PRIVATE KEY]")
	out = urlCredentialRe.ReplaceAllString(out, "$1[REDACTED]@")
	out = authBearerRe.ReplaceAllString(out, "$1[REDACTED]")
	out = apiKeyHeaderRe.ReplaceAllString(out, "$1[REDACTED]")
	out = keyAssignRe.ReplaceAllString(out, "$1[REDACTED]")
	out = envHighRiskRe.ReplaceAllString(out, "$1[REDACTED]")
	out = bareSecretTokenRe.ReplaceAllString(out, "[REDACTED]")
	return out
}

// HighRisk reports whether text still carries a high-risk secret shape after
// sanitization. Messages/candidates that still match are dropped entirely.
func HighRisk(text string) bool {
	return pemBlockRe.MatchString(text) ||
		urlCredentialRe.MatchString(text) ||
		envHighRiskRe.MatchString(text) ||
		keyAssignRe.MatchString(text) ||
		bareSecretTokenRe.MatchString(text)
}

// extractionEnvelope is the wire shape of a structured extraction response.
type extractionEnvelope struct {
	Candidates []Candidate `json:"candidates"`
}

// ParseExtractionOutput parses and validates a structured extraction response.
//
//   - candidates: [] is a legal no-op.
//   - malformed JSON, a missing/malformed candidates field, or an unknown enum
//     value is a failure (ErrInvalidExtraction), never a no-op.
//   - per-candidate validation (lengths, paths, cognitive state consistency,
//     secrets) drops only the offending candidate; the surviving candidates are
//     still returned with their drop reasons so callers can commit the rest.
//
// The returned dropped slice lists per-candidate drop reasons (non-nil result
// with a non-empty dropped slice is expected for partially valid output); a
// non-nil err alongside a non-nil result means the whole output was invalid.
func ParseExtractionOutput(data []byte) ([]Candidate, []string, error) {
	var env extractionEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidExtraction, err)
	}
	if env.Candidates == nil {
		return nil, nil, fmt.Errorf("%w: missing candidates field", ErrInvalidExtraction)
	}
	var out []Candidate
	var dropReasons []string
	for i, c := range env.Candidates {
		// Schema-level problems (unknown enum, role mismatch) invalidate the
		// whole output; content-level problems drop only the offending
		// candidate so the survivors can still be committed.
		if err := validateCandidateStructural(c); err != nil {
			return nil, nil, fmt.Errorf("%w: candidate %d: %v", ErrInvalidExtraction, i, err)
		}
		if err := validateCandidateDroppable(c); err != nil {
			dropReasons = append(dropReasons, fmt.Sprintf("candidate %d dropped: %v", i, err))
			continue
		}
		// Second-layer secret cleaning: a candidate whose raw text still carries
		// a high-risk secret shape is dropped entirely (a sanitized quote still
		// leaks that a secret exists and where); survivors are re-sanitized
		// before anything is written to disk.
		if HighRisk(c.Statement) || HighRisk(c.Rationale) || HighRisk(c.Application) || HighRisk(c.Summary) || HighRisk(strings.Join(c.ProjectPaths, " ")) {
			dropReasons = append(dropReasons, fmt.Sprintf("candidate %d dropped: high-risk secret pattern", i))
			continue
		}
		c.Statement = SanitizeText(c.Statement)
		c.Rationale = SanitizeText(c.Rationale)
		c.Application = SanitizeText(c.Application)
		c.Summary = SanitizeText(c.Summary)
		out = append(out, c)
	}
	return out, dropReasons, nil
}

// summaryUsable reports whether a summary is safe to embed as a single managed
// index line: no line breaks and no managed markers that could close or nest
// the Chord-managed section.
func summaryUsable(s string) bool {
	return !strings.ContainsAny(s, "\r\n") &&
		!strings.Contains(s, managedStartMarker) &&
		!strings.Contains(s, managedEndMarker)
}

// pathUsable reports whether a project path is safe to embed in a record's
// Markdown backtick list: no line breaks, control characters, or backticks.
func pathUsable(p string) bool {
	for _, r := range p {
		if r == '`' || r == '\r' || r == '\n' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// validateCandidateStructural applies the schema-level checks shared by the
// whole extraction run. A violation is a run-level failure (unknown enums,
// source role mismatches) never a per-candidate drop.
func validateCandidateStructural(c Candidate) error {
	if !validTypes[c.Type] {
		return fmt.Errorf("unknown type %q", c.Type)
	}
	if !validConfidences[c.Confidence] {
		return fmt.Errorf("unknown confidence %q", c.Confidence)
	}
	if !validOutcomes[c.Outcome] {
		return fmt.Errorf("unknown outcome %q", c.Outcome)
	}
	if c.SourceRole != SourceRoleUser && c.SourceRole != SourceRoleAssistant {
		return fmt.Errorf("unknown source_role %q", c.SourceRole)
	}
	if c.Confidence == ConfidenceUserStated && c.SourceRole != SourceRoleUser {
		return fmt.Errorf("confidence user_stated requires source_role user")
	}
	return nil
}

// validateCandidateDroppable applies the per-candidate content checks: field
// presence and length bounds, path/ID shape, and characters that would break
// the managed index or record Markdown. A violation drops only the offending
// candidate; the surviving candidates are still committed.
func validateCandidateDroppable(c Candidate) error {
	if strings.TrimSpace(c.Statement) == "" {
		return errors.New("empty statement")
	}
	if len(c.Statement) > maxStatementLen {
		return fmt.Errorf("statement too long (%d > %d)", len(c.Statement), maxStatementLen)
	}
	if strings.TrimSpace(c.Rationale) == "" {
		return errors.New("empty rationale")
	}
	if len(c.Rationale) > maxRationaleLen {
		return fmt.Errorf("rationale too long (%d > %d)", len(c.Rationale), maxRationaleLen)
	}
	if strings.TrimSpace(c.Application) == "" {
		return errors.New("empty application")
	}
	if len(c.Application) > maxApplicationLen {
		return fmt.Errorf("application too long (%d > %d)", len(c.Application), maxApplicationLen)
	}
	if strings.TrimSpace(c.Summary) == "" {
		return errors.New("empty summary")
	}
	if len(c.Summary) > maxSummaryLen {
		return fmt.Errorf("summary too long (%d > %d)", len(c.Summary), maxSummaryLen)
	}
	if !summaryUsable(c.Summary) {
		return errors.New("summary must be a single line without managed markers")
	}
	if len(c.ProjectPaths) > maxProjectPaths {
		return fmt.Errorf("too many project paths (%d > %d)", len(c.ProjectPaths), maxProjectPaths)
	}
	if len(c.Supersedes) > maxSupersedes {
		return fmt.Errorf("too many supersedes (%d > %d)", len(c.Supersedes), maxSupersedes)
	}
	for _, p := range c.ProjectPaths {
		if err := validateProjectPathString(p); err != nil {
			return err
		}
		if !pathUsable(p) {
			return fmt.Errorf("project path %q contains forbidden characters", p)
		}
	}
	for _, s := range c.Supersedes {
		if !ValidateRecordID(s) {
			return fmt.Errorf("invalid supersedes record id %q", s)
		}
	}
	return nil
}
