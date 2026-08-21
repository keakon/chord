package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Type classifies the durable memory a record captures.
type Type string

const (
	TypePreference Type = "preference"
	TypeFact       Type = "fact"
	TypeWorkflow   Type = "workflow"
	TypePitfall    Type = "pitfall"
)

var validTypes = map[Type]bool{
	TypePreference: true,
	TypeFact:       true,
	TypeWorkflow:   true,
	TypePitfall:    true,
}

// Confidence records how the memory was learned. Assistant-reported material
// must not be upgraded to user-stated fact.
type Confidence string

const (
	ConfidenceUserStated Confidence = "user_stated"
	ConfidenceReported   Confidence = "reported"
	ConfidenceUncertain  Confidence = "uncertain"
)

var validConfidences = map[Confidence]bool{
	ConfidenceUserStated: true,
	ConfidenceReported:   true,
	ConfidenceUncertain:  true,
}

// Outcome records the apparent result of the work a candidate refers to. It is
// observational metadata, not a guarantee for future use.
type Outcome string

const (
	OutcomeSuccess   Outcome = "success"
	OutcomePartial   Outcome = "partial"
	OutcomeFailed    Outcome = "fail"
	OutcomeUncertain Outcome = "uncertain"
)

var validOutcomes = map[Outcome]bool{
	OutcomeSuccess:   true,
	OutcomePartial:   true,
	OutcomeFailed:    true,
	OutcomeUncertain: true,
}

const (
	maxStatementLen   = 2000
	maxRationaleLen   = 2000
	maxApplicationLen = 2000
	maxSummaryLen     = 300
	maxProjectPaths   = 8
	maxSupersedes     = 8
	hashHexLen        = 16
)

// Record is the immutable content of one detailed memory file
// (<slug>--<hash>.md under .chord/memory/records/). Records never change after
// write; corrections produce a new record and atomically update the index.
type Record struct {
	ID                string     `json:"id" yaml:"id"`
	Type              Type       `json:"type" yaml:"type"`
	Created           time.Time  `json:"created" yaml:"created"`
	OriginSessionID   string     `json:"origin_session_id" yaml:"origin_session_id"`
	SourceFingerprint string     `json:"source_fingerprint" yaml:"source_fingerprint"`
	Confidence        Confidence `json:"confidence" yaml:"confidence"`
	Outcome           Outcome    `json:"outcome" yaml:"outcome"`
	Supersedes        []string   `json:"supersedes,omitempty" yaml:"supersedes,omitempty"`
	// Summary is the one-line index preview shown in MEMORY.md.
	Summary string `json:"summary" yaml:"summary"`
	// Statement is the current durable conclusion captured by this memory.
	Statement string `json:"-" yaml:"-"`
	// Rationale explains why the conclusion matters beyond its source session.
	Rationale string `json:"-" yaml:"-"`
	// Application identifies the future trigger and action for using it.
	Application string `json:"-" yaml:"-"`
	// ProjectPaths are project-root-relative file references (never absolute,
	// never escaping via "..").
	ProjectPaths []string `json:"-" yaml:"-"`
}

// canonicalHashPayload builds the canonical bytes that identify a record. It
// deliberately excludes volatile fields (last_used, index position) so
// identical durable content maps to the same ID, while different content cannot
// collide.
func (r *Record) canonicalHashPayload() []byte {
	paths := append([]string(nil), r.ProjectPaths...)
	sort.Strings(paths)

	var b strings.Builder
	fmt.Fprintf(&b, "type=%s\n", r.Type)
	fmt.Fprintf(&b, "confidence=%s\n", r.Confidence)
	fmt.Fprintf(&b, "outcome=%s\n", r.Outcome)
	b.WriteString("statement=" + r.Statement + "\n")
	b.WriteString("rationale=" + r.Rationale + "\n")
	b.WriteString("application=" + r.Application + "\n")
	b.WriteString("summary=" + r.Summary + "\n")
	for _, p := range paths {
		b.WriteString("path=" + p + "\n")
	}
	return []byte(b.String())
}

// ContentHash returns the content-addressed hash (hex, hashHexLen chars) of the
// record's durable fields. It is the second half of a record's ID.
func (r *Record) ContentHash() string {
	sum := sha256.Sum256(r.canonicalHashPayload())
	return hex.EncodeToString(sum[:])[:hashHexLen]
}

