package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/acpmux"
)

func TestValidateACPSessionChildIDAcceptsSafeIDs(t *testing.T) {
	for _, id := range []string{
		"chord-12345-1",
		"session",
		"A.b_c-d",
		strings.Repeat("a", 64),
	} {
		if err := validateACPSessionChildID(id); err != nil {
			t.Errorf("validateACPSessionChildID(%q) = %v, want nil", id, err)
		}
	}
}

func TestValidateACPSessionChildIDRejectsUnsafeIDs(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "empty", id: ""},
		{name: "relative path", id: "../etc/passwd"},
		{name: "absolute path", id: "/tmp/session"},
		{name: "separator", id: "a/b"},
		{name: "backslash", id: `a\b`},
		{name: "space", id: "a b"},
		{name: "newline", id: "a\nb"},
		{name: "null byte", id: "abc\x00"},
		{name: "too long", id: strings.Repeat("a", 65)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateACPSessionChildID(tt.id); err == nil {
				t.Fatalf("validateACPSessionChildID(%q) = nil, want error", tt.id)
			}
		})
	}
}

// The id becomes a log file name, so an accepted id must not be able to name a
// path outside the log directory even when the flag is passed by hand.
func TestACPSessionLogFileNameStaysInLogDirectory(t *testing.T) {
	for _, id := range []string{"chord-12345-1", "session", "..", "."} {
		if err := validateACPSessionChildID(id); err != nil {
			t.Fatalf("validateACPSessionChildID(%q) = %v, want nil", id, err)
		}
		name := acpSessionLogFileName(id)
		if filepath.Base(name) != name {
			t.Fatalf("acpSessionLogFileName(%q) = %q, want a single path element", id, name)
		}
		if !strings.HasPrefix(name, acpSessionLogPrefix) || !strings.HasSuffix(name, acpSessionLogSuffix) {
			t.Fatalf("acpSessionLogFileName(%q) = %q, want %s*%s", id, name, acpSessionLogPrefix, acpSessionLogSuffix)
		}
	}
}

// Two frontends can share the logs directory (one per client connection), so
// the frontend log must be unique per process: a fixed name would make them
// rotate over each other.
func TestACPMuxLogFileNameIsProcessScoped(t *testing.T) {
	name := acpMuxLogFileName()
	if filepath.Base(name) != name {
		t.Fatalf("acpMuxLogFileName() = %q, want a single path element", name)
	}
	// The literal name is what the documentation tells users to look for, so
	// pin it here instead of re-deriving it from the same constants the
	// implementation composes.
	want := "chord-acp-mux-" + strconv.Itoa(os.Getpid()) + ".log"
	if name != want {
		t.Fatalf("acpMuxLogFileName() = %q, want %q", name, want)
	}
}

func TestACPChildArgs(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "no arguments",
			want: []string{"--session-child=chord-1-1"},
		},
		{
			name: "keeps unrelated arguments",
			argv: []string{"--verbose", "--config", "/tmp/chord.toml"},
			want: []string{"--verbose", "--config", "/tmp/chord.toml", "--session-child=chord-1-1"},
		},
		{
			name: "replaces equals form",
			argv: []string{"--session-child=old", "--verbose"},
			want: []string{"--verbose", "--session-child=chord-1-1"},
		},
		{
			name: "replaces spaced form",
			argv: []string{"--session-child", "old", "--verbose"},
			want: []string{"--verbose", "--session-child=chord-1-1"},
		},
		{
			name: "drops repeated child flags",
			argv: []string{"--session-child=a", "--session-child", "b", "--verbose"},
			want: []string{"--verbose", "--session-child=chord-1-1"},
		},
		{
			name: "trailing child flag without a value",
			argv: []string{"--verbose", "--session-child"},
			want: []string{"--verbose", "--session-child=chord-1-1"},
		},
		{
			name: "stops at the argument terminator",
			argv: []string{"--verbose", "--", "--session-child=old"},
			want: []string{"--verbose", "--session-child=chord-1-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := acpChildArgs(tt.argv, "chord-1-1")
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Fatalf("acpChildArgs(%q) = %q, want %q", tt.argv, got, tt.want)
			}
		})
	}
}

