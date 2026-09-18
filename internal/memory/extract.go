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

// Per-run allowances for retirements. A session-scoped extraction only sees one
// transcript, so it may only correct a few records; a whole-index review is
// expected to consolidate harder.
const (
	MaxRetirePerSessionRun = 3
	MaxRetirePerReviewRun  = 8
)

// RetireRequest asks for an active record to leave the managed index with no
// replacement conclusion.
//
// This is the channel for correcting memory that should never have been written.
// Supersedes can only trade an old record for a new one, so a conclusion that is
// simply wrong, out of scope, or already covered by project instructions had no
// way out of the index — including anything a weaker model wrote earlier.
type RetireRequest struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// PromotionTarget names the kind of home a conclusion belongs in instead of
// memory. It is deliberately semantic rather than a path: memory runs against
// arbitrary projects, so nothing here may assume a directory convention.
type PromotionTarget string

const (
	// PromotionProjectInstructions is the project's mandatory guidance file, for
	// rules that must always apply rather than be recalled as background.
	PromotionProjectInstructions PromotionTarget = "project_instructions"
	// PromotionProjectDocs is the project's own documentation, for material that
	// stays useful but triggers rarely and should not spend reminder budget every
	// turn.
	PromotionProjectDocs PromotionTarget = "project_docs"
)

var validPromotionTargets = map[PromotionTarget]bool{
	PromotionProjectInstructions: true,
	PromotionProjectDocs:         true,
}

// Promotion proposes moving a conclusion out of memory into a more durable and
// more authoritative home.
//
// Chord never writes those files itself. Turning assistant-reported material
// into mandatory project guidance is an authority upgrade, so the suggestion is
// appended to a pending review file and the model's draft stays a draft.
type Promotion struct {
	// SourceID is the active record this replaces, when the promotion comes from
	// reviewing existing memory rather than from a fresh conclusion.
	SourceID string          `json:"source_id,omitempty"`
	Target   PromotionTarget `json:"target"`
	// Summary is a one-line heading for the pending review file.
	Summary string `json:"summary"`
	// SuggestedLocation is a free-form hint (section name, doc path) the model
	// may infer from repository instructions. Empty is normal and fine.
	SuggestedLocation string `json:"suggested_location,omitempty"`
	DraftText         string `json:"draft_text"`
	Reason            string `json:"reason"`
}

// ExtractionOutput is the parsed, validated result of one extraction run.
type ExtractionOutput struct {
	Candidates []Candidate
	Retire     []RetireRequest
	Promotions []Promotion
	// Dropped lists per-item drop reasons. A non-empty Dropped alongside a
	// usable result is expected for partially valid model output.
	Dropped []string
}

// Empty reports whether the run produced nothing to commit.
func (o *ExtractionOutput) Empty() bool {
	return o == nil || (len(o.Candidates) == 0 && len(o.Retire) == 0 && len(o.Promotions) == 0)
}

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
	// sessionSHALikeRe catches session-local commit SHAs in durable text. A match
	// counts only when it mixes hex letters and digits (see
	// containsSessionSHALike): pure-letter words (defaced, effaced) and
	// pure-digit tokens (issue numbers, timeouts, YYYYMMDD dates) are never
	// SHAs. The residual miss is an all-digit short SHA (~3.7% of 7-char
	// SHAs); the avoided false positives are far more common in practice.
	sessionSHALikeRe = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	// absolutePathRe catches machine-local absolute paths in durable text, on a
	// best-effort prefix list plus Windows drive prefixes. Project-relative
	// paths never start with these prefixes, and project_paths entries already
	// reject absolute paths lexically; this covers the same shapes inside free
	// text. Home-relative (~/...) references are deliberately not matched: they
	// stay valid when the project moves between machines for the same user.
	absolutePathRe = regexp.MustCompile(`/(Users|home|tmp|var|private|etc|opt|data|root|mnt|srv|usr|proc|sys|dev|run|System|Library|Volumes|Applications)/\S*|[A-Za-z]:[\\/][^\s]*`)
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
	Candidates []Candidate     `json:"candidates"`
	Retire     []RetireRequest `json:"retire,omitempty"`
	Promotions []Promotion     `json:"promotions,omitempty"`
}

