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
Read: allow
Grep: allow
Bash: ask
`
	node := mustParseYAML(t, input)
	rs := ParsePermission(node)

	expected := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "Read", Pattern: "*", Action: ActionAllow},
		{Permission: "Grep", Pattern: "*", Action: ActionAllow},
		{Permission: "Bash", Pattern: "*", Action: ActionAsk},
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
Read: allow
Bash:
  "*": ask
  "rm *": deny
  "git *": ask
Skill:
  "go-expert": allow
  "code-review": deny
`
	node := mustParseYAML(t, input)
	rs := ParsePermission(node)

	expected := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "Read", Pattern: "*", Action: ActionAllow},
		{Permission: "Bash", Pattern: "*", Action: ActionAsk},
		{Permission: "Bash", Pattern: "rm *", Action: ActionDeny},
		{Permission: "Bash", Pattern: "git *", Action: ActionAsk},
		{Permission: "Skill", Pattern: "go-expert", Action: ActionAllow},
		{Permission: "Skill", Pattern: "code-review", Action: ActionDeny},
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
	if action := rs.Evaluate("Bash", "ls"); action != ActionDeny {
		t.Fatalf("expected deny for empty ruleset, got %s", action)
	}
}

func TestEvaluate_LastMatchWins(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "Bash", Pattern: "*", Action: ActionAsk},
		{Permission: "Bash", Pattern: "git *", Action: ActionAllow},
	}

	tests := []struct {
		perm, pattern string
		want          Action
	}{
		{"Bash", "git push", ActionAllow},    // matches "git *" rule
		{"Bash", "ls -la", ActionAsk},        // matches "Bash *" rule
		{"Read", "somefile.txt", ActionDeny}, // matches "*" wildcard deny
		{"Bash", "git", ActionAllow},         // "git *" pattern matches "git" (trailing " *" is optional)
	}
	for _, tt := range tests {
		got := rs.Evaluate(tt.perm, tt.pattern)
		if got != tt.want {
			t.Errorf("Evaluate(%q, %q) = %s; want %s", tt.perm, tt.pattern, got, tt.want)
		}
	}
}

func TestEvaluate_OverridePrecedence(t *testing.T) {
	// A later "allow" overrides an earlier "deny" for the same pattern.
	rs := Ruleset{
		{Permission: "Bash", Pattern: "rm *", Action: ActionDeny},
		{Permission: "Bash", Pattern: "rm *", Action: ActionAllow},
	}
	if got := rs.Evaluate("Bash", "rm -rf /"); got != ActionAllow {
		t.Errorf("expected allow (last match wins), got %s", got)
	}
}

