// Package acpagent adapts Chord's MainAgent to the Agent Client Protocol.
//
// Every ACP type stays inside this package: internal/agent and internal/tools
// keep their existing event channel and message APIs, and this adapter is just
// another consumer of that channel (the TUI and `chord headless` are the
// others). One process serves exactly one ACP session, which mirrors Chord's
// single-active-session model; the mux frontend in internal/acpmux serves
// several clients by running one such process per session.
package acpagent

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// Backend is the Chord surface the adapter drives. It is satisfied by
// *agent.MainAgent and deliberately exposes no ACP-shaped method.
type Backend interface {
	SendUserMessageWithParts(parts []message.ContentPart)
	Events() <-chan agent.AgentEvent
	CancelCurrentTurn() bool
}

// Runtime is a Chord runtime bootstrapped for one ACP session.
type Runtime struct {
	Backend Backend
	// SessionDir is the Chord session directory the runtime was bootstrapped
	// into. It is reported to the client as session metadata, never as the ACP
	// session id: the ACP id has to stay stable for as long as the client holds
	// the session, while the Chord directory moves with /resume, handoff and
	// fork.
	SessionDir string
}

// Options configures a Server.
type Options struct {
	// Version is reported to clients as the agent version in initialize.
	Version string
	// Bootstrap starts the Chord runtime rooted at cwd. It runs once per
	// process, on the first session/new, because the working directory only
	// becomes authoritative with that request.
	Bootstrap func(cwd string) (*Runtime, error)
}

// settleTimeout bounds how long a turn may hold the prompt response open once
// the turn itself is over: a cancelled turn needs the window to flush synthetic
// terminal tool results, and any turn waits at most this long for its last
// stream to report that its buffered deltas are out. An expired wait costs
// correctness only for the next prompt, so it is logged, not fatal. It is a var
// so tests can shorten it.
var settleTimeout = 30 * time.Second

// Server implements acp.Agent on top of one Chord MainAgent.
type Server struct {
	opts Options

	// promptMu keeps turn bookkeeping in order. Chord runs one turn at a time and
	// a prompt arriving while one is running supersedes it instead of queueing
	// behind it: the SDK cancels the running prompt's context before it calls
	// Prompt for the new one, so the older call answers cancelled and releases
	// this lock before the new turn starts.
	promptMu sync.Mutex

	// startMu orders the last steps of starting a prompt — the closed check, the
	// waiter install and the hand-off to the backend — against session/close,
	// which marks the session terminal and picks up the waiter under this lock
	// too. Without it a close could land between the check and the install, see
	// no waiter to cancel, and still let the prompt hand a turn to the backend
	// after the client was told the session had ended. It cannot be s.mu: the
	// hand-off may wait on agent event capacity, and the event pump needs s.mu
	// to apply turn effects.
	startMu sync.Mutex

	mu      sync.Mutex
	conn    *acp.AgentSideConnection
	rt      *Runtime
	session acp.SessionId
	cwd     string
	waiter  *turnWaiter
	started bool
	closed  bool

	// sessionClosed is closed once session/close has been handled and logged.
	// The entrypoint watches it to tear the runtime down and exit, which is
	// what frees the session's resources.
	sessionClosed chan struct{}
	closeOnce     sync.Once
}

var _ acp.Agent = (*Server)(nil)

// New creates a Server. Bind must be called with the connection built from it
// before any client traffic arrives.
func New(opts Options) *Server {
	return &Server{opts: opts, sessionClosed: make(chan struct{})}
}

// SessionClosed reports that the session was closed and this process has
// nothing left to serve. The entrypoint gives the close response a moment to
// reach the pipe, then shuts the runtime down and exits.
func (s *Server) SessionClosed() <-chan struct{} {
	return s.sessionClosed
}

// Bind attaches the connection the server sends session updates through.
func (s *Server) Bind(conn *acp.AgentSideConnection) {
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
}

// SessionID returns the ACP session id, if a session has been created.
func (s *Server) SessionID() acp.SessionId {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session
}

// Cwd returns the working directory the session was created with.
func (s *Server) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func (s *Server) connection() (*acp.AgentSideConnection, acp.SessionId) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn, s.session
}

