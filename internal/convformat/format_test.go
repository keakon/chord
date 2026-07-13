package convformat

import (
	"strings"
	"testing"
)

func TestLocalShellBlockString_success(t *testing.T) {
	s := LocalShellBlockString("ls -la", "a\nb", false)
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
