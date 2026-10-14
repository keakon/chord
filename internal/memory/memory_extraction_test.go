package memory

import (
	"strings"
	"testing"
)

func TestExtractionOutputParsing(t *testing.T) {
	valid := `{"candidates":[{"type":"preference","statement":"Prefer focused verification before broad test suites.","rationale":"Focused checks provide faster and clearer feedback.","application":"Run changed-package checks before broader suites.","summary":"Prefer focused verification.","source_role":"user","confidence":"user_stated","outcome":"success","project_paths":["a.go"]}]}`
	out, err := ParseExtractionOutput([]byte(valid), MaxRetirePerSessionRun)
	cands, dropped := outParts(out)
	if err != nil {
		t.Fatalf("ParseExtractionOutput: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("valid candidate dropped: %v", dropped)
	}
	if len(cands) != 1 || cands[0].Type != TypePreference || cands[0].Confidence != ConfidenceUserStated {
		t.Fatalf("candidates = %+v", cands)
	}
	// Empty candidates is a legal no-op.
	empty := `{"candidates":[]}`
	out, err = ParseExtractionOutput([]byte(empty), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil || len(cands) != 0 || len(dropped) != 0 {
		t.Fatalf("empty candidates: %v %v %v", cands, dropped, err)
	}
	// Malformed JSON is a failure.
	if _, err := ParseExtractionOutput([]byte("{not json"), MaxRetirePerSessionRun); err == nil {
		t.Fatal("expected failure for malformed JSON")
	}
	// Unknown enum is a failure.
	badEnum := `{"candidates":[{"type":"bogus","statement":"x","rationale":"why","application":"how","summary":"x","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	if _, err := ParseExtractionOutput([]byte(badEnum), MaxRetirePerSessionRun); err == nil {
		t.Fatal("expected failure for unknown enum")
	}
	// user_stated requires source_role user.
	mismatch := `{"candidates":[{"type":"fact","statement":"x","rationale":"why","application":"how","summary":"x","source_role":"assistant","confidence":"user_stated","outcome":"success"}]}`
	if _, err := ParseExtractionOutput([]byte(mismatch), MaxRetirePerSessionRun); err == nil {
		t.Fatal("expected failure for user_stated with assistant source")
	}
	// Path escaping is rejected by dropping the offending candidate; the rest
	// of the output still commits.
	badPath := `{"candidates":[{"type":"fact","statement":"x","rationale":"why","application":"how","summary":"x","source_role":"user","confidence":"user_stated","outcome":"success","project_paths":["../escape"]}]}`
	out, err = ParseExtractionOutput([]byte(badPath), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("escaping path must drop the candidate, not fail the run: %v", err)
	}
	if len(dropped) != 1 || len(cands) != 0 {
		t.Fatalf("escaping path: dropped=%v cands=%v, want 1 drop and no candidates", dropped, cands)
	}
	// A summary that would corrupt the managed index is dropped per-candidate.
	badSummary := `{"candidates":[{"type":"fact","statement":"x","rationale":"why","application":"how","summary":"line one\n<!-- chord:managed:start -->","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	out, err = ParseExtractionOutput([]byte(badSummary), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("broken summary must drop the candidate, not fail the run: %v", err)
	}
	if len(dropped) != 1 || len(cands) != 0 {
		t.Fatalf("broken summary: dropped=%v cands=%v, want 1 drop and no candidates", dropped, cands)
	}
	// A high-risk candidate is dropped with a reason, and the rest of the
	// output is still returned so the survivors can be committed.
	mixed := `{"candidates":[
		{"type":"fact","statement":"good one","rationale":"why it matters","application":"how to apply it","summary":"good","source_role":"user","confidence":"user_stated","outcome":"success"},
		{"type":"fact","statement":"the key is sk-abcdefghijklmnopqrstuvwx","rationale":"why it matters","application":"how to apply it","summary":"fine","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	out, err = ParseExtractionOutput([]byte(mixed), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("mixed output must not fail the whole run: %v", err)
	}
	if len(dropped) != 1 {
		t.Fatalf("expected one dropped candidate, got %v", dropped)
	}
	if len(cands) != 1 || cands[0].Statement != "good one" {
		t.Fatalf("survivor lost: %+v", cands)
	}
	// An all-secret output drops everything without failing the run.
	secret := `{"candidates":[{"type":"fact","statement":"the key is sk-abcdefghijklmnopqrstuvwx and the rest is fine","rationale":"why it matters","application":"how to apply it","summary":"fine","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	out, err = ParseExtractionOutput([]byte(secret), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("secret-only output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("secret candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
}

// The envelope's key vocabulary separates "this response found nothing to
// record" from "this response is not an extraction result". Only the first is a
// no-op: reading an unrelated object as a no-op would advance the checkpoint and
// skip the session without a trace.
func TestExtractionEnvelopeKeyVocabulary(t *testing.T) {
	noOp := []struct {
		name string
		raw  string
	}{
		{"empty object", `{}`},
		{"empty candidates", `{"candidates":[]}`},
		{"retire only", `{"retire":[]}`},
		{"promotions only", `{"promotions":[]}`},
		{"null list", `{"candidates":null}`},
	}
	for _, tc := range noOp {
		out, err := ParseExtractionOutput([]byte(tc.raw), MaxRetirePerSessionRun)
		if err != nil {
			t.Fatalf("%s (%s) must parse as a no-op: %v", tc.name, tc.raw, err)
		}
		cands, dropped := outParts(out)
		if len(cands) != 0 || len(dropped) != 0 {
			t.Fatalf("%s: cands=%+v dropped=%v, want nothing to commit", tc.name, cands, dropped)
		}
	}
	// A subset envelope still commits what it does carry.
	subset := `{"retire":[{"id":"one--1111111111111111","reason":"covered by project instructions"}]}`
	out, err := ParseExtractionOutput([]byte(subset), MaxRetirePerSessionRun)
	if err != nil {
		t.Fatalf("subset envelope must parse: %v", err)
	}
	if len(out.Retire) != 1 || out.Retire[0].ID != "one--1111111111111111" {
		t.Fatalf("subset envelope retire = %+v", out.Retire)
	}
	invalid := []struct {
		name string
		raw  string
	}{
		{"unknown key", `{"candidates":[],"notes":"nothing found"}`},
		{"unknown key alone", `{"foo":1}`},
		{"non-object document", `null`},
		{"array document", `[]`},
		{"wrong field type", `{"candidates":{}}`},
		{"truncated JSON", `{"candidates":[]`},
		{"trailing data", `{"candidates":[]} and that is all`},
	}
	for _, tc := range invalid {
		if got, err := ParseExtractionOutput([]byte(tc.raw), MaxRetirePerSessionRun); err == nil {
			t.Fatalf("%s (%s) must fail, got %+v", tc.name, tc.raw, got)
		}
	}
}

// Session-local identifiers are machine-checkable, so the deterministic layer
// drops them per-candidate: a commit SHA or machine-absolute path in durable
// text either goes stale or leaks a local layout into every later session.
// Prompt advice alone did not stop the same shapes from recurring post-rule.
func TestExtractionDropsSessionLocalIdentifiers(t *testing.T) {
	sha := `{"candidates":[{"type":"fact","statement":"Fixed in commit e127cde6 with the new helper.","rationale":"why it matters","application":"how to apply it","summary":"A fix","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	out, err := ParseExtractionOutput([]byte(sha), MaxRetirePerSessionRun)
	cands, dropped := outParts(out)
	if err != nil {
		t.Fatalf("SHA output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("SHA candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
	if !strings.Contains(dropped[0], "statement contains a session-local commit SHA") {
		t.Fatalf("SHA drop reason should name the field, got %v", dropped)
	}
	upper := `{"candidates":[{"type":"fact","statement":"Fixed in commit E127CDE6 with the new helper.","rationale":"why it matters","application":"how to apply it","summary":"A fix","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	out, err = ParseExtractionOutput([]byte(upper), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("uppercase SHA output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("uppercase SHA candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
	rationaleSHA := `{"candidates":[{"type":"fact","statement":"Keep mechanical lint fixes short.","rationale":"The user said e127cde6 can be reworded.","application":"how to apply it","summary":"A preference","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	out, err = ParseExtractionOutput([]byte(rationaleSHA), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("rationale SHA output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("rationale SHA candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
	if !strings.Contains(dropped[0], "rationale contains a session-local commit SHA") {
		t.Fatalf("rationale SHA drop reason should name the field, got %v", dropped)
	}
	abs := `{"candidates":[{"type":"fact","statement":"Config lives on this machine.","rationale":"why it matters","application":"Check /Users/tester/projects/chord for the file.","summary":"A fact","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	out, err = ParseExtractionOutput([]byte(abs), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("absolute-path output must not fail the run: %v", err)
	}
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("absolute-path candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
	if !strings.Contains(dropped[0], "application contains a machine-absolute path") {
		t.Fatalf("absolute-path drop reason should name the field, got %v", dropped)
	}
	// Best-effort prefixes beyond the common roots, plus Windows drives.
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"root", `{"candidates":[{"type":"fact","statement":"Config at /root/f must exist.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"mnt", `{"candidates":[{"type":"fact","statement":"Data under /mnt/d/f is cached.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"drive", `{"candidates":[{"type":"fact","statement":"Log at C:\\Users\\x\\f shows it.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"devshm", `{"candidates":[{"type":"fact","statement":"Scratch files live under /dev/shm/chord-scratch.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"devtty", `{"candidates":[{"type":"workflow","statement":"Attach the debugger to /dev/tty.usbserial-1420.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
	} {
		out, err := ParseExtractionOutput([]byte(tc.raw), MaxRetirePerSessionRun)
		cands, dropped := outParts(out)
		if err != nil {
			t.Fatalf("%s output must not fail the run: %v", tc.name, err)
		}
		if len(cands) != 0 || len(dropped) != 1 {
			t.Fatalf("%s candidate not dropped: cands=%+v dropped=%v", tc.name, cands, dropped)
		}
	}
	// Pure-digit tokens are issue numbers, counts, timeouts, or dates — never
	// SHAs — and home-relative references stay portable, so all of these commit.
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"timeout", `{"candidates":[{"type":"fact","statement":"Retry after 3000000 ms backoff.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"count", `{"candidates":[{"type":"fact","statement":"Flushed 1234567 rows in one pass.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"issue", `{"candidates":[{"type":"fact","statement":"See issue 12345678 for context.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"date", `{"candidates":[{"type":"fact","statement":"Decided on 20260918 to keep it.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"home", `{"candidates":[{"type":"fact","statement":"Check ~/notes/20260918-x.md for the thread.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"devnull", `{"candidates":[{"type":"workflow","statement":"Redirect the scan's stderr to /dev/null so only hits reach the log.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"devstdin", `{"candidates":[{"type":"workflow","statement":"Read the patch from /dev/stdin when no file is given.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
		{"devstdout", `{"candidates":[{"type":"workflow","statement":"Write the report to /dev/stdout so the caller can pipe it.","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`},
	} {
		out, err := ParseExtractionOutput([]byte(tc.raw), MaxRetirePerSessionRun)
		cands, dropped := outParts(out)
		if err != nil {
			t.Fatalf("%s output must not fail: %v", tc.name, err)
		}
		if len(cands) != 1 || len(dropped) != 0 {
			t.Fatalf("%s candidate wrongly dropped: cands=%+v dropped=%v", tc.name, cands, dropped)
		}
	}
	// Ordinary prose without hex tokens or absolute paths still commits, and
	// pure-letter words spelled with a-f letters are not SHAs.
	clean := `{"candidates":[{"type":"fact","statement":"The defaced card face must not be clamped again.","rationale":"why it matters","application":"Check internal/agent/foo.go before changing the render path.","summary":"A fact","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	out, err = ParseExtractionOutput([]byte(clean), MaxRetirePerSessionRun)
	cands, dropped = outParts(out)
	if err != nil {
		t.Fatalf("clean output must not fail: %v", err)
	}
	if len(cands) != 1 || len(dropped) != 0 {
		t.Fatalf("clean candidate wrongly dropped: cands=%+v dropped=%v", cands, dropped)
	}
}

// The machine-absolute path check must fire only on a path that starts a
// token: a URL or a project-relative path that happens to contain a prefix
// segment (cmd/run, pkg/data, docs/etc) is durable text, not a local layout.
func TestContainsMachineAbsolutePathTokenStart(t *testing.T) {
	absolute := []string{
		"/Users/tester/projects/chord",
		"see /tmp/x for the socket",
		"config=/home/user/.config/chord",
		"under /var/log/chord.log",
		"scratch at /dev/shm/chord-scratch",
		`log at C:\Users\tester\chord.log shows it`,
		"drive C:/Users/tester/x exists",
		`quoted "/etc/chord/config" stays local`,
		"before (/opt/app) after",
	}
	for _, text := range absolute {
		if !containsMachineAbsolutePath(text) {
			t.Fatalf("containsMachineAbsolutePath(%q) = false, want true", text)
		}
	}
	portable := []string{
		"https://example.com/x",
		"https://example.com/tmp/x",
		"cmd/run/main.go",
		"pkg/data/store.go",
		"scripts/dev/setup.sh",
		"docs/etc/x.md",
		"internal/agent/loop.go",
		"use /dev/null for discard",
		"read /dev/stdin and write /dev/stdout",
		"check ~/notes/x.md",
		"relative path tmp/x",
	}
	for _, text := range portable {
		if containsMachineAbsolutePath(text) {
			t.Fatalf("containsMachineAbsolutePath(%q) = true, want false", text)
		}
	}
}

func TestSanitizeTextRedactsSecrets(t *testing.T) {
	input := "Authorization: Bearer abc123\nx-api-key: def456\npassword=secret12345\nhttps://user:pass@example.com\n-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----"
	out := SanitizeText(input)
	if strings.Contains(out, "abc123") || strings.Contains(out, "def456") || strings.Contains(out, "secret12345") || strings.Contains(out, "user:pass") || strings.Contains(out, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("sanitizer leaked secrets: %q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("sanitizer produced no redaction marker: %q", out)
	}
	// The redaction keeps the readable prefix (scheme, header, or assignment)
	// so the surrounding text stays intact instead of being collapsed.
	for _, want := range []string{
		"Authorization: Bearer [REDACTED]",
		"x-api-key: [REDACTED]",
		"password=[REDACTED]",
		"https://[REDACTED]@example.com",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("sanitized text should keep prefix %q; got: %q", want, out)
		}
	}
	// Raw bare tokens and PEM blocks are high-risk and must be dropped.
	if !HighRisk("sk-abcdefghijklmnopqrstuvwx") {
		t.Fatal("bare secret token should be high-risk")
	}
	if HighRisk(SanitizeText("sk-abcdefghijklmnopqrstuvwx")) {
		t.Fatal("sanitized text should not be high-risk")
	}
	if !HighRisk("-----BEGIN OPENSSH PRIVATE KEY-----\nAAA\n-----END OPENSSH PRIVATE KEY-----") {
		t.Fatal("PEM block should be high-risk")
	}
}

// The bare-token shapes must require their separator: without it "sk"/"pk"/"rk"
// matched ordinary identifiers, redacting them out of the transcript sent to the
// model and dropping any candidate that mentioned one.
func TestBareSecretTokenDoesNotMatchOrdinaryIdentifiers(t *testing.T) {
	for _, benign := range []string{
		"skipLockedSessions",
		"skipped_records",
		"skeletonization",
		"pkg_resources_loader",
		"rkhunter_configuration",
	} {
		if HighRisk(benign) {
			t.Fatalf("HighRisk(%q) = true, want false", benign)
		}
		if got := SanitizeText(benign); got != benign {
			t.Fatalf("SanitizeText(%q) = %q, want it untouched", benign, got)
		}
	}
	// Real key shapes still match on both separators.
	for _, secret := range []string{
		"sk-abcdefghijklmnopqrstuvwx",
		"sk_live_abcdefghijklmnop",
		"AKIAIOSFODNN7EXAMPLE",
	} {
		if !HighRisk(secret) {
			t.Fatalf("HighRisk(%q) = false, want true", secret)
		}
	}
}

// A pitfall claims something about this codebase, so it has to be able to point
// at the code. Without a path it is generic advice or a note about the
// assistant's own output — the shape that filled the index with material already
// covered by project instructions. Other types legitimately carry no path.
func TestPitfallWithoutProjectPathsIsDropped(t *testing.T) {
	pitfall := `{"candidates":[{"type":"pitfall","statement":"Do not trust unsupported review summaries.","rationale":"why it matters","application":"how to apply it","summary":"Distrust unsupported summaries.","source_role":"assistant","confidence":"reported","outcome":"uncertain"}]}`
	out, err := ParseExtractionOutput([]byte(pitfall), MaxRetirePerSessionRun)
	if err != nil {
		t.Fatalf("a pathless pitfall must drop, not fail the run: %v", err)
	}
	if len(out.Candidates) != 0 || len(out.Dropped) != 1 {
		t.Fatalf("pathless pitfall: candidates=%+v dropped=%v", out.Candidates, out.Dropped)
	}
	// An environment fact has no code to point at and must still be accepted.
	fact := `{"candidates":[{"type":"fact","statement":"Reference checkouts live under the workspace directory.","rationale":"why it matters","application":"how to apply it","summary":"Reference checkout locations.","source_role":"user","confidence":"user_stated","outcome":"success"}]}`
	out, err = ParseExtractionOutput([]byte(fact), MaxRetirePerSessionRun)
	if err != nil {
		t.Fatalf("ParseExtractionOutput: %v", err)
	}
	if len(out.Candidates) != 1 || len(out.Dropped) != 0 {
		t.Fatalf("pathless fact should survive: candidates=%+v dropped=%v", out.Candidates, out.Dropped)
	}
}

// TestExtractionKeepsIdentifierBoundHexRuns pins the boundary the SHA drop
// must not cross: a hex run glued into a longer identifier by '-' or '_' is a
// UUID segment or this store's own <slug>--<hash> record id, both of which a
// later session still resolves. Dropping them would make a record that cites
// the one it supersedes unrepresentable. A bare SHA keeps dropping.
func TestExtractionKeepsIdentifierBoundHexRuns(t *testing.T) {
	candidate := func(statement string) string {
		return `{"candidates":[{"type":"fact","statement":"` + statement + `","rationale":"why","application":"how","summary":"s","source_role":"assistant","confidence":"reported","outcome":"success","project_paths":["internal/agent/foo.go"]}]}`
	}
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"uuid", `The export dataset is 550e8400-e29b-41d4-a716-446655440abc.`},
		{"recordid", `Supersedes retry-backoff--3f2a1b4c9d8e7f60.`},
		{"snake", `Keyed by build_id 3f2a1b4c9d8e7f60_generated.`},
	} {
		out, err := ParseExtractionOutput([]byte(candidate(tc.raw)), MaxRetirePerSessionRun)
		if err != nil {
			t.Fatalf("%s output must not fail: %v", tc.name, err)
		}
		cands, dropped := outParts(out)
		if len(cands) != 1 || len(dropped) != 0 {
			t.Fatalf("%s candidate wrongly dropped: cands=%+v dropped=%v", tc.name, cands, dropped)
		}
	}
	// Sentence punctuation is not a joiner: a bare SHA still drops.
	out, err := ParseExtractionOutput([]byte(candidate("Use e127cde6 for the rebase.")), MaxRetirePerSessionRun)
	if err != nil {
		t.Fatalf("bare SHA output must not fail: %v", err)
	}
	cands, dropped := outParts(out)
	if len(cands) != 0 || len(dropped) != 1 {
		t.Fatalf("bare SHA candidate not dropped: cands=%+v dropped=%v", cands, dropped)
	}
}
