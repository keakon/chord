package tui

import (
	"testing"
)

func TestSuggestRulePatterns_BashSimple(t *testing.T) {
	candidates := suggestRulePatterns("Bash", `{"command":"git log --oneline"}`, nil, "/home/user/project")
	if len(candidates) == 0 {
		t.Fatal("expected candidates for simple bash command")
	}

	// First candidate should be literal
	if candidates[0].Pattern != "git log --oneline" {
		t.Errorf("expected literal pattern first, got %q", candidates[0].Pattern)
	}

	// Should have head2
	found := false
	for _, c := range candidates {
		if c.Pattern == "git log *" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'git log *' candidate")
	}
}

func TestSuggestRulePatterns_BashNeedsApproval(t *testing.T) {
	candidates := suggestRulePatterns("Bash", `{"command":"git log --oneline && git status"}`, []string{"git status"}, "/home/user/project")
	if len(candidates) == 0 {
		t.Fatal("expected candidates")
	}
	// Should use needsApproval[0] as seed
	if candidates[0].Pattern != "git status" {
		t.Errorf("expected literal 'git status' first when needsApproval is set, got %q", candidates[0].Pattern)
	}
}

func TestSuggestRulePatterns_BashHighRisk(t *testing.T) {
	candidates := suggestRulePatterns("Bash", `{"command":"rm -rf /tmp/foo"}`, nil, "/home/user/project")
	if len(candidates) == 0 {
		t.Fatal("expected candidates for high-risk command")
	}
	// High risk should default to literal
	if !candidates[0].Default {
		t.Error("expected literal to be default for high-risk command")
	}
	// Should not have head2/head1 for high-risk
	for _, c := range candidates {
		if c.Pattern == "rm *" || c.Pattern == "rm -rf *" {
			t.Errorf("unexpected broad candidate for high-risk command: %q", c.Pattern)
		}
	}
}

func TestSuggestRulePatterns_BashComplex(t *testing.T) {
	candidates := suggestRulePatterns("Bash", `{"command":"git log | grep foo"}`, nil, "/home/user/project")
	if len(candidates) == 0 {
		t.Fatal("expected candidates for complex command")
	}
	// Complex command: should only have literal + very broad
	if len(candidates) > 2 {
		t.Errorf("expected at most 2 candidates for complex command, got %d", len(candidates))
	}
	if candidates[0].Pattern != "git log | grep foo" {
		t.Errorf("expected literal pattern first, got %q", candidates[0].Pattern)
	}
}

func TestSuggestRulePatterns_WriteFile(t *testing.T) {
	candidates := suggestRulePatterns("Write", `{"path":"internal/tui/app.go","content":"..."}`, nil, "/home/user/project")
	if len(candidates) == 0 {
		t.Fatal("expected candidates for Write tool")
	}

	// First should be literal
	if candidates[0].Pattern != "internal/tui/app.go" {
		t.Errorf("expected literal path first, got %q", candidates[0].Pattern)
	}

	// Should have dir/*
	found := false
	for _, c := range candidates {
		if c.Pattern == "internal/tui/*" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'internal/tui/*' candidate")
	}
}

func TestSuggestRulePatterns_EditFile(t *testing.T) {
	candidates := suggestRulePatterns("Edit", `{"path":"docs/README.md"}`, nil, "/home/user/project")
	if len(candidates) == 0 {
		t.Fatal("expected candidates for Edit tool")
	}

	// Should have *.md pattern
	found := false
	for _, c := range candidates {
		if c.Pattern == "**/*.md" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected '**/*.md' candidate")
	}
}

func TestSuggestRulePatterns_WebFetch(t *testing.T) {
	candidates := suggestRulePatterns("WebFetch", `{"url":"https://example.com/api/v1/data"}`, nil, "")
	if len(candidates) == 0 {
		t.Fatal("expected candidates for WebFetch")
	}

	// First should be literal URL
	if candidates[0].Pattern != "https://example.com/api/v1/data" {
		t.Errorf("expected literal URL first, got %q", candidates[0].Pattern)
	}

	// Should have path/* and host/*
	foundPath := false
	foundHost := false
	for _, c := range candidates {
		if c.Pattern == "https://example.com/api/v1/*" {
			foundPath = true
		}
		if c.Pattern == "https://example.com/*" {
			foundHost = true
		}
	}
	if !foundPath {
		t.Error("expected 'https://example.com/api/v1/*' candidate")
	}
	if !foundHost {
		t.Error("expected 'https://example.com/*' candidate")
	}
}

