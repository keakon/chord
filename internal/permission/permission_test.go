package permission

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// helper: parse a YAML string into a yaml.Node and return the root value node.
func mustParseYAML(t *testing.T, input string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(input), &doc); err != nil {
		t.Fatalf("failed to parse YAML: %v", err)
	}
	// yaml.Unmarshal wraps in a DocumentNode; the actual content is the first child.
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return &doc
}

// ---------- ParsePermission tests ----------

func TestParsePermission_Nil(t *testing.T) {
	rs := ParsePermission(nil)
	if rs != nil {
		t.Fatalf("expected nil, got %v", rs)
	}
}

func TestParsePermission_Scalar(t *testing.T) {
	node := mustParseYAML(t, `deny`)
	rs := ParsePermission(node)

	if len(rs) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs))
	}
	r := rs[0]
	if r.Permission != "*" || r.Pattern != "*" || r.Action != ActionDeny {
		t.Fatalf("unexpected rule: %+v", r)
	}
}

func TestParsePermission_SimpleMapping(t *testing.T) {
	input := `
"*": deny
read: allow
grep: allow
shell: ask
`
	node := mustParseYAML(t, input)
	rs := ParsePermission(node)

	expected := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "read", Pattern: "*", Action: ActionAllow},
		{Permission: "grep", Pattern: "*", Action: ActionAllow},
		{Permission: "shell", Pattern: "*", Action: ActionAsk},
	}
	if len(rs) != len(expected) {
		t.Fatalf("expected %d rules, got %d: %+v", len(expected), len(rs), rs)
	}
	for i, e := range expected {
		if rs[i] != e {
			t.Errorf("rule[%d]: expected %+v, got %+v", i, e, rs[i])
		}
	}
}

func TestParsePermission_NestedMapping(t *testing.T) {
	input := `
"*": deny
read: allow
shell:
  "*": ask
  "rm *": deny
  "git *": ask
skill:
  "go-expert": allow
  "code-review": deny
`
	node := mustParseYAML(t, input)
	rs := ParsePermission(node)

	expected := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "read", Pattern: "*", Action: ActionAllow},
		{Permission: "shell", Pattern: "*", Action: ActionAsk},
		{Permission: "shell", Pattern: "rm *", Action: ActionDeny},
		{Permission: "shell", Pattern: "git *", Action: ActionAsk},
		{Permission: "skill", Pattern: "go-expert", Action: ActionAllow},
		{Permission: "skill", Pattern: "code-review", Action: ActionDeny},
	}
	if len(rs) != len(expected) {
		t.Fatalf("expected %d rules, got %d: %+v", len(expected), len(rs), rs)
	}
	for i, e := range expected {
		if rs[i] != e {
			t.Errorf("rule[%d]: expected %+v, got %+v", i, e, rs[i])
		}
	}
}

func TestParsePermission_UnsupportedKind(t *testing.T) {
	// Sequence node should return nil.
	node := mustParseYAML(t, `[a, b, c]`)
	rs := ParsePermission(node)
	if rs != nil {
		t.Fatalf("expected nil for sequence node, got %v", rs)
	}
}

// ---------- Evaluate tests ----------

func TestEvaluate_DefaultDeny(t *testing.T) {
	rs := Ruleset{}
	if action := rs.Evaluate("shell", "ls"); action != ActionDeny {
		t.Fatalf("expected deny for empty ruleset, got %s", action)
	}
}

func TestEvaluate_LastMatchWins(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "shell", Pattern: "*", Action: ActionAsk},
		{Permission: "shell", Pattern: "git *", Action: ActionAllow},
	}

	tests := []struct {
		perm, pattern string
		want          Action
	}{
		{"shell", "git push", ActionAllow},   // matches "git *" rule
		{"shell", "ls -la", ActionAsk},       // matches "shell *" rule
		{"read", "somefile.txt", ActionDeny}, // matches "*" wildcard deny
		{"shell", "git", ActionAllow},        // "git *" pattern matches "git" (trailing " *" is optional)
	}
	for _, tt := range tests {
		got := rs.Evaluate(tt.perm, tt.pattern)
		if got != tt.want {
			t.Errorf("Evaluate(%q, %q) = %s; want %s", tt.perm, tt.pattern, got, tt.want)
		}
	}
}

