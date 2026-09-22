package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/acpmux"
)

const (
	// acpSessionChildFlag selects the single-session mode the mux frontend
	// starts for each session. It is hidden: it is an internal entrypoint, not
	// something a client invokes.
	acpSessionChildFlag = "session-child"

	// acpMaxSessionsFlag caps how many sessions the frontend serves at once.
	acpMaxSessionsFlag = "max-sessions"

	// acpMuxLogStem is the frontend log's stem: its file is
	// chord-acp-mux-<pid>.log, the name the documentation gives it.
	acpMuxLogStem = "mux"

	acpSessionLogPrefix = "chord-acp-"
	acpSessionLogSuffix = ".log"

	// childTermGrace is how long a child gets after SIGTERM to finish
	// AppContext.Close. Stopping alone runs StopAllJobsForShutdown (up to
	// ~7s) plus MCP and LSP teardown; SIGKILL after a short wait would cut
	// that short. The stop signals the child's process group, but the jobs
	// (setsid) and MCP servers it spawned left that group, so only the
	// child's own teardown can stop them.
	childTermGrace = 15 * time.Second
)

// acpSessionChildIDPattern bounds what --session-child may contain. The id
// becomes a log file name, and the flag can be passed by hand, so it must not
// be able to name a path outside the log directory.
var acpSessionChildIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func validateACPSessionChildID(id string) error {
	if !acpSessionChildIDPattern.MatchString(id) {
		return fmt.Errorf("invalid --%s id %q: must match %s", acpSessionChildFlag, id, acpSessionChildIDPattern.String())
	}
	return nil
}

// acpSessionLogFileName is the log file of one session child.
func acpSessionLogFileName(id string) string {
	return acpSessionLogPrefix + id + acpSessionLogSuffix
}

// acpMuxLogFileName is the frontend's log file. It carries the process id
// because several frontends can share the logs directory (one per client
// connection, so one per project window): with a fixed name they would judge
// the same file's size and rotate over each other. A session child's log is
// named by its session id, which already embeds the frontend pid.
func acpMuxLogFileName() string {
	return acpSessionLogPrefix + acpMuxLogStem + "-" + strconv.Itoa(os.Getpid()) + acpSessionLogSuffix
}

// acpChildArgs drops any session-child flag the frontend was started with and
// appends the one for the new session. Everything else is inherited: a client
// that configured logging or config directories expects the child to see the
// same arguments.
func acpChildArgs(argv []string, id string) []string {
	args := make([]string, 0, len(argv)+1)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			// Everything past -- is positional, and the command takes none.
			break
		}
		if arg == "--"+acpSessionChildFlag {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--"+acpSessionChildFlag+"=") {
			continue
		}
		args = append(args, arg)
	}
	return append(args, "--"+acpSessionChildFlag+"="+id)
}

// spawnACPChild starts one session child: the same binary, the same arguments
// and environment, with the session in its own process so that its working
// directory, runtime and MCP servers cannot touch another session's.
func spawnACPChild(cwd, id string) (acpmux.Child, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate chord executable: %w", err)
	}
	cmd := exec.Command(exe, acpChildArgs(os.Args[1:], id)...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	return startChildProcess(cmd)
}

// startChildProcess wires the pipes a session child talks over and starts it.
//
// Only the child keeps the child ends: holding one of them here would hide EOF
// from the child's stdin, and holding the child's stdout would hide EOF from the
// frontend. The returned processChild owns the parent ends and releases them in
// Close.
func startChildProcess(cmd *exec.Cmd) (*processChild, error) {
	childStdin, parentStdin, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create session stdin pipe: %w", err)
	}
	parentStdout, childStdout, err := os.Pipe()
	if err != nil {
		childStdin.Close()
		parentStdin.Close()
		return nil, fmt.Errorf("create session stdout pipe: %w", err)
	}
	cmd.Stdin = childStdin
	cmd.Stdout = childStdout
	cmd.Stderr = os.Stderr
	configureChildProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		childStdin.Close()
		parentStdin.Close()
		parentStdout.Close()
		childStdout.Close()
		return nil, fmt.Errorf("start session child: %w", err)
	}
	// The child holds its own ends now. Keeping them open here would keep the
	// child from ever seeing EOF on stdin, and the frontend from seeing EOF on
	// the child's output.
	childStdin.Close()
	childStdout.Close()
	return &processChild{cmd: cmd, stdin: parentStdin, stdout: parentStdout, waited: make(chan struct{})}, nil
}

// processChild is a spawned `chord acp --session-child` process.
type processChild struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File

	waitOnce sync.Once
	waitErr  error
	waited   chan struct{}
}

func (c *processChild) Reader() io.Reader      { return c.stdout }
func (c *processChild) Writer() io.WriteCloser { return c.stdin }

// Wait reaps the process exactly once, however many callers ask: the frontend
// watches it for the session's lifetime and Stop waits on the same exit.
func (c *processChild) Wait() error {
	c.waitOnce.Do(func() {
		c.waitErr = c.cmd.Wait()
		close(c.waited)
	})
	<-c.waited
	return c.waitErr
}

// Stop ends the child. Closing its stdin is the graceful signal: the ACP
// connection ends there, and the child exits after tearing its runtime down.
// grace bounds that first wait; after SIGTERM the child gets childTermGrace,
// long enough for AppContext.Close to finish job and MCP teardown. Only a
// child that still ignores both gets SIGKILL.
//
// exited observes the reap, not the exit, so Stop reaps the process itself: the
// caller is not required to be watching it, and Wait is idempotent, so a
// concurrent watcher shares this one reap.
func (c *processChild) Stop(grace time.Duration) error {
	go func() { _ = c.Wait() }()
	_ = c.stdin.Close()
	if c.exited(grace) {
		return nil
	}
	if err := terminateChildProcess(c.cmd, false); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal session child: %w", err)
	}
	if c.exited(childTermGrace) {
		return nil
	}
	if err := terminateChildProcess(c.cmd, true); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill session child: %w", err)
	}
	<-c.waited
	return nil
}

// Close releases both pipe ends. Wait has already reaped the process, so the
// child cannot use them any more; without this the frontend would keep one pipe
// per closed session open until it exited.
func (c *processChild) Close() error {
	var errs []error
	for _, file := range []*os.File{c.stdin, c.stdout} {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// exited reports whether the process was reaped within grace.
func (c *processChild) exited(grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-c.waited:
		return true
	case <-timer.C:
		return false
	}
}