func TestEvaluate_FullConfigScenario(t *testing.T) {
	input := `
"*": deny
Read: allow
Grep: allow
Glob: allow
Bash:
  "*": ask
  "rm *": deny
  "curl *": deny
  "git *": ask
Task: allow
`
	node := mustParseYAML(t, input)
	rs := ParsePermission(node)

	tests := []struct {
		perm, pattern string
		want          Action
	}{
		{"Read", "anyfile", ActionAllow},
		{"Grep", "pattern", ActionAllow},
		{"Glob", "**/*.go", ActionAllow},
		{"Bash", "echo hello", ActionAsk},
		{"Bash", "rm -rf /tmp/test", ActionDeny},
		{"Bash", "curl https://example.com", ActionDeny},
		{"Bash", "git commit -m fix", ActionAsk},
		{"Task", "do something", ActionAllow},
		{"Write", "something", ActionDeny},  // not explicitly allowed
		{"Unknown", "anything", ActionDeny}, // default deny
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
		{Permission: "Read", Pattern: "*", Action: ActionAllow},
		{Permission: "Bash", Pattern: "*", Action: ActionAsk},
		{Permission: "Bash", Pattern: "rm *", Action: ActionDeny},
	}

	tests := []struct {
		tool string
		want bool
	}{
		{"Read", false},   // allowed
		{"Bash", false},   // ask (not fully denied)
		{"Write", true},   // matches "*" deny (no specific rule)
		{"Unknown", true}, // matches "*" deny
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
		{Permission: "Bash", Pattern: "*", Action: ActionAllow},
		{Permission: "Bash", Pattern: "rm *", Action: ActionDeny},
	}
	// Last rule matching "Bash" by permission is "rm *" deny, but pattern != "*",
	// so IsDisabled scans further back and finds the "*" allow rule.
	// Actually, IsDisabled finds the LAST rule matching the tool name,
	// which is "rm *" deny. Since pattern is "rm *" (not "*"), it returns false.
	if rs.IsDisabled("Bash") {
		t.Error("Bash should not be disabled; only 'rm *' sub-pattern is denied")
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
		{Permission: "Read", Pattern: "*", Action: ActionAllow},
	}
	override := Ruleset{
		{Permission: "Bash", Pattern: "*", Action: ActionAllow},
	}

	merged := Merge(base, override)
	if len(merged) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(merged))
	}

	// Later ruleset rules come last and thus take precedence in Evaluate.
	if got := merged.Evaluate("Bash", "anything"); got != ActionAllow {
		t.Errorf("merged.Evaluate(Bash, anything) = %s; want allow (override)", got)
	}
	// Base rules still work for non-overridden tools.
	if got := merged.Evaluate("Read", "file.txt"); got != ActionAllow {
		t.Errorf("merged.Evaluate(Read, file.txt) = %s; want allow (base)", got)
	}
	// Default deny from base still applies to unknown tools.
	if got := merged.Evaluate("Write", "file.txt"); got != ActionDeny {
		t.Errorf("merged.Evaluate(Write, file.txt) = %s; want deny (base default)", got)
	}
}

func TestMerge_OverridePrecedence(t *testing.T) {
	base := Ruleset{
		{Permission: "Bash", Pattern: "*", Action: ActionDeny},
	}
	project := Ruleset{
		{Permission: "Bash", Pattern: "*", Action: ActionAsk},
	}
	session := Ruleset{
		{Permission: "Bash", Pattern: "*", Action: ActionAllow},
	}

	merged := Merge(base, project, session)
	// Session (last) overrides project and base.
	if got := merged.Evaluate("Bash", "ls"); got != ActionAllow {
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
		{"Bash", "Bash", true},
		{"Bash", "B?sh", true},
		{"Bash", "B??h", true},
		{"Bash", "B???", true},
		{"Bash", "B????", false},
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
Read: allow
Grep: allow
Glob: allow
Question: allow
Skill: allow
Bash:
  "*": ask
  "rm *": deny
  "curl *": deny
  "git *": ask
Task: allow
TodoWrite: allow
`
	node := mustParseYAML(t, input)
	rs := ParsePermission(node)

	// Verify the architecture doc example scenario.
	tests := []struct {
		perm    string
		pattern string
		want    Action
	}{
		{"Read", "/etc/hosts", ActionAllow},
		{"Grep", "TODO", ActionAllow},
		{"Glob", "**/*.go", ActionAllow},
		{"Question", "how are you?", ActionAllow},
		{"Skill", "go-expert", ActionAllow},
		{"Bash", "go test ./...", ActionAsk},
		{"Bash", "rm -rf /tmp", ActionDeny},
		{"Bash", "curl http://evil.com", ActionDeny},
		{"Bash", "git push", ActionAsk},
		{"Bash", "git", ActionAsk},
		{"Task", "refactor code", ActionAllow},
		{"TodoWrite", "add item", ActionAllow},
		{"Write", "file.go", ActionDeny}, // not in allowed list
		{"Edit", "file.go", ActionDeny},  // not in allowed list
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
Read: allow
Grep: allow
Glob: allow
Bash:
  "*": ask
  "rm *": deny
  "git *": ask
Task: allow
`)
	var doc yaml.Node
	if err := yaml.NewDecoder(input).Decode(&doc); err != nil {
		b.Fatal(err)
	}
	rs := ParsePermission(doc.Content[0])

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rs.Evaluate("Bash", "git push --force")
	}
}