// ParseExtractionOutput parses and validates a structured extraction response.
// maxRetire bounds removals for this run (see MaxRetirePerSessionRun /
// MaxRetirePerReviewRun): retire requests and promotions carrying a source_id
// share the same allowance, so wrapping retirements as promotions cannot
// multiply it.
//
//   - candidates: [] with no retirements or promotions is a legal no-op.
//   - malformed JSON, a missing/malformed candidates field, or an unknown enum
//     value is a failure (ErrInvalidExtraction), never a no-op.
//   - per-item validation (lengths, paths, cognitive state consistency, secrets)
//     drops only the offending item; the survivors are still returned with their
//     drop reasons so callers can commit the rest.
//
// Retirement targets are only checked for shape here. Whether an ID is still
// active, and whether it is allowed to be retired at all, needs the active
// snapshot and is enforced at commit time.
func ParseExtractionOutput(data []byte, maxRetire int) (*ExtractionOutput, error) {
	var env extractionEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidExtraction, err)
	}
	if env.Candidates == nil {
		return nil, fmt.Errorf("%w: missing candidates field", ErrInvalidExtraction)
	}
	out := &ExtractionOutput{}
	for i, c := range env.Candidates {
		// Schema-level problems (unknown enum, role mismatch) invalidate the
		// whole output; content-level problems drop only the offending
		// candidate so the survivors can still be committed.
		if err := validateCandidateStructural(c); err != nil {
			return nil, fmt.Errorf("%w: candidate %d: %v", ErrInvalidExtraction, i, err)
		}
		if err := validateCandidateDroppable(c); err != nil {
			out.Dropped = append(out.Dropped, fmt.Sprintf("candidate %d dropped: %v", i, err))
			continue
		}
		// Second-layer secret cleaning: a candidate whose raw text still carries
		// a high-risk secret shape is dropped entirely (a sanitized quote still
		// leaks that a secret exists and where); survivors are re-sanitized
		// before anything is written to disk.
		if HighRisk(c.Statement) || HighRisk(c.Rationale) || HighRisk(c.Application) || HighRisk(c.Summary) || HighRisk(strings.Join(c.ProjectPaths, " ")) {
			out.Dropped = append(out.Dropped, fmt.Sprintf("candidate %d dropped: high-risk secret pattern", i))
			continue
		}
		c.Statement = SanitizeText(c.Statement)
		c.Rationale = SanitizeText(c.Rationale)
		c.Application = SanitizeText(c.Application)
		c.Summary = SanitizeText(c.Summary)
		out.Candidates = append(out.Candidates, c)
	}
	seenRetire := make(map[string]bool, len(env.Retire))
	for i, r := range env.Retire {
		// A retirement is a removal, so an unusable one is always dropped rather
		// than failing the batch: refusing the whole run would also discard the
		// valid additions, and the retry would hit the same bad item.
		if err := validateRetireDroppable(r, seenRetire); err != nil {
			out.Dropped = append(out.Dropped, fmt.Sprintf("retire %d dropped: %v", i, err))
			continue
		}
		if len(out.Retire) >= maxRetire {
			out.Dropped = append(out.Dropped, fmt.Sprintf("retire %d dropped: over per-run limit (%d)", i, maxRetire))
			continue
		}
		seenRetire[r.ID] = true
		r.Reason = SanitizeText(r.Reason)
		out.Retire = append(out.Retire, r)
	}
	sourceRemovals := 0
	for i, p := range env.Promotions {
		if !validPromotionTargets[p.Target] {
			return nil, fmt.Errorf("%w: promotion %d: unknown target %q", ErrInvalidExtraction, i, p.Target)
		}
		if err := validatePromotionDroppable(p); err != nil {
			out.Dropped = append(out.Dropped, fmt.Sprintf("promotion %d dropped: %v", i, err))
			continue
		}
		// A promotion that unindexes its active source is a removal like any
		// retire and shares the same per-run allowance: otherwise wrapping
		// retirements as promotions would multiply the removal budget. Whether
		// a specific source may leave at all (still active, not user-stated)
		// needs the active snapshot and is enforced at commit time.
		if p.SourceID != "" && len(out.Retire)+sourceRemovals >= maxRetire {
			out.Dropped = append(out.Dropped, fmt.Sprintf("promotion %d dropped: source removal over per-run retire limit (%d)", i, maxRetire))
			continue
		}
		if len(out.Promotions) >= maxPromotionsPerRun {
			out.Dropped = append(out.Dropped, fmt.Sprintf("promotion %d dropped: over per-run limit (%d)", i, maxPromotionsPerRun))
			continue
		}
		if HighRisk(p.DraftText) || HighRisk(p.Reason) || HighRisk(p.SuggestedLocation) || HighRisk(p.Summary) {
			out.Dropped = append(out.Dropped, fmt.Sprintf("promotion %d dropped: high-risk secret pattern", i))
			continue
		}
		p.DraftText = SanitizeText(p.DraftText)
		p.Reason = SanitizeText(p.Reason)
		p.SuggestedLocation = SanitizeText(p.SuggestedLocation)
		p.Summary = SanitizeText(p.Summary)
		if p.SourceID != "" {
			sourceRemovals++
		}
		out.Promotions = append(out.Promotions, p)
	}
	return out, nil
}

