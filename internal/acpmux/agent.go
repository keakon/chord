package acpmux

import (
	"context"
	"errors"

	acp "github.com/coder/acp-go-sdk"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/acpagent"
)

// Initialize answers the client directly and memoizes the request, so every
// child is initialized with the same protocol version and client capabilities.
// Chord exposes no authentication, no session loading and no client-side
// filesystem or terminal delegation, so the answer only carries the negotiated
// protocol version and the capabilities every child implements.
func (p *Proxy) Initialize(_ context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	acpagent.LogInitialize(req)
	stored := req
	p.mu.Lock()
	p.initReq = &stored
	p.mu.Unlock()
	return acpagent.InitializeResponse(p.opts.Version), nil
}

// Authenticate is unreachable while no auth methods are advertised.
func (p *Proxy) Authenticate(_ context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
}

// Logout is not supported; Chord keeps no ACP-visible credentials.
func (p *Proxy) Logout(_ context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

// NewSession starts a child for the session and forwards session/new to it. The
// child reports the Chord session directory as metadata; the id the client gets
// is the one minted here, which also names the child's log file.
func (p *Proxy) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	cwd, cwdErr := acpagent.SessionCwd(req.Cwd)
	if cwdErr != nil {
		return acp.NewSessionResponse{}, cwdErr
	}
	if p.opts.Spawn == nil {
		return acp.NewSessionResponse{}, acp.NewInternalError(map[string]any{"error": "acp mux has no child spawner"})
	}
	initReq, s, err := p.reserve(cwd)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	child, err := p.opts.Spawn(cwd, string(s.clientID))
	if err != nil {
		p.drop(s)
		p.wg.Done()
		log.Warnf("acp mux session spawn failed session_id=%v cwd=%v error=%v", s.clientID, cwd, err)
		return acp.NewSessionResponse{}, acp.NewInternalError(map[string]any{"cwd": cwd, "error": err.Error()})
	}
	conn := acp.NewClientSideConnection(&childClient{proxy: p, session: s}, child.Writer(), child.Reader())
	published := p.attachChild(s, child, conn)
	// watch owns the child's exit either way: it frees the session slot and
	// releases the pipes once the child's output has drained.
	go p.watch(s, child, conn)
	if !published {
		// A stop was requested while the child was starting, so this child was
		// never routable. It is still a live runtime, and this is the only path
		// that knows about it, so this path reaps it.
		startErr := errors.New("session was closed while starting")
		p.failStartup(s, startErr)
		return acp.NewSessionResponse{}, acp.NewInternalError(map[string]any{"cwd": cwd, "error": startErr.Error()})
	}

	// The child serves one session and has to be initialized before it will
	// create one; the client's capabilities are the ones it negotiates with.
	if _, err := conn.Initialize(ctx, *initReq); err != nil {
		p.failStartup(s, err)
		return acp.NewSessionResponse{}, childError(err)
	}
	resp, err := conn.NewSession(ctx, req)
	if err != nil {
		p.failStartup(s, err)
		return acp.NewSessionResponse{}, childError(err)
	}
	chordSession := chordSessionName(resp.Meta)
	p.mu.Lock()
	s.agentID = resp.SessionId
	s.chordSession = chordSession
	p.mu.Unlock()
	log.Infof("acp mux session created session_id=%v cwd=%v chord_session_id=%v", s.clientID, cwd, chordSession)
	return acp.NewSessionResponse{SessionId: s.clientID, Meta: resp.Meta}, nil
}

// failStartup retires a session whose child could not be brought up. The
// session stays in the table until the child is reaped so the limit still
// counts a live process; only its closing flag makes it unroutable. The child
// is stopped in the background: the client is waiting for the session/new
// answer, and the child still has to be reaped. watch removes the entry.
func (p *Proxy) failStartup(s *session, err error) {
	p.mu.Lock()
	s.closing = true
	p.mu.Unlock()
	log.Warnf("acp mux session startup failed session_id=%v cwd=%v error=%v", s.clientID, s.cwd, err)
	p.stopChildAsync(s)
}