func TestEvaluate_ShellAllowRuleDoesNotAutoAllowCompoundCommands(t *testing.T) {
	rs := Ruleset{
		{Permission: "shell", Pattern: "*", Action: ActionAsk},
		{Permission: "shell", Pattern: "git *", Action: ActionAllow},
	}

	tests := []string{
		"git status; rm -rf ~",
		"git status && rm -rf ~",
		"git status || rm -rf ~",
		"git status | cat",
		"git status & rm -rf ~",
		"git status\nrm -rf ~",
	}
	for _, command := range tests {
		if got := rs.Evaluate("shell", command); got != ActionAsk {
			t.Fatalf("Evaluate(shell, %q) = %s, want ask", command, got)
		}
	}
	if got := rs.Evaluate("shell", "git status 'a;b'"); got != ActionAllow {
		t.Fatalf("quoted separator command = %s, want allow", got)
	}
}

func TestEvaluate_OverridePrecedence(t *testing.T) {
	// A later "allow" overrides an earlier "deny" for the same pattern.
	rs := Ruleset{
		{Permission: "shell", Pattern: "rm *", Action: ActionDeny},
		{Permission: "shell", Pattern: "rm *", Action: ActionAllow},
	}
	if got := rs.Evaluate("shell", "rm -rf /"); got != ActionAllow {
		t.Errorf("expected allow (last match wins), got %s", got)
	}
}

func TestEvaluate_FullConfigScenario(t *testing.T) {
	input := `
"*": deny
read: allow
grep: allow
glob: allow
shell:
  "*": ask
  "rm *": deny
  "curl *": deny
  "git *": ask
task: allow
`
	node := mustParseYAML(t, input)
	rs := ParsePermission(node)

	tests := []struct {
		perm, pattern string
		want          Action
	}{
		{"read", "anyfile", ActionAllow},
		{"grep", "pattern", ActionAllow},
		{"glob", "**/*.go", ActionAllow},
		{"shell", "echo hello", ActionAsk},
		{"shell", "rm -rf /tmp/test", ActionDeny},
		{"shell", "curl https://example.com", ActionDeny},
		{"shell", "git commit -m fix", ActionAsk},
		{"task", "do something", ActionAllow},
		{"write", "something", ActionDeny},  // not explicitly allowed
		{"unknown", "anything", ActionDeny}, // default deny
	}
	for _, tt := range tests {
		got := rs.Evaluate(tt.perm, tt.pattern)
		if got != tt.want {
			t.Errorf("Evaluate(%q, %q) = %s; want %s", tt.perm, tt.pattern, got, tt.want)
		}
	}
}

// ---------- IsDisabled tests ----------

func TestIsDisabled(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "read", Pattern: "*", Action: ActionAllow},
		{Permission: "shell", Pattern: "*", Action: ActionAsk},
		{Permission: "shell", Pattern: "rm *", Action: ActionDeny},
	}

	tests := []struct {
		tool string
		want bool
	}{
		{"read", false},   // allowed
		{"shell", false},  // ask (not fully denied)
		{"write", true},   // matches "*" deny (no specific rule)
		{"unknown", true}, // matches "*" deny
	}
	for _, tt := range tests {
		got := rs.IsDisabled(tt.tool)
		if got != tt.want {
			t.Errorf("IsDisabled(%q) = %v; want %v", tt.tool, got, tt.want)
		}
	}
}

func TestIsDisabled_SubPatternDeny(t *testing.T) {
	// If the last matching rule has a specific pattern (not "*"), tool is not disabled.
	rs := Ruleset{
		{Permission: "shell", Pattern: "*", Action: ActionAllow},
		{Permission: "shell", Pattern: "rm *", Action: ActionDeny},
	}
	// Last rule matching "shell" by permission is "rm *" deny, but pattern != "*",
	// so IsDisabled scans further back and finds the "*" allow rule.
	// Actually, IsDisabled finds the LAST rule matching the tool name,
	// which is "rm *" deny. Since pattern is "rm *" (not "*"), it returns false.
	if rs.IsDisabled("shell") {
		t.Error("Shell should not be disabled; only 'rm *' sub-pattern is denied")
	}
}

