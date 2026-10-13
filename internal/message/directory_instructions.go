package message

import (
	"crypto/sha256"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	KindDirectoryInstructions                      = "directory_instructions"
	KindDirectoryInstructionProjection             = "directory_instruction_projection"
	DirectoryInstructionUpsert                     = "upsert"
	DirectoryInstructionRemove                     = "remove"
	DirectoryInstructionBlocked                    = "blocked"
	DirectoryInstructionReasonIO                   = "io"
	DirectoryInstructionReasonPermission           = "permission"
	DirectoryInstructionReasonCapacity             = "capacity"
	DirectoryInstructionReasonEncoding             = "encoding"
	DirectoryInstructionReasonSize                 = "size"
	DirectoryInstructionReasonInputBudget          = "input_budget"
	DirectoryInstructionReasonOutOfScope           = "out_of_scope"
	ToolControlReasonDirectoryInstructionsRequired = "directory_instructions_required"
	ToolControlReasonDirectoryInstructionsReplan   = "directory_instructions_replan"
	ToolControlReasonDirectoryInstructionsBlocked  = "directory_instructions_blocked"
)

type DirectoryInstructionFact struct {
	CheckoutRoot string `json:"checkout_root"`
	Scope        string `json:"scope"`
	Operation    string `json:"operation"`
	SourcePath   string `json:"source_path,omitempty"`
	Body         string `json:"body,omitempty"`
	BodyHash     string `json:"body_hash,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Detail       string `json:"detail,omitempty"`
}
type DirectoryInstructions struct {
	Facts []DirectoryInstructionFact `json:"facts"`
}
type DirectoryInstructionRef struct{ CheckoutRoot, Scope, SourcePath, BodyHash string }

func DirectoryInstructionBodyHash(body string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(body))))
}
func (f DirectoryInstructionFact) Ref() DirectoryInstructionRef {
	return DirectoryInstructionRef{f.CheckoutRoot, f.Scope, f.SourcePath, f.BodyHash}
}
func ValidateDirectoryInstructionFact(f DirectoryInstructionFact) error {
	if !filepath.IsAbs(f.CheckoutRoot) || filepath.Clean(f.CheckoutRoot) != f.CheckoutRoot {
		return fmt.Errorf("invalid directory instruction checkout root")
	}
	if f.Scope == "" || path.IsAbs(f.Scope) || path.Clean(f.Scope) != f.Scope || f.Scope == ".." || strings.HasPrefix(f.Scope, "../") || strings.Contains(f.Scope, "\\") {
		return fmt.Errorf("invalid directory instruction scope %q", f.Scope)
	}
	if f.SourcePath != "" && !filepath.IsAbs(f.SourcePath) {
		return fmt.Errorf("invalid directory instruction source")
	}
	if len(f.Detail) > 1024 || !utf8.ValidString(f.Detail) {
		return fmt.Errorf("invalid directory instruction detail")
	}
	if f.Body != "" && (len(f.Body) > 64<<10 || !utf8.ValidString(f.Body) || strings.TrimSpace(f.Body) != f.Body || f.BodyHash != DirectoryInstructionBodyHash(f.Body)) {
		return fmt.Errorf("invalid directory instruction body/hash")
	}
	if f.Body == "" && f.BodyHash != "" {
		return fmt.Errorf("directory instruction hash without body")
	}
	switch f.Operation {
	case DirectoryInstructionUpsert:
		if f.Body == "" || f.SourcePath == "" || f.Reason != "" {
			return fmt.Errorf("incomplete directory instruction upsert")
		}
	case DirectoryInstructionRemove:
		if f.Body != "" || f.Reason != "" {
			return fmt.Errorf("invalid directory instruction remove")
		}
	case DirectoryInstructionBlocked:
		if f.Reason == "" || (f.Body != "" && f.Reason != DirectoryInstructionReasonInputBudget) {
			return fmt.Errorf("invalid blocked directory instruction")
		}
	default:
		return fmt.Errorf("unknown directory instruction operation %q", f.Operation)
	}
	return nil
}
func ValidateDirectoryInstructions(m Message) error {
	if m.Kind != KindDirectoryInstructions {
		return nil
	}
	if m.Role != RoleUser || m.Content != "" || len(m.Parts) > 0 || m.DirectoryInstructions == nil || len(m.DirectoryInstructions.Facts) == 0 {
		return fmt.Errorf("invalid directory instructions message")
	}
	for _, f := range m.DirectoryInstructions.Facts {
		if err := ValidateDirectoryInstructionFact(f); err != nil {
			return err
		}
	}
	return nil
}
func IsDirectoryInstructions(m Message) bool {
	return m.Kind == KindDirectoryInstructions && ValidateDirectoryInstructions(m) == nil
}
func SortDirectoryInstructionFacts(facts []DirectoryInstructionFact) {
	sort.SliceStable(facts, func(i, j int) bool {
		a, b := facts[i], facts[j]
		if a.CheckoutRoot != b.CheckoutRoot {
			return a.CheckoutRoot < b.CheckoutRoot
		}
		depth := func(s string) int {
			if s == "." {
				return 0
			}
			return strings.Count(s, "/") + 1
		}
		if depth(a.Scope) != depth(b.Scope) {
			return depth(a.Scope) < depth(b.Scope)
		}
		return a.Scope < b.Scope
	})
}

// RenderDirectoryInstructionFacts is shared by initial and dynamic projections.
func RenderDirectoryInstructionFacts(facts []DirectoryInstructionFact) string {
	var b strings.Builder
	for _, f := range facts {
		if f.Operation != DirectoryInstructionUpsert {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("# AGENTS.md instructions\n\nDeeper directory rules override parent rules only within their subtree; sibling rules do not override each other. System instructions, explicit user requests, and role permissions remain higher priority.\n")
		}
		fmt.Fprintf(&b, "\nScope: %s (checkout: %s)\nSource: %s\n<INSTRUCTIONS>\n%s\n</INSTRUCTIONS>\n", f.Scope, f.CheckoutRoot, f.SourcePath, f.Body)
	}
	return b.String()
}

// DirectoryInstructionAccountingMessages projects durable facts for token
// accounting without retaining stale versions or charging blocked bodies.
func DirectoryInstructionAccountingMessages(messages []Message) []Message {
	type key struct{ root, scope string }
	type entry struct {
		fact  DirectoryInstructionFact
		index int
	}
	latest := make(map[key]entry)
	hasFacts := false
	for i, m := range messages {
		if m.Kind != KindDirectoryInstructions {
			continue
		}
		hasFacts = true
		if !IsDirectoryInstructions(m) {
			continue
		}
		for _, f := range m.DirectoryInstructions.Facts {
			latest[key{f.CheckoutRoot, f.Scope}] = entry{f, i}
		}
	}
	if !hasFacts {
		return messages
	}
	byIndex := make(map[int][]DirectoryInstructionFact)
	var blocked []DirectoryInstructionFact
	for _, e := range latest {
		switch e.fact.Operation {
		case DirectoryInstructionUpsert:
			byIndex[e.index] = append(byIndex[e.index], e.fact)
		case DirectoryInstructionBlocked:
			blocked = append(blocked, e.fact)
		}
	}
	out := make([]Message, 0, len(messages)+1)
	for i, m := range messages {
		if m.Kind != KindDirectoryInstructions {
			out = append(out, m)
			continue
		}
		if fs := byIndex[i]; len(fs) > 0 {
			SortDirectoryInstructionFacts(fs)
			out = append(out, Message{Role: RoleUser, Kind: KindDirectoryInstructionProjection, Content: RenderDirectoryInstructionFacts(fs)})
		}
	}
	if len(blocked) > 0 {
		SortDirectoryInstructionFacts(blocked)
		var b strings.Builder
		b.WriteString("Directory rules unavailable; modifications in affected scopes remain blocked.\n")
		count := 0
		for _, f := range blocked {
			line := fmt.Sprintf("%s: %s\n", f.Scope, f.Reason)
			if count >= 8 || b.Len()+len(line) > 1900 {
				break
			}
			b.WriteString(line)
			count++
		}
		if count < len(blocked) {
			fmt.Fprintf(&b, "%d additional blocked scopes.\n", len(blocked)-count)
		}
		out = append(out, Message{Role: RoleUser, Kind: KindDirectoryInstructionProjection, Content: b.String()})
	}
	return out
}