func (s *Server) currentWaiter() *turnWaiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiter
}

func (s *Server) setWaiter(w *turnWaiter) {
	s.mu.Lock()
	s.waiter = w
	s.mu.Unlock()
}

func (s *Server) clearWaiter(w *turnWaiter) {
	s.mu.Lock()
	if s.waiter == w {
		s.waiter = nil
	}
	s.mu.Unlock()
}

// sessionFor resolves the runtime for an incoming request's session id.
func (s *Server) sessionFor(id acp.SessionId) (*Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rt == nil || s.session == "" {
		return nil, acp.NewInvalidParams(map[string]any{"error": "no session: call session/new first"})
	}
	if s.closed {
		return nil, acp.NewInvalidParams(map[string]any{"error": "session is closed"})
	}
	if id != s.session {
		return nil, acp.NewInvalidParams(map[string]any{"sessionId": string(id), "error": "unknown session"})
	}
	return s.rt, nil
}

// Initialize negotiates protocol capabilities. Chord exposes no authentication,
// no session loading and no client-side filesystem or terminal delegation, so
// only the negotiated protocol version and image prompts are advertised.
func (s *Server) Initialize(_ context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	LogInitialize(req)
	return InitializeResponse(s.opts.Version), nil
}

// Authenticate is unreachable while no auth methods are advertised.
func (s *Server) Authenticate(_ context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
}

// Logout is not supported; Chord keeps no ACP-visible credentials.
func (s *Server) Logout(_ context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

// chordSessionName is the name of the Chord session directory this runtime was
// bootstrapped into. It is the id `chord resume` and a bug report take, which is
// why the client gets it: unlike the ACP session id, the directory changes when
// the session is resumed, handed off or forked.
func chordSessionName(rt *Runtime) string {
	if rt == nil {
		return ""
	}
	dir := strings.TrimSpace(rt.SessionDir)
	if dir == "" {
		return ""
	}
	return filepath.Base(dir)
}

// sessionMeta carries the Chord session identity alongside the ACP session id.
func sessionMeta(rt *Runtime) map[string]any {
	name := chordSessionName(rt)
	if name == "" {
		return nil
	}
	return map[string]any{"chord": map[string]any{"sessionId": name}}
}

// mcpServerNames lists what the client asked Chord to connect to. Only names are
// recorded: a server entry also carries environment variables and headers, which
// have no business in a log file.
func mcpServerNames(servers []acp.McpServer) []string {
	names := make([]string, 0, len(servers))
	for _, server := range servers {
		switch {
		case server.Stdio != nil:
			names = append(names, server.Stdio.Name)
		case server.Http != nil:
			names = append(names, server.Http.Name)
		case server.Sse != nil:
			names = append(names, server.Sse.Name)
		}
	}
	return names
}

// NewSession starts the Chord runtime in the client-provided working directory.
// The cwd is the only source of truth for the session root; the directory the
// client process happens to be started in is irrelevant.
func (s *Server) NewSession(_ context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if s.opts.Bootstrap == nil {
		return acp.NewSessionResponse{}, acp.NewInternalError(map[string]any{"error": "acp adapter has no runtime bootstrap"})
	}
	cwd, cwdErr := SessionCwd(req.Cwd)
	if cwdErr != nil {
		return acp.NewSessionResponse{}, cwdErr
	}
	if len(req.McpServers) > 0 {
		log.Warnf("acp session/new asked for mcp servers; chord does not take them from a client yet count=%v names=%v", len(req.McpServers), mcpServerNames(req.McpServers))
	}
	if len(req.AdditionalDirectories) > 0 {
		log.Warnf("acp session/new asked for additional directories; chord keeps the session rooted at cwd count=%v dirs=%v", len(req.AdditionalDirectories), req.AdditionalDirectories)
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		// One process serves one session. A second session/new means the client
		// reached a session child instead of the mux frontend, which mints the
		// ids and spawns one child per session.
		return acp.NewSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "session already created for this process"})
	}
	s.started = true
	s.mu.Unlock()

	rt, err := s.opts.Bootstrap(cwd)
	if err != nil {
		s.mu.Lock()
		s.started = false
		s.mu.Unlock()
		log.Warnf("acp session bootstrap failed cwd=%v error=%v", cwd, err)
		return acp.NewSessionResponse{}, acp.NewInternalError(map[string]any{"cwd": cwd, "error": err.Error()})
	}
	if rt == nil || rt.Backend == nil {
		s.mu.Lock()
		s.started = false
		s.mu.Unlock()
		return acp.NewSessionResponse{}, acp.NewInternalError(map[string]any{"error": "runtime bootstrap returned no backend"})
	}
	session := NewSessionID()

	s.mu.Lock()
	s.rt = rt
	s.session = session
	s.cwd = cwd
	s.mu.Unlock()
	log.Infof("acp session created session_id=%v cwd=%v chord_session_id=%v", session, cwd, chordSessionName(rt))

	go s.pump(rt.Backend.Events())
	return acp.NewSessionResponse{SessionId: session, Meta: sessionMeta(rt)}, nil
}