func TestIsDisabled_EmptyRuleset(t *testing.T) {
	rs := Ruleset{}
	// No rules means tool is not actively disabled (though Evaluate would return deny).
	if rs.IsDisabled("Anything") {
		t.Error("empty ruleset should not disable tools (returns false, not true)")
	}
}

// ---------- Merge tests ----------

func TestMerge_Empty(t *testing.T) {
	merged := Merge()
	if len(merged) != 0 {
		t.Fatalf("expected empty, got %d rules", len(merged))
	}
}

func TestMerge_Concatenation(t *testing.T) {
	base := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "read", Pattern: "*", Action: ActionAllow},
	}
	override := Ruleset{
		{Permission: "shell", Pattern: "*", Action: ActionAllow},
	}

	merged := Merge(base, override)
	if len(merged) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(merged))
	}

	// Later ruleset rules come last and thus take precedence in Evaluate.
	if got := merged.Evaluate("shell", "anything"); got != ActionAllow {
		t.Errorf("merged.Evaluate(shell, anything) = %s; want allow (override)", got)
	}
	// Base rules still work for non-overridden tools.
	if got := merged.Evaluate("read", "file.txt"); got != ActionAllow {
		t.Errorf("merged.Evaluate(read, file.txt) = %s; want allow (base)", got)
	}
	// Default deny from base still applies to unknown tools.
	if got := merged.Evaluate("write", "file.txt"); got != ActionDeny {
		t.Errorf("merged.Evaluate(write, file.txt) = %s; want deny (base default)", got)
	}
}

func TestMerge_OverridePrecedence(t *testing.T) {
	base := Ruleset{
		{Permission: "shell", Pattern: "*", Action: ActionDeny},
	}
	project := Ruleset{
		{Permission: "shell", Pattern: "*", Action: ActionAsk},
	}
	session := Ruleset{
		{Permission: "shell", Pattern: "*", Action: ActionAllow},
	}

	merged := Merge(base, project, session)
	// Session (last) overrides project and base.
	if got := merged.Evaluate("shell", "ls"); got != ActionAllow {
		t.Errorf("expected allow from session override, got %s", got)
	}
}

// ---------- globMatch tests ----------

func TestGlobMatch_Exact(t *testing.T) {
	if !globMatch("hello", "hello") {
		t.Error("exact match should succeed")
	}
	if globMatch("hello", "world") {
		t.Error("different strings should not match")
	}
}

func TestGlobMatch_Star(t *testing.T) {
	if !globMatch("anything", "*") {
		t.Error("* should match any string")
	}
	if !globMatch("", "*") {
		t.Error("* should match empty string")
	}
	if !globMatch("git push --force", "git *") {
		t.Error("git * should match 'git push --force'")
	}
}

func TestGlobMatch_QuestionMark(t *testing.T) {
	if !globMatch("a", "?") {
		t.Error("? should match single char")
	}
	if globMatch("ab", "?") {
		t.Error("? should not match two chars")
	}
	if !globMatch("abc", "a?c") {
		t.Error("a?c should match abc")
	}
}

func TestGlobMatch_TrailingStarOptional(t *testing.T) {
	// "ls *" should match "ls" (no args) and "ls -la" (with args).
	if !globMatch("ls", "ls *") {
		t.Error("'ls *' should match 'ls' (trailing ' *' is optional)")
	}
	if !globMatch("ls -la", "ls *") {
		t.Error("'ls *' should match 'ls -la'")
	}
	if !globMatch("ls ", "ls *") {
		t.Error("'ls *' should match 'ls ' (trailing space)")
	}
}

func TestGlobMatch_TrailingStarOptional_Git(t *testing.T) {
	// "git *" should match "git" and "git push".
	if !globMatch("git", "git *") {
		t.Error("'git *' should match 'git'")
	}
	if !globMatch("git push", "git *") {
		t.Error("'git *' should match 'git push'")
	}
}