func TestNewACPCmdSessionFlags(t *testing.T) {
	cmd := newACPCmd()

	child := cmd.Flags().Lookup(acpSessionChildFlag)
	if child == nil {
		t.Fatalf("--%s flag is not registered", acpSessionChildFlag)
	}
	if !child.Hidden {
		t.Fatalf("--%s must stay hidden: it is an internal entrypoint, not a client option", acpSessionChildFlag)
	}

	maxSessions := cmd.Flags().Lookup(acpMaxSessionsFlag)
	if maxSessions == nil {
		t.Fatalf("--%s flag is not registered", acpMaxSessionsFlag)
	}
	if maxSessions.DefValue != strconv.Itoa(acpmux.DefaultMaxSessions) {
		t.Fatalf("--%s default = %s, want %d", acpMaxSessionsFlag, maxSessions.DefValue, acpmux.DefaultMaxSessions)
	}

	// An invalid limit must be rejected before any frontend is started.
	if err := cmd.Flags().Set(acpMaxSessionsFlag, "0"); err != nil {
		t.Fatalf("set --%s: %v", acpMaxSessionsFlag, err)
	}
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatalf("--%s=0 was accepted, want an error", acpMaxSessionsFlag)
	}
}

// An explicitly empty --session-child is a mistake, not a request to serve
// clients: the flag is only ever set by the frontend, which always names the
// session it wants.
func TestNewACPCmdRejectsEmptySessionChild(t *testing.T) {
	cmd := newACPCmd()
	if err := cmd.Flags().Set(acpSessionChildFlag, ""); err != nil {
		t.Fatalf("set --%s: %v", acpSessionChildFlag, err)
	}
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatalf("--%s= was accepted, want an error", acpSessionChildFlag)
	}
}

// The frontend stops its session children on shutdown, so its own wait has to
// outlive a whole child stop sequence: the grace acpmux allows before SIGTERM,
// the window processChild then gives the child for AppContext.Close, and the
// kill and reap that follow. A shorter wait would let the frontend exit before
// the force-kill runs, leaving behind a child that ignored SIGTERM.
func TestACPMuxShutdownOutlivesChildStopSequence(t *testing.T) {
	stopSequence := acpmux.ChildStopGrace + childTermGrace
	if acpMuxShutdownTimeout <= stopSequence {
		t.Fatalf("acpMuxShutdownTimeout = %v, want more than the child stop sequence %v", acpMuxShutdownTimeout, stopSequence)
	}
}

// sessionChildHelperEnv marks the test binary as the stand-in session child of
// TestStartChildProcessReleasesPipes.
const sessionChildHelperEnv = "CHORD_TEST_SESSION_CHILD_HELPER"

// TestSessionChildHelperProcess is not a test: it is the child process
// TestStartChildProcessReleasesPipes starts, standing in for
// `chord acp --session-child=<id>`. Like a real session child it runs until its
// stdin reaches EOF, which is how the frontend asks it to exit.
func TestSessionChildHelperProcess(t *testing.T) {
	if os.Getenv(sessionChildHelperEnv) != "1" {
		t.Skip("helper process for TestStartChildProcessReleasesPipes")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// TestStartChildProcessReleasesPipes covers the fd ownership of a session
// child, which no fake child can: the child gets its own pipe ends, the frontend
// keeps the parent ends, and Close gives them back. A parent end that is never
// released is one fd leaked per closed session.
func TestStartChildProcessReleasesPipes(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSessionChildHelperProcess")
	cmd.Env = append(os.Environ(), sessionChildHelperEnv+"=1")
	child, err := startChildProcess(cmd)
	if err != nil {
		t.Fatalf("startChildProcess: %v", err)
	}

	// The child holds no copy of the frontend's ends, so closing the frontend's
	// stdin is what the child sees as EOF on its own.
	if err := child.Writer().Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("close the child's stdin: %v", err)
	}
	if err := child.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// Draining the output is what the connection does before the pipes are
	// released, so reading to EOF stands in for it here.
	if _, err := io.ReadAll(child.Reader()); err != nil {
		t.Fatalf("read the child's output: %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for name, file := range map[string]*os.File{"stdin": child.stdin, "stdout": child.stdout} {
		// Closing an already closed file reports os.ErrClosed, so this is how a
		// released fd shows itself.
		if err := file.Close(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("the child's %s pipe was not released: close returned %v", name, err)
		}
	}
}
