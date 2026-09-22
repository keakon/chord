// Package acpmux serves several ACP sessions from one process by giving each
// session its own `chord acp --session-child=<id>` child process.
//
// The frontend is a router, not a runtime: it answers initialize itself, mints
// a stable id per client session, and forwards everything else to the child
// that owns the session. A child is the existing single-session agent, so every
// session keeps its own working directory, MCP servers, model client and
// journal — at the cost of one full runtime per open session, including its MCP
// child processes.
package acpmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/acpagent"
)

const (
	// DefaultMaxSessions caps concurrent sessions when Options.MaxSessions is
	// not set.
	DefaultMaxSessions = 8

	// ChildStopGrace is how long a child gets to exit on its own after its
	// stdin closes, before Child.Stop escalates to a terminate signal. A real
	// process child then gets a longer post-signal window for AppContext.Close
	// (job/MCP/LSP teardown) before it is force-killed, so a caller whose own
	// timeout has to outlive a whole stop sequence sizes it with this grace.
	ChildStopGrace = 5 * time.Second

	// childCloseTimeout bounds the wait for a child's answer to session/close.
	// It sits above the child's own settle window so the child can always
	// report a cancelled turn before the frontend gives up on it.
	childCloseTimeout = 60 * time.Second
)

// Child is one session child process: a single-session `chord acp` reached
// through pipes.
type Child interface {
	// Reader yields the child's protocol output.
	Reader() io.Reader
	// Writer takes the child's protocol input. Closing it ends the child's
	// input stream, which is what asks the child to shut down.
	Writer() io.WriteCloser
	// Wait blocks until the child process exits and reports how it ended.
	Wait() error
	// Stop asks the child to exit and escalates when it does not: it closes
	// stdin, waits up to grace, asks the child to terminate, waits long enough
	// for a real child's runtime teardown, then force-kills. It reaps the
	// process itself, so it returns once the process is gone even when nobody
	// is watching it.
	Stop(grace time.Duration) error
	// Close releases the pipes the child was reached through. Wait has to
	// report the process gone first and the caller has to drain the output, so
	// nothing the child wrote is lost; without it a closed session would hold
	// its pipes open for the life of the frontend.
	Close() error
}

// Spawn starts a child for cwd. id names the session and is unique for the
// lifetime of the frontend.
type Spawn func(cwd, id string) (Child, error)

// Options configures a Proxy.
type Options struct {
	// Version is reported to clients as the agent version.
	Version string
	// Spawn starts a session child. It must not be nil.
	Spawn Spawn
	// MaxSessions caps how many sessions may exist at once: one runtime with
	// its own MCP processes each. Values <= 0 mean DefaultMaxSessions.
	MaxSessions int
}

// Proxy serves many ACP sessions over one client connection.
type Proxy struct {
	opts Options

	mu       sync.Mutex
	conn     *acp.AgentSideConnection
	initReq  *acp.InitializeRequest
	sessions map[acp.SessionId]*session
	closed   bool
	// wg counts reserved sessions that may still have a live child process.
	wg sync.WaitGroup
}

// session is one client session and the child process serving it.
//
// clientID and cwd are written before the session is published and never
// change. Everything else is guarded by Proxy.mu.
type session struct {
	clientID acp.SessionId
	cwd      string

	child   Child
	conn    *acp.ClientSideConnection
	agentID acp.SessionId
	// chordSession is the Chord session directory the child reported, which is
	// what `chord resume` and a bug report take.
	chordSession string
	closing      bool
	// stopRequested records that the session's child has to stop. It is set
	// even while the child is still being spawned, so a session that is
	// stopped during startup never publishes a child nobody would reap.
	stopRequested bool
	// stopped records that Stop ran on the child. A child is stopped at most
	// once, so the close path, a failed startup and shutdown can all ask for
	// the same process to exit.
	stopped bool
}

var _ acp.Agent = (*Proxy)(nil)

// New creates a Proxy. Bind must be called with the connection built from it
// before any client traffic arrives.
func New(opts Options) *Proxy {
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = DefaultMaxSessions
	}
	return &Proxy{opts: opts, sessions: make(map[acp.SessionId]*session)}
}

// Bind attaches the connection the proxy forwards child traffic to.
func (p *Proxy) Bind(conn *acp.AgentSideConnection) {
	p.mu.Lock()
	p.conn = conn
	p.mu.Unlock()
}

// Shutdown stops every child and waits up to timeout for them to exit. A child
// that outlives the frontend still exits on its own: its stdin reaches EOF when
// the frontend process goes away.
func (p *Proxy) Shutdown(timeout time.Duration) {
	p.mu.Lock()
	p.closed = true
	sessions := make([]*session, 0, len(p.sessions))
	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}
	p.mu.Unlock()
	if len(sessions) == 0 {
		return
	}
	for _, s := range sessions {
		go p.stopChild(s)
	}
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		log.Infof("acp mux stopped session children sessions=%v", len(sessions))
	case <-timer.C:
		log.Warnf("acp mux shutdown timed out waiting for session children sessions=%v", len(sessions))
	}
}

// clientConn is the connection child traffic is forwarded to.
func (p *Proxy) clientConn() (*acp.AgentSideConnection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil, acp.NewInternalError(map[string]any{"error": "no client connection"})
	}
	return p.conn, nil
}

