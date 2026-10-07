package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ManualDraft is an explicit user-reviewed mutation, never an extraction result.
type ManualDraft struct {
	Base    *ReviewSnapshot
	All     bool
	Records []*Record
	Remove  []RetireRequest
	Issues  []string
}

// NewRemovalDraft creates a deterministic index-only change.
func NewRemovalDraft(base *ReviewSnapshot) *ManualDraft {
	d := &ManualDraft{Base: base}
	for _, i := range base.Items {
		d.Remove = append(d.Remove, RetireRequest{ID: i.Entry.ID, Reason: "Removed by user"})
	}
	return d
}

// ParseOrganization fails the whole preview if any candidate is unusable.
func ParseOrganization(base *ReviewSnapshot, all bool, sessionID string, data []byte) (*ManualDraft, error) {
	if len(data) > maxReviewTotalBytes {
		return nil, fmt.Errorf("organization result too large")
	}
	var result struct {
		Candidates []Candidate     `json:"candidates"`
		Retire     []RetireRequest `json:"retire"`
		Issues     []string        `json:"issues"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return nil, fmt.Errorf("parse memory organization: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("organization must contain exactly one JSON object")
	}
	d := &ManualDraft{Base: base, All: all, Remove: result.Retire, Issues: result.Issues}
	for _, c := range result.Candidates {
		// Approval permits a write; it does not turn model wording into user testimony.
		c.SourceRole = SourceRoleAssistant
		c.Confidence = ConfidenceUncertain
		c.Outcome = OutcomeUncertain
		if err := validateCandidateDroppable(c); err != nil {
			return nil, fmt.Errorf("invalid organization candidate: %w", err)
		}
		if HighRisk(c.Statement + "\n" + c.Rationale + "\n" + c.Application + "\n" + c.Summary + "\n" + strings.Join(c.ProjectPaths, "\n")) {
			return nil, fmt.Errorf("organization candidate contains sensitive content")
		}
		if len(c.Supersedes) == 0 {
			return nil, fmt.Errorf("organization candidate requires source record IDs")
		}
		rec := recordFromCandidate(sessionID, "manual-organization", c)
		rec.ID = RecordID(rec.Summary, rec.ContentHash())
		d.Records = append(d.Records, rec)
	}
	if err := d.validate(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *ManualDraft) validate() error {
	if d == nil || d.Base == nil || d.Base.Index == nil {
		return fmt.Errorf("missing memory preview baseline")
	}
	targets := map[string]bool{}
	for _, i := range d.Base.Items {
		entry, ok := findReviewEntry(d.Base.Index.Managed, i.Entry.ID)
		if !ok || entry != i.Entry || targets[i.Entry.ID] {
			return fmt.Errorf("invalid memory preview baseline")
		}
		targets[i.Entry.ID] = true
	}
	if d.All && len(targets) != len(d.Base.Index.Managed) {
		return fmt.Errorf("full organization requires every active memory")
	}
	if len(d.Issues) > 32 {
		return fmt.Errorf("too many unresolved memory questions")
	}
	for _, issue := range d.Issues {
		if len(issue) > maxStatementLen || HighRisk(issue) {
			return fmt.Errorf("invalid unresolved memory question")
		}
	}
	used := map[string]bool{}
	claim := func(id string) error {
		if !targets[id] || !ValidateRecordID(id) {
			return fmt.Errorf("organization references an unselected memory %s", id)
		}
		if used[id] {
			return fmt.Errorf("memory %s appears in multiple changes", id)
		}
		used[id] = true
		return nil
	}
	newIDs := map[string]bool{}
	for _, r := range d.Records {
		if r == nil {
			return fmt.Errorf("empty replacement record")
		}
		if err := validateRecordBounds(r); err != nil {
			return err
		}
		if HighRisk(r.Statement + "\n" + r.Rationale + "\n" + r.Application + "\n" + r.Summary + "\n" + strings.Join(r.ProjectPaths, "\n")) {
			return fmt.Errorf("replacement contains sensitive content")
		}
		if r.Confidence != ConfidenceUncertain || r.Outcome != OutcomeUncertain || r.ID != RecordID(r.Summary, r.ContentHash()) || len(r.Supersedes) == 0 {
			return fmt.Errorf("invalid manual replacement provenance")
		}
		if newIDs[r.ID] {
			return fmt.Errorf("duplicate replacement %s", r.ID)
		}
		newIDs[r.ID] = true
		for _, id := range r.Supersedes {
			if err := claim(id); err != nil {
				return err
			}
		}
	}
	retired := map[string]bool{}
	for _, r := range d.Remove {
		if err := validateRetireDroppable(r, retired); err != nil {
			return err
		}
		retired[r.ID] = true
		if err := claim(r.ID); err != nil {
			return err
		}
	}
	return nil
}

func (d *ManualDraft) changedIDs() []string {
	var out []string
	for _, r := range d.Records {
		out = append(out, r.Supersedes...)
	}
	for _, r := range d.Remove {
		out = append(out, r.ID)
	}
	return out
}

func (d *ManualDraft) Empty() bool { return d == nil || len(d.Records)+len(d.Remove) == 0 }

// Preview renders only changed entries and unresolved questions, not the full index.
func (d *ManualDraft) Preview() string {
	var b strings.Builder
	b.WriteString("# Memory changes\n\n")
	for _, r := range d.Records {
		b.WriteString("## Replace\n\n")
		for _, id := range r.Supersedes {
			for _, i := range d.Base.Items {
				if i.Entry.ID == id {
					fmt.Fprintf(&b, "- %s (%s)\n", i.Entry.Summary, id)
				}
			}
		}
		body, _ := MarshalRecord(r)
		fmt.Fprintf(&b, "\n### Replacement\n\n%s\n", body)
	}
	for _, r := range d.Remove {
		for _, i := range d.Base.Items {
			if i.Entry.ID == r.ID {
				fmt.Fprintf(&b, "## Remove: %s\n\n%s\n\n", i.Entry.Summary, r.Reason)
			}
		}
	}
	if d.Empty() {
		b.WriteString("No changes to apply.\n\n")
	}
	if len(d.Issues) > 0 {
		b.WriteString("## Needs clarification (unchanged)\n\n")
		for _, s := range d.Issues {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	return b.String()
}
