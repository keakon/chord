package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/keakon/golog"
	"github.com/keakon/golog/log"
	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/acpagent"
	"github.com/keakon/chord/internal/acpmux"
	"github.com/keakon/chord/internal/buildinfo"
	"github.com/keakon/chord/internal/config"
)

// acpMuxShutdownTimeout bounds how long the frontend waits for its session
// children to exit. It has to outlive a child's whole stop sequence — the wait
// acpmux allows before SIGTERM, the window processChild then gives the child
// for AppContext.Close, and the kill and reap that follow — or the frontend
// could exit while the force-kill is still pending, leaving behind a child that
// ignored SIGTERM. A child that outlives the frontend still exits on its own
// when its stdin reaches EOF.
const acpMuxShutdownTimeout = acpmux.ChildStopGrace + childTermGrace + time.Second

func newACPCmd() *cobra.Command {
	var (
		sessionChild string
		maxSessions  int
	)
	cmd := &cobra.Command{
		Use:   "acp",
		Short: "Serve the Agent Client Protocol over stdio",
		Long: "Serve the Agent Client Protocol (ACP) over stdio so ACP clients such as Zed\n" +
			"can drive Chord as their agent. One connection serves every session the client\n" +
			"opens: it sends initialize, then session/new with the working directory each\n" +
			"session is rooted at, and Chord starts one runtime per session. stdout carries\n" +
			"JSON-RPC only, everything else goes to the Chord log.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps := defaultACPDeps()
			// An explicitly empty --session-child is a mistake, not a request to
			// run the frontend: only the frontend ever sets that flag.
			if cmd.Flags().Changed(acpSessionChildFlag) {
				if err := validateACPSessionChildID(sessionChild); err != nil {
					return err
				}
				runtimeLogFileName = acpSessionLogFileName(sessionChild)
				return runACPWithDeps(cmd.Context(), deps)
			}
			if maxSessions <= 0 {
				return fmt.Errorf("--%s must be positive: got %d", acpMaxSessionsFlag, maxSessions)
			}
			runtimeLogFileName = acpMuxLogFileName()
			return runACPMux(cmd.Context(), deps, maxSessions)
		},
	}
	cmd.Flags().StringVar(&sessionChild, acpSessionChildFlag, "", "serve one ACP session as a child of the mux frontend")
	_ = cmd.Flags().MarkHidden(acpSessionChildFlag)
	cmd.Flags().IntVar(&maxSessions, acpMaxSessionsFlag, acpmux.DefaultMaxSessions, "maximum number of concurrent ACP sessions")
	return cmd
}

// acpBootstrap is the Chord runtime started for the ACP session, together with
// the application context that owns its lifecycle.
type acpBootstrap struct {
	ac         *AppContext
	rt         *Runtime
	sessionDir string
}

func (b *acpBootstrap) Close() {
	if b == nil {
		return
	}
	if b.rt != nil {
		b.rt.Close()
	}
	if b.ac != nil {
		b.ac.Close()
	}
}

type acpDeps struct {
	// logPath resolves the log file the stdout guard redirects fd 1 to.
	logPath func() (string, error)
	stdin   io.Reader
	version string
	// bootstrap starts the Chord runtime rooted at cwd.
	bootstrap func(cwd string) (*acpBootstrap, error)
	// spawn starts a session child process for the mux frontend.
	spawn func(cwd, id string) (acpmux.Child, error)
}

func defaultACPDeps() acpDeps {
	return acpDeps{
		logPath:   resolveACPLogPath,
		stdin:     os.Stdin,
		version:   buildinfo.Current().Version,
		bootstrap: bootstrapACPRuntime,
		spawn:     spawnACPChild,
	}
}

// resolveACPLogPath finds the log file Chord will use before initApp has created
// it, because the stdout guard must be in place before any library can print.
func resolveACPLogPath() (string, error) {
	globalCfg, err := config.LoadConfig()
	if err != nil {
		return "", wrapConfigLoadError("load config", err)
	}
	pathLocator, err := config.ResolvePathLocator(globalCfg, config.PathOptions{})
	if err != nil {
		return "", fmt.Errorf("resolve storage paths: %w", err)
	}
	return filepath.Join(pathLocator.LogsDir, runtimeLogFileName), nil
}

// bootstrapACPRuntime starts Chord in the session's working directory. ACP
// clients may spawn the agent anywhere, so the process changes into cwd (the
// authoritative session root) before anything resolves project paths.
func bootstrapACPRuntime(cwd string) (*acpBootstrap, error) {
	if err := os.Chdir(cwd); err != nil {
		return nil, fmt.Errorf("enter session directory: %w", err)
	}
	ac, err := initApp(false, "acp", sessionStartupOptions{})
	if err != nil {
		return nil, err
	}
	rt, err := createRuntime(ac)
	if err != nil {
		ac.Close()
		return nil, err
	}
	return &acpBootstrap{ac: ac, rt: rt, sessionDir: ac.SessionDir}, nil
}