// reserve registers a placeholder session and reports the initialize request
// every child has to be initialized with.
//
// The capability check, the session limit and the registration share one lock
// on purpose: the SDK dispatches each inbound request in its own goroutine, so
// two concurrent session/new calls would otherwise both pass the check and both
// take a slot. Registering before the child exists also means the session is
// routable before anything is forwarded to it.
func (p *Proxy) reserve(cwd string) (*acp.InitializeRequest, *session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, acp.NewInvalidParams(map[string]any{"error": "connection is shutting down"})
	}
	if p.initReq == nil {
		return nil, nil, acp.NewInvalidParams(map[string]any{"error": "initialize must be called before session/new"})
	}
	if len(p.sessions) >= p.opts.MaxSessions {
		return nil, nil, acp.NewInvalidParams(map[string]any{
			"maxSessions": p.opts.MaxSessions,
			"error":       fmt.Sprintf("session limit reached: close a session before opening more than %d", p.opts.MaxSessions),
		})
	}
	req := *p.initReq
	s := &session{clientID: acpagent.NewSessionID(), cwd: cwd}
	p.sessions[s.clientID] = s
	p.wg.Add(1)
	return &req, s, nil
}

// sessionConn resolves a client session id to its child connection. Sessions
// that are closing or whose child is not up answer with an error instead.
func (p *Proxy) sessionConn(id acp.SessionId) (*session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[id]
	if s == nil {
		return nil, unknownSession(id)
	}
	if s.closing {
		return nil, acp.NewInvalidParams(map[string]any{"sessionId": string(id), "error": "session is closing"})
	}
	if s.conn == nil || s.agentID == "" {
		return nil, acp.NewInvalidParams(map[string]any{"sessionId": string(id), "error": "session is still starting"})
	}
	return s, nil
}

// drop removes a session from the table without waiting for its child.
func (p *Proxy) drop(s *session) {
	p.mu.Lock()
	if p.sessions[s.clientID] == s {
		delete(p.sessions, s.clientID)
	}
	p.mu.Unlock()
}

// attachChild publishes the child a session was spawned for, unless a stop was
// requested while it was starting. It reports whether the child is routable.
//
// The child is recorded either way, because stopChild can only reach it through
// this session: a child that was not published has no other owner to reap it.
func (p *Proxy) attachChild(s *session, child Child, conn *acp.ClientSideConnection) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	s.child = child
	if s.stopRequested {
		return false
	}
	s.conn = conn
	return true
}

// stopChild stops a child at most once, so the close path, a failed startup and
// shutdown can all ask for the same process to exit. A session whose child is
// still being spawned only records the request: NewSession sees it, refuses to
// publish that child, and stops it instead.
func (p *Proxy) stopChild(s *session) error {
	p.mu.Lock()
	s.stopRequested = true
	child := s.child
	if child == nil || s.stopped {
		p.mu.Unlock()
		return nil
	}
	s.stopped = true
	p.mu.Unlock()
	if err := child.Stop(ChildStopGrace); err != nil {
		return fmt.Errorf("stop session child: %w", err)
	}
	return nil
}

// stopChildAsync reaps a child in the background: the client already has its
// answer, and watch frees the slot once the process is gone.
func (p *Proxy) stopChildAsync(s *session) {
	go func() {
		if err := p.stopChild(s); err != nil {
			log.Warnf("acp mux failed to stop session child session_id=%v error=%v", s.clientID, err)
		}
	}()
}

// watch retires a session once its child exits, and releases the pipes the
// session held.
func (p *Proxy) watch(s *session, child Child, conn *acp.ClientSideConnection) {
	waitErr := child.Wait()
	p.mu.Lock()
	delete(p.sessions, s.clientID)
	p.mu.Unlock()
	p.wg.Done()
	// The connection reads the child's output until EOF, so waiting for it here
	// releases the pipes only after everything the child wrote has been
	// consumed. Closing them earlier would drop the tail of a crash, and never
	// closing them would hold one pipe per closed session for the life of the
	// frontend.
	<-conn.Done()
	if err := child.Close(); err != nil {
		log.Warnf("acp mux failed to release session child pipes session_id=%v error=%v", s.clientID, err)
	}
	if waitErr != nil {
		log.Warnf("acp mux session child failed session_id=%v error=%v", s.clientID, waitErr)
		return
	}
	log.Infof("acp mux session child exited session_id=%v", s.clientID)
}

// chordSessionName reads the Chord session directory name out of the metadata a
// child reported, so session logs can be matched with `chord resume` and bug
// reports.
func chordSessionName(meta map[string]any) string {
	chordMeta, ok := meta["chord"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := chordMeta["sessionId"].(string)
	return name
}

// childError translates a failure reported by a child into the error the client
// sees: a JSON-RPC error from the child passes through with its code, message
// and data, and anything else (a dead child, a cancelled request) becomes an
// internal error carrying the transport text.
func childError(err error) error {
	if err == nil {
		return nil
	}
	if reqErr, ok := errors.AsType[*acp.RequestError](err); ok {
		return reqErr
	}
	return acp.NewInternalError(map[string]any{"error": err.Error()})
}

// unknownSession is the error for an id no live session owns. Cancel is a
// notification and ignores unknown ids; requests report them.
func unknownSession(id acp.SessionId) error {
	return acp.NewInvalidParams(map[string]any{"sessionId": string(id), "error": "unknown session"})
}

// contextWithoutCancel keeps the values of ctx but drops its cancellation, so a
// forwarded call is decided by the child rather than by the client's own
// cancellation. A cancelled turn still has to reach its answer — and the
// updates that precede it — through the child.
func contextWithoutCancel(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}