// slugify derives a stable, filesystem-safe slug from the summary. It keeps at
// most a few words so record IDs stay readable and bounded.
func slugify(summary string) string {
	summary = strings.ToLower(strings.TrimSpace(summary))
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range summary {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == ',':
			flush()
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	if len(words) > 4 {
		words = words[:4]
	}
	if len(words) == 0 {
		words = []string{"memory"}
	}
	slug := strings.Join(words, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		cut := slug[:40]
		// Never cut in the middle of a multi-byte rune: an invalid UTF-8 slug
		// would not round-trip ValidateRecordID and would fail the commit.
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
		slug = strings.TrimRight(cut, "-")
	}
	if slug == "" {
		return "memory"
	}
	return slug
}

// RecordID computes the stable record ID from a summary and content hash.
func RecordID(summary, contentHash string) string {
	return slugify(summary) + "--" + contentHash
}

// ValidateRecordID reports whether id matches <hashHexLen hex chars> and a
// non-empty slug suffix "--" join.
func ValidateRecordID(id string) bool {
	slug, hash, ok := strings.Cut(id, "--")
	if !ok || hash == "" || slug == "" {
		return false
	}
	if len(hash) != hashHexLen {
		return false
	}
	for _, r := range hash {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return slugify(slug) == slug
}

// recordFileName is the immutable file name for a record.
func recordFileName(id string) string {
	return id + ".md"
}

// recordPath returns the absolute path under recordsDir for id.
func recordPath(recordsDir, id string) string {
	return filepath.Join(recordsDir, recordFileName(id))
}

// validateRecordBounds applies the fixed caps shared by extraction validation
// and manual writes.
func validateRecordBounds(r *Record) error {
	if r == nil {
		return errors.New("record is nil")
	}
	if !validTypes[r.Type] {
		return fmt.Errorf("invalid record type %q", r.Type)
	}
	if !validConfidences[r.Confidence] {
		return fmt.Errorf("invalid record confidence %q", r.Confidence)
	}
	if !validOutcomes[r.Outcome] {
		return fmt.Errorf("invalid record outcome %q", r.Outcome)
	}
	if strings.TrimSpace(r.Statement) == "" {
		return errors.New("record statement is empty")
	}
	if len(r.Statement) > maxStatementLen {
		return fmt.Errorf("record statement too long (%d > %d)", len(r.Statement), maxStatementLen)
	}
	if strings.TrimSpace(r.Rationale) == "" {
		return errors.New("record rationale is empty")
	}
	if len(r.Rationale) > maxRationaleLen {
		return fmt.Errorf("record rationale too long (%d > %d)", len(r.Rationale), maxRationaleLen)
	}
	if strings.TrimSpace(r.Application) == "" {
		return errors.New("record application is empty")
	}
	if len(r.Application) > maxApplicationLen {
		return fmt.Errorf("record application too long (%d > %d)", len(r.Application), maxApplicationLen)
	}
	if len(r.Summary) > maxSummaryLen {
		return fmt.Errorf("record summary too long (%d > %d)", len(r.Summary), maxSummaryLen)
	}
	if !summaryUsable(r.Summary) {
		return errors.New("record summary must be a single line without managed markers")
	}
	if len(r.ProjectPaths) > maxProjectPaths {
		return fmt.Errorf("too many project paths (%d > %d)", len(r.ProjectPaths), maxProjectPaths)
	}
	if len(r.Supersedes) > maxSupersedes {
		return fmt.Errorf("too many supersedes (%d > %d)", len(r.Supersedes), maxSupersedes)
	}
	for _, p := range r.ProjectPaths {
		if err := validateProjectPathString(p); err != nil {
			return err
		}
		if !pathUsable(p) {
			return fmt.Errorf("project path %q contains forbidden characters", p)
		}
	}
	for _, s := range r.Supersedes {
		if !ValidateRecordID(s) {
			return fmt.Errorf("invalid supersedes record id %q", s)
		}
	}
	return nil
}

// validateProjectPathString is the lexical gate for record paths. Paths are
// stored slash-separated and resolved against the project root by Layout.
func validateProjectPathString(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return errors.New("empty project path")
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("project path must be relative, got %q", p)
	}
	for _, part := range strings.Split(filepath.Clean(filepath.FromSlash(p)), string(filepath.Separator)) {
		if part == ".." {
			return fmt.Errorf("project path %q escapes the project root", p)
		}
	}
	return nil
}

// frontmatter fields carried in the immutable record file.
type recordFrontmatter struct {
	ID                string     `yaml:"id"`
	Type              Type       `yaml:"type"`
	Created           time.Time  `yaml:"created"`
	OriginSessionID   string     `yaml:"origin_session_id"`
	SourceFingerprint string     `yaml:"source_fingerprint"`
	Confidence        Confidence `yaml:"confidence"`
	Outcome           Outcome    `yaml:"outcome"`
	Summary           string     `yaml:"summary"`
	Supersedes        []string   `yaml:"supersedes,omitempty"`
}

// MarshalRecord renders a record as immutable Markdown with a YAML frontmatter
// carrying the stable identity fields, the summary line, the conclusion,
// rationale, application guidance, and project-path references.
func MarshalRecord(r *Record) ([]byte, error) {
	if err := validateRecordBounds(r); err != nil {
		return nil, err
	}
	fm := recordFrontmatter{
		ID:                r.ID,
		Type:              r.Type,
		Created:           r.Created,
		OriginSessionID:   r.OriginSessionID,
		SourceFingerprint: r.SourceFingerprint,
		Confidence:        r.Confidence,
		Outcome:           r.Outcome,
		Summary:           r.Summary,
		Supersedes:        append([]string(nil), r.Supersedes...),
	}
	if len(fm.Supersedes) == 0 {
		fm.Supersedes = nil
	}
	yamlData, err := yaml.Marshal(&fm)
	if err != nil {
		return nil, fmt.Errorf("marshal record frontmatter: %w", err)
	}
	parts := append([]string(nil), r.ProjectPaths...)
	sort.Strings(parts)
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(yamlData)
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimSpace(r.Statement) + "\n\n")
	sb.WriteString("## Why it matters\n\n")
	sb.WriteString(strings.TrimSpace(r.Rationale) + "\n\n")
	sb.WriteString("## How to apply\n\n")
	sb.WriteString(strings.TrimSpace(r.Application) + "\n\n")
	if len(parts) > 0 {
		sb.WriteString("## Relevant project paths\n\n")
		for _, p := range parts {
			sb.WriteString("- `" + p + "`\n")
		}
	}
	return []byte(sb.String()), nil
}