func TestGlobMatch_MiddleStar(t *testing.T) {
	// Star not at trailing position — standard glob behavior.
	if !globMatch("foobar", "foo*") {
		t.Error("foo* should match foobar")
	}
	if !globMatch("foobar", "*bar") {
		t.Error("*bar should match foobar")
	}
	if !globMatch("fooXbar", "foo*bar") {
		t.Error("foo*bar should match fooXbar")
	}
}

func TestGlobMatch_SpecialRegexChars(t *testing.T) {
	// Patterns with regex special chars should be escaped properly.
	if !globMatch("file.txt", "file.txt") {
		t.Error("literal dot should match")
	}
	if globMatch("fileTtxt", "file.txt") {
		t.Error("dot in pattern should be literal, not regex wildcard")
	}
	if !globMatch("[test]", "[test]") {
		t.Error("square brackets should be literal")
	}
}

func TestGlobMatch_ComplexPatterns(t *testing.T) {
	tests := []struct {
		str     string
		pattern string
		want    bool
	}{
		{"rm -rf /", "rm *", true},
		{"rm", "rm *", true},         // trailing " *" optional
		{"rmdir foo", "rm *", false}, // "rm *" should NOT match "rmdir foo"
		{"curl https://example.com", "curl *", true},
		{"curl", "curl *", true},
		{"shell", "shell", true},
		{"shell", "s?ell", true},
		{"shell", "s??ll", true},
		{"shell", "s???l", true},
		{"shell", "s?????", false},
	}
	for _, tt := range tests {
		got := globMatch(tt.str, tt.pattern)
		if got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v; want %v", tt.str, tt.pattern, got, tt.want)
		}
	}
}

func TestGlobMatch_EmptyStrings(t *testing.T) {
	if !globMatch("", "") {
		t.Error("empty string should match empty pattern")
	}
	if !globMatch("", "*") {
		t.Error("empty string should match *")
	}
	if globMatch("a", "") {
		t.Error("non-empty string should not match empty pattern")
	}
}

// ---------- Integration: ParsePermission + Evaluate ----------

func TestIntegration_ParseAndEvaluate(t *testing.T) {
	input := `
"*": deny
read: allow
grep: allow
glob: allow
question: allow
skill: allow
shell:
  "*": ask
  "rm *": deny
  "curl *": deny
  "git *": ask
task: allow
todo_write: allow
`
	node := mustParseYAML(t, input)
	rs := ParsePermission(node)

	// Verify the architecture doc example scenario.
	tests := []struct {
		perm    string
		pattern string
		want    Action
	}{
		{"read", "/etc/hosts", ActionAllow},
		{"grep", "TODO", ActionAllow},
		{"glob", "**/*.go", ActionAllow},
		{"question", "how are you?", ActionAllow},
		{"skill", "go-expert", ActionAllow},
		{"shell", "go test ./...", ActionAsk},
		{"shell", "rm -rf /tmp", ActionDeny},
		{"shell", "curl http://evil.com", ActionDeny},
		{"shell", "git push", ActionAsk},
		{"shell", "git", ActionAsk},
		{"task", "refactor code", ActionAllow},
		{"todo_write", "add item", ActionAllow},
		{"write", "file.go", ActionDeny}, // not in allowed list
		{"edit", "file.go", ActionDeny},  // not in allowed list
	}

	for _, tt := range tests {
		got := rs.Evaluate(tt.perm, tt.pattern)
		if got != tt.want {
			t.Errorf("Evaluate(%q, %q) = %s; want %s", tt.perm, tt.pattern, got, tt.want)
		}
	}
}

// ---------- Benchmark ----------

func BenchmarkGlobMatch(b *testing.B) {
	// Pre-warm the cache.
	globMatch("git push --force", "git *")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		globMatch("git push --force", "git *")
	}
}

func BenchmarkEvaluate(b *testing.B) {
	input := strings.NewReader(`
"*": deny
read: allow
grep: allow
glob: allow
shell:
  "*": ask
  "rm *": deny
  "git *": ask
task: allow
`)
	var doc yaml.Node
	if err := yaml.NewDecoder(input).Decode(&doc); err != nil {
		b.Fatal(err)
	}
	rs := ParsePermission(doc.Content[0])

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rs.Evaluate("shell", "git push --force")
	}
}