// validateRetireDroppable checks a retirement request's shape. Activity and
// eligibility of the target need the active snapshot and are checked at commit.
func validateRetireDroppable(r RetireRequest, seen map[string]bool) error {
	if !ValidateRecordID(r.ID) {
		return fmt.Errorf("invalid record id %q", r.ID)
	}
	if seen[r.ID] {
		return fmt.Errorf("duplicate retire target %q", r.ID)
	}
	if strings.TrimSpace(r.Reason) == "" {
		return errors.New("empty reason")
	}
	if len(r.Reason) > maxRetireReasonLen {
		return fmt.Errorf("reason too long (%d > %d)", len(r.Reason), maxRetireReasonLen)
	}
	if !summaryUsable(r.Reason) {
		return errors.New("reason must be a single line without managed markers")
	}
	return nil
}

// validatePromotionDroppable checks a promotion suggestion's content bounds.
func validatePromotionDroppable(p Promotion) error {
	if p.SourceID != "" && !ValidateRecordID(p.SourceID) {
		return fmt.Errorf("invalid source id %q", p.SourceID)
	}
	if strings.TrimSpace(p.DraftText) == "" {
		return errors.New("empty draft_text")
	}
	if strings.TrimSpace(p.Summary) == "" {
		return errors.New("empty summary")
	}
	if len(p.Summary) > maxSummaryLen {
		return fmt.Errorf("summary too long (%d > %d)", len(p.Summary), maxSummaryLen)
	}
	if !summaryUsable(p.Summary) {
		return errors.New("summary must be a single line without managed markers")
	}
	if len(p.DraftText) > maxPromotionDraftLen {
		return fmt.Errorf("draft_text too long (%d > %d)", len(p.DraftText), maxPromotionDraftLen)
	}
	if strings.TrimSpace(p.Reason) == "" {
		return errors.New("empty reason")
	}
	if len(p.Reason) > maxRetireReasonLen {
		return fmt.Errorf("reason too long (%d > %d)", len(p.Reason), maxRetireReasonLen)
	}
	if len(p.SuggestedLocation) > maxSummaryLen {
		return fmt.Errorf("suggested_location too long (%d > %d)", len(p.SuggestedLocation), maxSummaryLen)
	}
	if !summaryUsable(p.SuggestedLocation) {
		return errors.New("suggested_location must be a single line without managed markers")
	}
	// draft_text may be multi-line guidance, so only carriage returns and the
	// markers that would break MEMORY.md parsing are rejected.
	if strings.ContainsRune(p.DraftText, '\r') {
		return errors.New("draft_text must not contain carriage returns")
	}
	if strings.Contains(p.DraftText, managedStartMarker) || strings.Contains(p.DraftText, managedEndMarker) {
		return errors.New("draft_text must not contain managed markers")
	}
	return nil
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
// presence and length bounds, path/ID shape, session-local identifiers that
// must never become durable text, and characters that would break the managed
// index or record Markdown. A violation drops only the offending candidate;
// the surviving candidates are still committed.
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
	// A pitfall claims something about this codebase, so it must be able to point
	// at the code it is about. Without a path it is generic advice or a note about
	// the assistant's own output, which belongs in project instructions rather
	// than in per-turn memory. Other types legitimately have no path: an
	// environment fact or a stated preference need not map to a file.
	if c.Type == TypePitfall && len(c.ProjectPaths) == 0 {
		return errors.New("pitfall without project paths")
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
	// Session-local identifiers are prompt-discouraged and partly
	// machine-checkable, so the checkable shapes are dropped deterministically:
	// a commit SHA or machine-absolute path in durable text would either go
	// stale or leak a local layout into every later session. Temporary pins and
	// one-off flaky-test noise have no stable machine-checkable shape (version
	// strings and the word "flaky" also appear in legitimate durable text), so
	// they stay at the prompt level only.
	for _, field := range []string{c.Statement, c.Rationale, c.Application, c.Summary} {
		if containsSessionSHALike(field) {
			return errors.New("statement contains a session-local commit SHA")
		}
		if absolutePathRe.MatchString(field) {
			return errors.New("statement contains a machine-absolute path")
		}
	}
	return nil
}

// containsSessionSHALike reports whether text carries a hex token shaped like a
// session commit SHA: at least 7 hex chars mixing a-f letters and digits.
// Pure-letter matches are ordinary English words (defaced, effaced) and
// pure-digit matches are issue numbers, counts, timeouts, or dates, so neither
// trips the check.
func containsSessionSHALike(text string) bool {
	for _, m := range sessionSHALikeRe.FindAllString(text, -1) {
		if strings.ContainsAny(m, "abcdef") && strings.ContainsAny(m, "0123456789") {
			return true
		}
	}
	return false
}