// Prompt runs one turn and reports how it ended. It returns only after the turn
// settles, so a client's next prompt never overlaps a running turn.
func (s *Server) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	rt, err := s.sessionFor(req.SessionId)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	parts, err := messageParts(req.Prompt)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	// sessionFor ran before this lock, so the session is checked again here.
	// The check, the waiter install and the send form one hand-off under
	// startMu: CloseSession takes the same lock to mark the session closed and
	// pick up the waiter, so a close that lands in between either sees this
	// waiter and cancels it, or makes this prompt refuse to start a turn the
	// client was told had ended.
	s.startMu.Lock()
	if _, err := s.sessionFor(req.SessionId); err != nil {
		s.startMu.Unlock()
		return acp.PromptResponse{}, err
	}
	if ctx.Err() != nil {
		s.startMu.Unlock()
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}

	waiter := newTurnWaiter()
	s.setWaiter(waiter)
	rt.Backend.SendUserMessageWithParts(parts)
	s.startMu.Unlock()
	log.Debugf("acp prompt sent session_id=%v parts=%v", req.SessionId, len(parts))

	select {
	case <-waiter.done:
		s.clearWaiter(waiter)
		if err := waiter.waitErr(); err != nil {
			return acp.PromptResponse{}, acp.NewInternalError(map[string]any{"error": err.Error()})
		}
		// session/close cancels the Chord turn without cancelling this prompt's
		// context, so the context alone is not what decides the stop reason.
		if ctx.Err() != nil || waiter.wasCancelled() {
			log.Infof("acp prompt cancelled session_id=%v", req.SessionId)
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		if waiter.drainTimedOut() {
			log.Warnf("acp turn answered before its last stream reported an end session_id=%v waited=%v", req.SessionId, settleTimeout)
		}
		log.Debugf("acp prompt finished session_id=%v", req.SessionId)
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	case <-ctx.Done():
		// session/cancel (or a superseding prompt) cancelled this turn.
		waiter.cancel(rt)
		s.waitSettled(waiter, settleTimeout)
		s.clearWaiter(waiter)
		log.Infof("acp prompt cancelled session_id=%v", req.SessionId)
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
}

// Cancel handles session/cancel. The SDK has already cancelled the prompt
// context of this session, so cancelling the Chord turn here is what unblocks
// the agent loop; Prompt reports the cancelled stop reason.
//
// The cancel shares startMu with the prompt hand-off, so it runs either before
// the hand-off or after the message is in — never in between. Without that, a
// cancel could run between the send and the message's acceptance, find nothing
// to cancel, and leave the turn running to completion.
func (s *Server) Cancel(_ context.Context, params acp.CancelNotification) error {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	rt := s.rt
	waiter := s.waiter
	session := s.session
	s.mu.Unlock()
	if params.SessionId != session {
		log.Debugf("acp cancel ignored session_id=%v reason=unknown-session", params.SessionId)
		return nil
	}
	if rt == nil || waiter == nil {
		log.Debugf("acp cancel ignored session_id=%v reason=no-active-turn", params.SessionId)
		return nil
	}
	waiter.cancel(rt)
	return nil
}

// CloseSession cancels any running turn and marks the session terminal, as the
// protocol requires before resources are released. Signaling SessionClosed
// hands the teardown to the entrypoint: a child process exits, and the mux
// frontend reaps it.
func (s *Server) CloseSession(_ context.Context, req acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	if _, err := s.sessionFor(req.SessionId); err != nil {
		return acp.CloseSessionResponse{}, err
	}
	// Closing shares startMu with the prompt hand-off: by the time this lock is
	// held, every prompt that got past its own check has already handed its turn
	// to the backend, so the waiter picked up here belongs to a started turn
	// this close can cancel; a prompt that comes later sees closed and refuses
	// to start.
	s.startMu.Lock()
	s.mu.Lock()
	s.closed = true
	rt := s.rt
	waiter := s.waiter
	s.mu.Unlock()
	if waiter != nil {
		waiter.cancel(rt)
	}
	s.startMu.Unlock()
	if waiter != nil {
		s.waitSettled(waiter, settleTimeout)
	}
	s.closeOnce.Do(func() { close(s.sessionClosed) })
	log.Infof("acp session closed session_id=%v", req.SessionId)
	return acp.CloseSessionResponse{}, nil
}

// ListSessions is optional and not advertised as a capability.
func (s *Server) ListSessions(_ context.Context, _ acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

// ResumeSession is optional and not advertised as a capability.
func (s *Server) ResumeSession(_ context.Context, _ acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

// SetSessionMode is unsupported while no modes are advertised.
func (s *Server) SetSessionMode(_ context.Context, _ acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}

// SetSessionConfigOption is unsupported while no config options are advertised.
func (s *Server) SetSessionConfigOption(_ context.Context, _ acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

// waitSettled waits for a cancelled turn to stop emitting before the next
// prompt runs. Timing out is logged, not fatal: the client already has its
// cancelled answer.
func (s *Server) waitSettled(waiter *turnWaiter, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-waiter.done:
	case <-timer.C:
		log.Warnf("acp cancelled turn did not settle within %v", timeout)
	}
}

// pump forwards main-agent events to the client and tracks turn boundaries.
func (s *Server) pump(events <-chan agent.AgentEvent) {
	mapper := &eventMapper{}
	for ev := range events {
		updates, effects := mapper.Map(ev)
		for _, update := range updates {
			s.sendUpdate(update)
		}
		s.applyEffects(effects)
	}
}

func (s *Server) sendUpdate(update acp.SessionUpdate) {
	conn, session := s.connection()
	if conn == nil || session == "" {
		return
	}
	err := conn.SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: session,
		Update:    update,
	})
	if err != nil {
		log.Warnf("acp session update failed session_id=%v error=%v", session, err)
	}
}

func (s *Server) applyEffects(effects eventEffects) {
	waiter := s.currentWaiter()
	if waiter == nil {
		return
	}
	if effects.err != nil {
		waiter.recordError(effects.err)
		log.Warnf("acp turn error recorded error=%v", effects.err)
	}
	if effects.recovered {
		waiter.clearError()
	}
	if effects.busy {
		waiter.markBusy()
	}
	if effects.streamStarted {
		waiter.markStreamStarted(effects.segment)
	}
	if effects.streamDrained {
		waiter.markStreamDrained(effects.segment)
	}
	if effects.settle {
		waiter.markTurnEnded()
	}
}

// turnWaiter tracks one session/prompt turn until the agent settles.
//
// Settling needs three things, not just idle: the turn must have done work (the
// runtime also reports idle while it is only starting up), global quiescence
// must have arrived, and no main-agent stream may still be open. The last one is
// what keeps a cancelled turn's tail inside its own turn: Chord's streaming
// reducer flushes buffered deltas when the request goroutine unwinds, which on a
// cancel can land after the idle signal, and StreamSegmentEndedEvent is the
// boundary it reports after that flush.
type turnWaiter struct {
	done       chan struct{}
	cancelOnce sync.Once

	mu         sync.Mutex
	busySeen   bool
	turnEnded  bool
	streamOpen bool
	streamID   streamSegment
	settled    bool
	// cancelled records that the turn was aborted, whatever cancelled it.
	cancelled bool
	// drainTimer bounds the wait for the open stream's end once the turn is
	// over; drainExpired says the wait ran out and the turn was answered with
	// that stream still open.
	drainTimer   *time.Timer
	drainExpired bool
	err          error
}

func newTurnWaiter() *turnWaiter {
	return &turnWaiter{done: make(chan struct{})}
}

// markBusy records that the turn actually did work. Global idle alone is not
// enough to finish a prompt: the runtime also reports suppressed idle while it
// is only starting up.
func (w *turnWaiter) markBusy() {
	w.mu.Lock()
	w.busySeen = true
	w.mu.Unlock()
}

// markStreamStarted records that a main-agent request began streaming. The
// waiter stays open until the segment that produced that request reports its
// end.
func (w *turnWaiter) markStreamStarted(segment streamSegment) {
	w.mu.Lock()
	w.streamOpen = true
	w.streamID = segment
	w.mu.Unlock()
}

// markStreamDrained records the segment end that follows a request's final
// flush, so any buffered delta has already been handed to the client. An end
// naming a different segment than the open one belongs to an older request whose
// own boundary was lost, and must not close the stream the client is reading.
func (w *turnWaiter) markStreamDrained(segment streamSegment) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.streamOpen && !sameSegment(w.streamID, segment) {
		return
	}
	w.streamOpen = false
	w.settleLocked()
}