func TestSuggestRulePatterns_WebFetchWithPortAndQuery(t *testing.T) {
	candidates := suggestRulePatterns("WebFetch", `{"url":"https://example.com:8443/api/v1/data?q=ok"}`, nil, "")
	foundPath := false
	foundHost := false
	for _, c := range candidates {
		if c.Pattern == "https://example.com:8443/api/v1/*" {
			foundPath = true
		}
		if c.Pattern == "https://example.com:8443/*" {
			foundHost = true
		}
	}
	if !foundPath || !foundHost {
		t.Fatalf("expected path and host candidates, got %#v", candidates)
	}
}

func TestSuggestRulePatterns_Delete(t *testing.T) {
	candidates := suggestRulePatterns("Delete", `{"paths":["foo.go"]}`, nil, "")
	if candidates != nil {
		t.Error("expected nil candidates for Delete tool")
	}
}

func TestSuggestRulePatterns_Read(t *testing.T) {
	candidates := suggestRulePatterns("Read", `{"path":"foo.go"}`, nil, "")
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate for Read, got %d", len(candidates))
	}
	if candidates[0].Pattern != "*" {
		t.Errorf("expected '*' pattern for Read, got %q", candidates[0].Pattern)
	}
}

func TestSuggestRulePatterns_Grep(t *testing.T) {
	candidates := suggestRulePatterns("Grep", `{"pattern":"TODO"}`, nil, "")
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate for Grep, got %d", len(candidates))
	}
	if candidates[0].Pattern != "*" {
		t.Errorf("expected '*' pattern for Grep, got %q", candidates[0].Pattern)
	}
}

func TestSuggestRulePatterns_BashSudo(t *testing.T) {
	candidates := suggestRulePatterns("Bash", `{"command":"sudo apt install foo"}`, nil, "")
	if len(candidates) == 0 {
		t.Fatal("expected candidates for sudo command")
	}
	if !candidates[0].Default || candidates[0].Pattern != "sudo apt install foo" {
		t.Error("sudo should default to literal")
	}
}

func TestSuggestRulePatterns_BashComplexCommandFallsBackToLiteral(t *testing.T) {
	candidates := suggestRulePatterns("Bash", `{"command":"cat <<'EOF'\nhello\nEOF"}`, nil, "")
	if len(candidates) < 1 {
		t.Fatal("expected candidates for heredoc command")
	}
	if got := candidates[0].Pattern; got != "cat <<'EOF'\nhello\nEOF" {
		t.Fatalf("first pattern = %q, want literal heredoc", got)
	}
	if !candidates[0].Default {
		t.Fatal("expected literal heredoc candidate to be default")
	}
}

func TestSuggestRulePatterns_BashMultilineFallsBackToLiteral(t *testing.T) {
	candidates := suggestRulePatterns("Bash", `{"command":"echo one\necho two"}`, nil, "")
	if len(candidates) < 1 {
		t.Fatal("expected candidates for multiline command")
	}
	if got := candidates[0].Pattern; got != "echo one\necho two" {
		t.Fatalf("first pattern = %q, want multiline literal", got)
	}
	if !candidates[0].Default {
		t.Fatal("expected multiline literal candidate to be default")
	}
}

func TestSuggestRulePatterns_BashSingleWord(t *testing.T) {
	candidates := suggestRulePatterns("Bash", `{"command":"ls"}`, nil, "")
	if len(candidates) == 0 {
		t.Fatal("expected candidates for single-word command")
	}
	// Should have literal and *
	if candidates[0].Pattern != "ls" {
		t.Errorf("expected literal 'ls' first, got %q", candidates[0].Pattern)
	}
	foundHead1Default := false
	for _, c := range candidates {
		if c.Pattern == "ls *" && c.Default {
			foundHead1Default = true
			break
		}
	}
	if !foundHead1Default {
		t.Error("expected head1 wildcard to be default for single-word command")
	}
}

func TestSuggestRulePatterns_WriteRelativePathDefaultsToDirScope(t *testing.T) {
	candidates := suggestRulePatterns("Write", `{"path":"docs/guide.md","content":"..."}`, nil, "/home/user/project")
	foundDefaultDir := false
	for _, c := range candidates {
		if c.Pattern == "docs/*" && c.Default {
			foundDefaultDir = true
			break
		}
	}
	if !foundDefaultDir {
		t.Error("expected docs/* to be default for cwd-relative write path")
	}
}

func TestSuggestRulePatterns_WriteParentPathDoesNotDefaultToDirScope(t *testing.T) {
	candidates := suggestRulePatterns("Write", `{"path":"../secret/plan.md","content":"..."}`, nil, "/home/user/project")
	for _, c := range candidates {
		if c.Pattern == "../secret/*" && c.Default {
			t.Fatalf("unexpected default dir candidate for parent path: %+v", c)
		}
	}
}