// runACPWithDeps runs one ACP session in this process and returns when the
// client disconnects or the session is closed. It is what a session child of
// the mux frontend runs.
func runACPWithDeps(ctx context.Context, deps acpDeps) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if deps.logPath == nil {
		deps.logPath = resolveACPLogPath
	}
	if deps.bootstrap == nil {
		deps.bootstrap = bootstrapACPRuntime
	}
	if deps.stdin == nil {
		deps.stdin = os.Stdin
	}

	logPath, err := deps.logPath()
	if err != nil {
		return err
	}
	protocolOut, redirect, err := redirectProcessStdout(logPath)
	if err != nil {
		return fmt.Errorf("prepare acp stdio: %w", err)
	}
	defer func() {
		if closeErr := redirect.Close(); closeErr != nil {
			log.Warnf("failed to restore stdout error=%v", closeErr)
		}
	}()

	var (
		mu       sync.Mutex
		boot     *acpBootstrap
		sessions int
	)
	server := acpagent.New(acpagent.Options{
		Version: deps.version,
		Bootstrap: func(cwd string) (*acpagent.Runtime, error) {
			mu.Lock()
			defer mu.Unlock()
			if sessions > 0 {
				// A process that already bootstrapped a runtime cannot bootstrap
				// a second one. Server.started normally rejects the request
				// before it reaches this closure; this is the layer behind it.
				return nil, errors.New("acp session runtime already started for this process")
			}
			started, err := deps.bootstrap(cwd)
			if err != nil {
				return nil, err
			}
			sessions++
			boot = started
			if started.ac != nil && started.ac.LogWriter != nil {
				if rebindErr := redirect.Rebind(started.ac.LogWriter.CurrentFile()); rebindErr != nil {
					log.Warnf("failed to rebind stdout to the rotating log error=%v", rebindErr)
				}
				// Keep fd 1 on the live log after the writer rotates or reopens
				// it, the same way the stderr redirect is kept in sync.
				started.ac.LogWriter.SetStdoutRedirect(redirect)
			}
			return &acpagent.Runtime{Backend: started.rt.Agent, SessionDir: started.sessionDir}, nil
		},
	})
	conn := acp.NewAgentSideConnection(server, protocolOut, deps.stdin)
	server.Bind(conn)
	log.Infof("acp agent ready version=%v", deps.version)

	log.Infof("acp agent stopping reason=%v", waitForACPStop(ctx, conn, server.SessionClosed()))

	mu.Lock()
	started := boot
	mu.Unlock()
	started.Close()

	if server.SessionID() != "" {
		log.Infof("acp agent stopped cwd=%v", server.Cwd())
	}
	return nil
}

// waitForACPStop blocks until this session child should stop and names why.
//
// A session/close is not enough by itself. The ACP connection writes the close
// answer only after its handler returns, so tearing the runtime down as soon as
// the session closes could beat the answer to the pipe, and the client would
// see a broken connection instead of a clean close. Nothing inside this process
// can observe that write, but the frontend can: it answers the client only once
// this child has answered, and then stops the child by closing its stdin, which
// ends this connection. Waiting for the connection is exact, where a sleep was
// only long enough on an idle machine.
func waitForACPStop(ctx context.Context, conn *acp.AgentSideConnection, sessionClosed <-chan struct{}) string {
	reason := "client-disconnected"
	for {
		select {
		case <-ctx.Done():
			return "shutdown"
		case <-conn.Done():
			return reason
		case <-sessionClosed:
			log.Infof("acp session closed; waiting for the client to release the connection")
			reason = "session-closed"
			sessionClosed = nil
		}
	}
}

// runACPMux serves every session the client opens from one process, one child
// process per session. The frontend never starts a runtime itself: it routes
// the client's traffic to the child that owns each session.
func runACPMux(ctx context.Context, deps acpDeps, maxSessions int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if deps.logPath == nil {
		deps.logPath = resolveACPLogPath
	}
	if deps.stdin == nil {
		deps.stdin = os.Stdin
	}
	if deps.spawn == nil {
		return errors.New("acp mux requires a session child spawner")
	}

	logPath, err := deps.logPath()
	if err != nil {
		return err
	}
	protocolOut, redirect, err := redirectProcessStdout(logPath)
	if err != nil {
		return fmt.Errorf("prepare acp stdio: %w", err)
	}
	defer func() {
		if closeErr := redirect.Close(); closeErr != nil {
			log.Warnf("failed to restore stdout error=%v", closeErr)
		}
	}()
	logWriter, err := startACPMuxLog(logPath, redirect)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := logWriter.Close(); closeErr != nil {
			log.Warnf("failed to close acp mux log error=%v", closeErr)
		}
	}()

	proxy := acpmux.New(acpmux.Options{
		Version:     deps.version,
		Spawn:       deps.spawn,
		MaxSessions: maxSessions,
	})
	conn := acp.NewAgentSideConnection(proxy, protocolOut, deps.stdin)
	proxy.Bind(conn)
	log.Infof("acp mux ready version=%v max_sessions=%v", deps.version, maxSessions)

	select {
	case <-ctx.Done():
		log.Infof("acp mux stopping reason=shutdown")
	case <-conn.Done():
		log.Infof("acp mux stopping reason=client-disconnected")
	}
	proxy.Shutdown(acpMuxShutdownTimeout)
	return nil
}

// startACPMuxLog points the frontend's logger and fd 1 at its own log file. The
// frontend never calls initApp, which is where every other entrypoint sets its
// logger up, so it opens its file here.
func startACPMuxLog(logPath string, redirect *stdoutRedirect) (*rotatingLogFile, error) {
	writer, err := newRotatingLogFile(logPath)
	if err != nil {
		return nil, fmt.Errorf("open acp mux log: %w", err)
	}
	pwd, _ := os.Getwd()
	setDefaultLogger(newGologLoggerWithContext(writer, golog.InfoLevel, logContext{PWD: pwd, PID: os.Getpid()}))
	if redirect != nil {
		if rebindErr := redirect.Rebind(writer.CurrentFile()); rebindErr != nil {
			log.Warnf("failed to rebind stdout to the acp mux log error=%v", rebindErr)
		}
		writer.SetStdoutRedirect(redirect)
	}
	return writer, nil
}
