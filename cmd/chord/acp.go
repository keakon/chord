package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	acp "github.com/coder/acp-go-sdk"
	"github.com/keakon/golog/log"
	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/acpagent"
	"github.com/keakon/chord/internal/buildinfo"
	"github.com/keakon/chord/internal/config"
)

func newACPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "acp",
		Short: "Serve the Agent Client Protocol over stdio",
		Long: "Serve the Agent Client Protocol (ACP) over stdio so ACP clients such as Zed\n" +
			"can drive Chord as their agent. The client sends initialize followed by\n" +
			"session/new with the working directory it wants the session rooted at; stdout\n" +
			"carries JSON-RPC only, everything else goes to the Chord log.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runACPWithDeps(cmd.Context(), defaultACPDeps())
		},
	}
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
}

func defaultACPDeps() acpDeps {
	return acpDeps{
		logPath:   resolveACPLogPath,
		stdin:     os.Stdin,
		version:   buildinfo.Current().Version,
		bootstrap: bootstrapACPRuntime,
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
	return filepath.Join(pathLocator.LogsDir, chordLogFileName), nil
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

// runACPWithDeps runs the ACP stdio agent until the client disconnects.
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
				return nil, errors.New("chord acp serves one session per process")
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

	select {
	case <-ctx.Done():
		log.Infof("acp agent stopping reason=shutdown")
	case <-conn.Done():
		log.Infof("acp agent stopping reason=client-disconnected")
	}

	mu.Lock()
	started := boot
	mu.Unlock()
	started.Close()

	if server.SessionID() != "" {
		log.Infof("acp agent stopped cwd=%v", server.Cwd())
	}
	return nil
}
