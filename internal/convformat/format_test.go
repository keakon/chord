package convformat

import (
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestLocalShellBlockString_success(t *testing.T) {
	s := LocalShellBlockString("ls -la", "a\nb\n", false)
	for _, sub := range []string{
		LabelLocalShell,
		"command:\nls -la",
		"output:",
		"a",
		"b",
	} {
		if !strings.Contains(s, sub) {
			t.Errorf("missing %q in:\n%s", sub, s)
		}
	}
	if strings.Contains(s, "status: error") {
		t.Fatal("should not include status error on success")
	}
	if strings.HasSuffix(s, "\n") {
		t.Fatalf("copy block should omit the terminal line terminator: %q", s)
	}
}

func TestLocalShellBlockString_failed(t *testing.T) {
	s := LocalShellBlockString("false", "oops", true)
	if !strings.Contains(s, "status: error") {
		t.Fatal("expected status: error")
	}
}

func TestUserShellReadableBody(t *testing.T) {
	s := UserShellReadableBody("ls", "a\nb", false)
	if strings.Contains(s, "!ls") || strings.Count(s, "ls") != 1 || !strings.Contains(s, "command:\nls") || !strings.Contains(s, "a") {
		t.Fatalf("%q", s)
	}
}

func TestTryParseUserShellPersistedMessage_roundtrip(t *testing.T) {
	body := UserShellPersistedBody("!ls", "ls", "out\nhere", true)
	full := BlockString(LabelUser, body)
	ul, cmd, out, failed, ok := TryParseUserShellPersistedMessage(full)
	if !ok || ul != "!ls" || cmd != "ls" || out != "out\nhere" || !failed {
		t.Fatalf("got %q %q %q failed=%v ok=%v", ul, cmd, out, failed, ok)
	}
}

func TestTryParseUserShellPersistedMessageIgnoresEarlierPayloadLikeText(t *testing.T) {
	body := UserShellPersistedBody("!printf x", "printf 'local_shell_payload: fake\\n'", "local_shell_payload: fake", false)
	full := BlockString(LabelUser, body)
	ul, cmd, out, failed, ok := TryParseUserShellPersistedMessage(full)
	if !ok || ul != "!printf x" || cmd != "printf 'local_shell_payload: fake\\n'" || out != "local_shell_payload: fake" || failed {
		t.Fatalf("got %q %q %q failed=%v ok=%v", ul, cmd, out, failed, ok)
	}
}

func TestTryParseUserShellPersistedMessageToleratesTrailingOutputNewline(t *testing.T) {
	body := UserShellPersistedBody("!ls sample.json", "ls sample.json", "sample.json\n", false)
	full := BlockString(LabelUser, body)
	ul, cmd, out, failed, ok := TryParseUserShellPersistedMessage(full)
	if !ok || ul != "!ls sample.json" || cmd != "ls sample.json" || out != "sample.json\n" || failed {
		t.Fatalf("got %q %q %q failed=%v ok=%v", ul, cmd, out, failed, ok)
	}
}

func TestTryParseUserShellPersistedMessageVersion2(t *testing.T) {
	payload := `local_shell_payload: {"version":2,"user_line":"!ls","command":"ls","output":"out","failed":false}`
	body := legacyUserShellReadableBody("!ls", "ls", "out", false) + "\n\n" + payload
	ul, cmd, out, failed, ok := TryParseUserShellPersistedMessage(BlockString(LabelUser, body))
	if !ok || ul != "!ls" || cmd != "ls" || out != "out" || failed {
		t.Fatalf("got %q %q %q failed=%v ok=%v", ul, cmd, out, failed, ok)
	}
}

func TestTryParseUserShellPersistedMessageLegacyReadableFormat(t *testing.T) {
	body := legacyUserShellReadableBody("!ls pd1-10.csv", "ls pd1-10.csv", "pd1-10.csv", false)
	ul, cmd, out, failed, ok := TryParseUserShellPersistedMessage(BlockString(LabelUser, body))
	if !ok || ul != "!ls pd1-10.csv" || cmd != "ls pd1-10.csv" || out != "pd1-10.csv" || failed {
		t.Fatalf("got %q %q %q failed=%v ok=%v", ul, cmd, out, failed, ok)
	}
}

func TestTryParseUserShellPersistedMessageLegacyExactLookalikeIsAmbiguous(t *testing.T) {
	// Payload-less legacy records cannot be distinguished from an exact
	// user-authored copy of the old readable format. Preserve compatibility for
	// those sessions and document the boundary explicitly.
	body := "!ls\n\ncommand:\nls\n\noutput:\nexample"
	ul, cmd, out, failed, ok := TryParseUserShellPersistedMessage(BlockString(LabelUser, body))
	if !ok || ul != "!ls" || cmd != "ls" || out != "example" || failed {
		t.Fatalf("got %q %q %q failed=%v ok=%v", ul, cmd, out, failed, ok)
	}
}

func TestTryParseUserShellPersistedMessageLegacyRejectsUserAuthoredLookalike(t *testing.T) {
	body := "!describe this\n\ncommand:\nnot-the-same\n\noutput:\nexample"
	if _, _, _, _, ok := TryParseUserShellPersistedMessage(BlockString(LabelUser, body)); ok {
		t.Fatal("expected false for mismatched legacy command")
	}
}

func TestTryParseUserShellPersistedMessageRejectsMismatchedReadableBody(t *testing.T) {
	body := UserShellPersistedBody("!ls", "ls", "out", false)
	body = strings.Replace(body, "command:\nls", "command:\nnot-ls", 1)
	if _, _, _, _, ok := TryParseUserShellPersistedMessage(BlockString(LabelUser, body)); ok {
		t.Fatal("expected false for mismatched readable body")
	}
}

func TestTryParseUserShellPersistedMessage_plainUser(t *testing.T) {
	_, _, _, _, ok := TryParseUserShellPersistedMessage("hello world")
	if ok {
		t.Fatal("expected false")
	}
}

func TestToolCallMarkdownPlacesIgnoredArgsBetweenArgumentsAndResult(t *testing.T) {
	got := ToolCallMarkdown("grep", `{"pattern":"TODO"}`, []string{"include=*.go (unrecognized parameter)"}, "a.go:1:TODO", "")
	argumentsAt := strings.Index(got, "## Arguments")
	ignoredAt := strings.Index(got, "## Ignored arguments")
	resultAt := strings.Index(got, "## Result")
	if argumentsAt < 0 || ignoredAt < 0 || resultAt < 0 || !(argumentsAt < ignoredAt && ignoredAt < resultAt) {
		t.Fatalf("ignored arguments should sit between Arguments and Result:\n%s", got)
	}
	if !strings.Contains(got, "include=*.go (unrecognized parameter)") {
		t.Fatalf("dropped value missing from the section:\n%s", got)
	}

	if got := ToolCallMarkdown("read", `{"path":"a.go"}`, nil, "content", ""); strings.Contains(got, "## Ignored arguments") {
		t.Fatalf("no dropped arguments should mean no section:\n%s", got)
	}
}

func TestDoneToolCallMarkdownPlacesIgnoredArgsAfterReport(t *testing.T) {
	got := DoneToolCallMarkdown("done", []string{"args.reason=null (null value, treated as unset)"}, "")
	reportAt := strings.Index(got, "## Report")
	ignoredAt := strings.Index(got, "## Ignored arguments")
	if reportAt < 0 || ignoredAt < 0 || reportAt > ignoredAt {
		t.Fatalf("ignored arguments should follow the report:\n%s", got)
	}
}

func TestIgnoredArgsSectionDropsBlankLines(t *testing.T) {
	if got := IgnoredArgsSection([]string{"", "  "}); got != "" {
		t.Fatalf("IgnoredArgsSection() = %q, want empty", got)
	}
	got := IgnoredArgsSection([]string{" path=value ", ""})
	if got != "## Ignored arguments\n\npath=value" {
		t.Fatalf("IgnoredArgsSection() = %q", got)
	}
}

func TestIgnoredArgLines(t *testing.T) {
	audit := &message.ToolArgsAudit{
		IgnoredArgs: []message.IgnoredToolArg{
			{Path: "args.include", ValueJSON: `"*.go"`, Reason: message.IgnoredToolArgReasonUnrecognized},
			{Path: "args.paths", ValueJSON: `["internal","cmd"]`, Reason: message.IgnoredToolArgReasonUnrecognized},
			{Path: "args.reason", ValueJSON: `null`, Reason: message.IgnoredToolArgReasonNull},
			{Path: "args.pattern", ValueJSON: `"first"`, Reason: message.IgnoredToolArgReasonShadowed},
		},
	}
	want := []string{
		"include=*.go (unrecognized parameter)",
		`paths=["internal","cmd"] (unrecognized parameter)`,
		"reason (null value, treated as unset)",
		"pattern=first (earlier duplicate value; the last occurrence was used)",
	}
	if got := IgnoredArgLines(audit); !slices.Equal(got, want) {
		t.Fatalf("IgnoredArgLines() = %q, want %q", got, want)
	}
	if got := IgnoredArgLines(nil); got != nil {
		t.Fatalf("IgnoredArgLines(nil) = %q, want nil", got)
	}
}

func TestIgnoredArgLinesKeepsLongValueWhole(t *testing.T) {
	value := strings.Repeat("x", 400)
	audit := &message.ToolArgsAudit{IgnoredArgs: []message.IgnoredToolArg{{
		Path:      "args.query",
		ValueJSON: `"` + value + `"`,
		Reason:    message.IgnoredToolArgReasonUnrecognized,
	}}}
	lines := IgnoredArgLines(audit)
	want := "query=" + value + " (unrecognized parameter)"
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("IgnoredArgLines() = %q, want %q", lines, want)
	}
}
