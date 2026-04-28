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
	s := UserShellReadableBody("!ls", "ls", "a\nb", false)
	if !strings.Contains(s, "!ls") || !strings.Contains(s, "command:\nls") || !strings.Contains(s, "a") {
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