// markTurnEnded records global quiescence. Idle before the turn did any work is
// a startup signal, not this turn's end, so it is ignored.
func (w *turnWaiter) markTurnEnded() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.busySeen {
		return
	}
	w.turnEnded = true
	w.settleLocked()
}

func (w *turnWaiter) settleLocked() {
	if w.settled || !w.busySeen || !w.turnEnded {
		return
	}
	if w.streamOpen {
		// The turn is over but its last stream has not reported its end yet, so
		// buffered deltas may still be on the way. Chord always reports that
		// end, but a lost boundary must not hold the client's answer forever.
		w.startDrainTimerLocked()
		return
	}
	w.finishLocked()
}

func (w *turnWaiter) startDrainTimerLocked() {
	if w.drainTimer != nil {
		return
	}
	w.drainTimer = time.AfterFunc(settleTimeout, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.settled {
			return
		}
		w.drainExpired = true
		w.streamOpen = false
		w.finishLocked()
	})
}

func (w *turnWaiter) finishLocked() {
	if w.settled {
		return
	}
	if w.drainTimer != nil {
		w.drainTimer.Stop()
		w.drainTimer = nil
	}
	w.settled = true
	close(w.done)
}

// drainTimedOut reports that the turn was answered while a stream was still
// open, which means chunks belonging to it can still reach the client after the
// response.
func (w *turnWaiter) drainTimedOut() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.drainExpired
}

func (w *turnWaiter) recordError(err error) {
	if err == nil {
		return
	}
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
}

func (w *turnWaiter) clearError() {
	w.mu.Lock()
	w.err = nil
	w.mu.Unlock()
}

func (w *turnWaiter) waitErr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// cancel asks Chord to abort the turn once: both Prompt (via the cancelled
// context) and Cancel (via session/cancel) may observe the same request, and a
// second effective call would double-report cancellation. Every caller runs
// after the prompt hand-off — Cancel and CloseSession share startMu with it,
// and Prompt's cancelled-context branch is already past it — so the request
// either covers the accepted message or finds the session genuinely idle.
func (w *turnWaiter) cancel(rt *Runtime) {
	w.cancelOnce.Do(func() {
		w.mu.Lock()
		w.cancelled = true
		w.mu.Unlock()
		if rt == nil || rt.Backend == nil {
			return
		}
		rt.Backend.CancelCurrentTurn()
	})
}

// wasCancelled reports whether the turn was aborted. It is what decides the stop
// reason rather than the request context: session/close cancels the Chord turn
// without cancelling the prompt context, and a superseding prompt cancels both.
func (w *turnWaiter) wasCancelled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cancelled
}