// ParseRecord parses record bytes produced by MarshalRecord. It is lossless for
// the fields that participate in the index and canonical hash.
func ParseRecord(data []byte) (*Record, error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return nil, errors.New("record missing frontmatter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return nil, errors.New("record missing frontmatter terminator")
	}
	var fm recordFrontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return nil, fmt.Errorf("parse record frontmatter: %w", err)
	}
	body := strings.TrimSpace(rest[end+len("\n---\n"):])
	r := &Record{
		ID:                fm.ID,
		Type:              fm.Type,
		Created:           fm.Created,
		OriginSessionID:   fm.OriginSessionID,
		SourceFingerprint: fm.SourceFingerprint,
		Confidence:        fm.Confidence,
		Outcome:           fm.Outcome,
		Summary:           fm.Summary,
		Supersedes:        fm.Supersedes,
	}
	const (
		whyHeading         = "\n\n## Why it matters\n\n"
		applicationHeading = "\n\n## How to apply\n\n"
		pathsHeading       = "\n\n## Relevant project paths\n\n"
	)
	whyIdx := strings.Index(body, whyHeading)
	if whyIdx < 0 {
		return nil, errors.New("record missing why-it-matters section")
	}
	afterWhy := body[whyIdx+len(whyHeading):]
	applicationIdx := strings.Index(afterWhy, applicationHeading)
	if applicationIdx < 0 {
		return nil, errors.New("record missing how-to-apply section")
	}
	r.Statement = strings.TrimSpace(body[:whyIdx])
	r.Rationale = strings.TrimSpace(afterWhy[:applicationIdx])
	applicationAndPaths := afterWhy[applicationIdx+len(applicationHeading):]
	if pathsIdx := strings.Index(applicationAndPaths, pathsHeading); pathsIdx >= 0 {
		r.Application = strings.TrimSpace(applicationAndPaths[:pathsIdx])
		pathsSection := applicationAndPaths[pathsIdx+len(pathsHeading):]
		for _, ln := range strings.Split(strings.TrimSpace(pathsSection), "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "- `") && strings.HasSuffix(ln, "`") {
				r.ProjectPaths = append(r.ProjectPaths, strings.TrimSuffix(strings.TrimPrefix(ln, "- `"), "`"))
			}
		}
	} else {
		r.Application = strings.TrimSpace(applicationAndPaths)
	}
	if err := validateRecordBounds(r); err != nil {
		return nil, err
	}
	return r, nil
}

// loadRecord reads and parses an immutable record file.
func loadRecord(path string) (*Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseRecord(data)
}