// Prompt forwards one turn to the session's child and reports how it ended. The
// client's own cancellation does not abort the forward: a cancelled turn has to
// be answered by the child, after the updates that belong to it.
func (p *Proxy) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	if ctx != nil && ctx.Err() != nil {
		// session/cancel reached this prompt before it was forwarded. The turn
		// never starts at the child, so answer cancelled here.
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	s, err := p.sessionConn(req.SessionId)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	p.mu.Lock()
	if s.closing {
		p.mu.Unlock()
		return acp.PromptResponse{}, acp.NewInvalidParams(map[string]any{"sessionId": string(req.SessionId), "error": "session is closing"})
	}
	conn, agentID := s.conn, s.agentID
	p.mu.Unlock()
	req.SessionId = agentID
	resp, err := conn.Prompt(contextWithoutCancel(ctx), req)
	if err != nil {
		if ctx.Err() != nil {
			// session/cancel reached this prompt's context. The child is
			// winding its turn down; the client's answer is the cancelled stop
			// reason.
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, childError(err)
	}
	return resp, nil
}

// Cancel forwards session/cancel to the child that owns the session. The child
// cancels its turn and answers the pending prompt with the cancelled stop
// reason; unknown, still-starting and closing sessions are ignored like any
// notification, and a failed forward is dropped the same way.
//
// The proxy keeps no cancel latch of its own. The child re-issues a cancel that
// lands before its own prompt hand-off, so a cancel racing this forward is
// covered there; the one case that can be lost is a cancel the SDK dispatches
// before it registered the prompt context, and that window lives inside the
// SDK's dispatch rather than in state this proxy can observe.
func (p *Proxy) Cancel(ctx context.Context, params acp.CancelNotification) error {
	p.mu.Lock()
	s := p.sessions[params.SessionId]
	if s == nil || s.conn == nil || s.agentID == "" || s.closing {
		p.mu.Unlock()
		log.Debugf("acp mux cancel ignored session_id=%v reason=unknown-starting-or-closing", params.SessionId)
		return nil
	}
	conn, agentID := s.conn, s.agentID
	p.mu.Unlock()
	if err := conn.Cancel(ctx, acp.CancelNotification{SessionId: agentID}); err != nil {
		// The turn keeps running and the client gets its end_turn instead of
		// cancelled, so this is worth more than a debug line.
		log.Warnf("acp mux cancel forward failed session_id=%v error=%v", params.SessionId, err)
		return nil
	}
	return nil
}

// CloseSession forwards session/close and then reaps the child. The child
// cancels its turn, releases the runtime and exits; the frontend answers the
// client as soon as the child has answered, and frees the slot when the process
// is actually gone.
func (p *Proxy) CloseSession(ctx context.Context, req acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	p.mu.Lock()
	s := p.sessions[req.SessionId]
	if s == nil {
		p.mu.Unlock()
		return acp.CloseSessionResponse{}, unknownSession(req.SessionId)
	}
	if s.closing {
		p.mu.Unlock()
		return acp.CloseSessionResponse{}, acp.NewInvalidParams(map[string]any{"sessionId": string(req.SessionId), "error": "session is closing"})
	}
	s.closing = true
	conn, agentID, chordSession := s.conn, s.agentID, s.chordSession
	p.mu.Unlock()
	if conn == nil || agentID == "" {
		// The child died before it created the session; there is nothing left
		// to close, only a process to reap.
		p.stopChildAsync(s)
		return acp.CloseSessionResponse{}, nil
	}

	closeCtx, cancel := context.WithTimeout(contextWithoutCancel(ctx), childCloseTimeout)
	resp, err := conn.CloseSession(closeCtx, acp.CloseSessionRequest{SessionId: agentID})
	cancel()
	p.stopChildAsync(s)
	if err != nil {
		return acp.CloseSessionResponse{}, childError(err)
	}
	log.Infof("acp mux session closed session_id=%v chord_session_id=%v", req.SessionId, chordSession)
	return resp, nil
}

// ListSessions is optional and not advertised as a capability.
func (p *Proxy) ListSessions(_ context.Context, _ acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

// ResumeSession is optional and not advertised as a capability.
func (p *Proxy) ResumeSession(_ context.Context, _ acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

// SetSessionMode forwards a mode change to the session's child.
func (p *Proxy) SetSessionMode(ctx context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	s, err := p.sessionConn(req.SessionId)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	p.mu.Lock()
	if s.closing {
		p.mu.Unlock()
		return acp.SetSessionModeResponse{}, acp.NewInvalidParams(map[string]any{"sessionId": string(req.SessionId), "error": "session is closing"})
	}
	conn, agentID := s.conn, s.agentID
	p.mu.Unlock()
	req.SessionId = agentID
	resp, err := conn.SetSessionMode(ctx, req)
	if err != nil {
		return acp.SetSessionModeResponse{}, childError(err)
	}
	return resp, nil
}

// SetSessionConfigOption forwards a config change to the session's child. The
// request is a union, and the session id lives in whichever variant is set.
func (p *Proxy) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	var sessionID acp.SessionId
	switch {
	case req.Boolean != nil:
		sessionID = req.Boolean.SessionId
	case req.ValueId != nil:
		sessionID = req.ValueId.SessionId
	default:
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "set_config_option carries no value"})
	}
	s, err := p.sessionConn(sessionID)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	p.mu.Lock()
	if s.closing {
		p.mu.Unlock()
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"sessionId": string(sessionID), "error": "session is closing"})
	}
	conn, agentID := s.conn, s.agentID
	p.mu.Unlock()
	if req.Boolean != nil {
		req.Boolean.SessionId = agentID
	}
	if req.ValueId != nil {
		req.ValueId.SessionId = agentID
	}
	resp, err := conn.SetSessionConfigOption(ctx, req)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, childError(err)
	}
	return resp, nil
}
