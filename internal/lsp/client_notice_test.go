package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/logtest"
)

// TestClientRegistersServerNoticeHandlers guards the transport contract that
// made server notices invisible: without a registered handler the transport
// drops the notification without logging or erroring, so "the server serves
// this workspace" and "the server refuses to work here" looked identical.
func TestClientRegistersServerNoticeHandlers(t *testing.T) {
	fake := &fakePowernapClient{}
	client := &Client{client: fake, name: "typescript", cwd: "/tmp/workspace"}
	client.registerHandlers()

	for _, method := range []string{"window/showMessage", "window/logMessage", "$/typescriptVersion"} {
		if _, ok := fake.registeredNotifies[method]; !ok {
			t.Fatalf("notification handler %q not registered", method)
		}
	}
}

func TestClientLogsServerNotices(t *testing.T) {
	var buf bytes.Buffer
	log.SetDefaultLogger(logtest.NewLogger(&buf, golog.DebugLevel))
	t.Cleanup(func() { log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel)) })

	fake := &fakePowernapClient{}
	client := &Client{client: fake, name: "typescript", cwd: "/tmp/workspace"}
	client.registerHandlers()
	dispatch := func(method, params string) {
		fake.registeredNotifies[method](context.Background(), method, json.RawMessage(params))
	}

	dispatch("window/showMessage", `{"type":2,"message":"The TypeScript of the workspace provides no tsserver.js. Using 5.9.3 instead"}`)
	dispatch("window/logMessage", `{"type":4,"message":"Resolved client capabilities"}`)
	dispatch("$/typescriptVersion", `{"version":"5.9.3","source":"user-setting"}`)

	got := buf.String()
	for _, want := range []string{
		"[W ",
		"kind=showMessage",
		"provides no tsserver.js",
		"kind=logMessage",
		"[D ",
		"Resolved client capabilities",
		"kind=typescriptVersion",
		"[I ",
		"5.9.3 (user-setting)",
		"name=typescript",
		"root=/tmp/workspace",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("server notice log missing %q in %q", want, got)
		}
	}
}

// TestClientServerNoticeDedupAndBound guards the two log-hygiene rules: a
// server that resends the same notice must not log it twice, and one oversized
// notice must not flood the log.
func TestClientServerNoticeDedupAndBound(t *testing.T) {
	var buf bytes.Buffer
	log.SetDefaultLogger(logtest.NewLogger(&buf, golog.DebugLevel))
	t.Cleanup(func() { log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel)) })

	fake := &fakePowernapClient{}
	client := &Client{client: fake, name: "typescript", cwd: "/tmp/workspace"}
	client.registerHandlers()
	repeated := `{"type":2,"message":"repeated version warning"}`
	huge := `{"type":4,"message":"` + strings.Repeat("x", 4000) + `"}`

	fake.registeredNotifies["window/showMessage"](context.Background(), "window/showMessage", json.RawMessage(repeated))
	fake.registeredNotifies["window/showMessage"](context.Background(), "window/showMessage", json.RawMessage(repeated))
	fake.registeredNotifies["window/logMessage"](context.Background(), "window/logMessage", json.RawMessage(huge))

	got := buf.String()
	if count := strings.Count(got, "repeated version warning"); count != 1 {
		t.Fatalf("identical notices logged %d times, want 1: %q", count, got)
	}
	if strings.Contains(got, strings.Repeat("x", noticeLogMaxChars+1)) {
		t.Fatalf("oversized notice was not truncated to %d chars", noticeLogMaxChars)
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("truncated notice should be marked with an ellipsis: %q", got)
	}
}

// TestClientServerNoticeTruncationKeepsUTF8 guards the log-text invariant that
// truncation cuts on a rune boundary: servers do send non-ASCII messages, and a
// byte slice through a multi-byte character would put invalid UTF-8 in the log.
func TestClientServerNoticeTruncationKeepsUTF8(t *testing.T) {
	var buf bytes.Buffer
	log.SetDefaultLogger(logtest.NewLogger(&buf, golog.DebugLevel))
	t.Cleanup(func() { log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel)) })

	fake := &fakePowernapClient{}
	client := &Client{client: fake, name: "typescript", cwd: "/tmp/workspace"}
	client.registerHandlers()

	head := strings.Repeat("x", noticeLogMaxChars-2)
	params, err := json.Marshal(map[string]any{"type": 4, "message": head + "界" + strings.Repeat("y", 100)})
	if err != nil {
		t.Fatal(err)
	}
	fake.registeredNotifies["window/logMessage"](context.Background(), "window/logMessage", params)

	got := buf.String()
	if !utf8.ValidString(got) {
		t.Fatalf("truncated notice is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, head) {
		t.Fatalf("truncation dropped the notice head: %q", got)
	}
}

// TestClientServerNoticeDedupSetIsBounded guards that a server emitting an
// unbounded stream of distinct notices cannot grow the per-client dedup set
// (and with it Chord's memory) without limit.
func TestClientServerNoticeDedupSetIsBounded(t *testing.T) {
	var buf bytes.Buffer
	log.SetDefaultLogger(logtest.NewLogger(&buf, golog.DebugLevel))
	t.Cleanup(func() { log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel)) })

	fake := &fakePowernapClient{}
	client := &Client{client: fake, name: "typescript", cwd: "/tmp/workspace"}
	client.registerHandlers()

	for i := range noticeDedupMaxEntries + 10 {
		params, err := json.Marshal(map[string]any{"type": 4, "message": fmt.Sprintf("notice %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		fake.registeredNotifies["window/logMessage"](context.Background(), "window/logMessage", params)
	}

	client.noticeMu.Lock()
	entries := len(client.noticesSeen)
	client.noticeMu.Unlock()
	if entries > noticeDedupMaxEntries {
		t.Fatalf("dedup set holds %d entries, want at most %d", entries, noticeDedupMaxEntries)
	}
}
