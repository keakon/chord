package acpmux

import (
	"context"

	acp "github.com/coder/acp-go-sdk"
	"github.com/keakon/golog/log"
)

// childClient is the frontend's client side for one session child.
//
// It is bound to the session rather than to the child's own session id: a child
// serves exactly one session, so everything it sends belongs to this session,
// including notifications that arrive before its session/new answer names the
// id. Rewriting to the client's id is therefore unconditional, and a child's
// notification is never dropped for arriving too early.
type childClient struct {
	proxy   *Proxy
	session *session
}

var _ acp.Client = (*childClient)(nil)

// ReadTextFile forwards a child's filesystem read to the client.
func (c *childClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	conn, err := c.proxy.clientConn()
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	params.SessionId = c.session.clientID
	return conn.ReadTextFile(ctx, params)
}

// WriteTextFile forwards a child's filesystem write to the client.
func (c *childClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	conn, err := c.proxy.clientConn()
	if err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	params.SessionId = c.session.clientID
	return conn.WriteTextFile(ctx, params)
}

// RequestPermission forwards a child's permission request to the client. Only
// the client can answer it; if the child dies first, the client's answer is
// dropped because ACP has no way to withdraw an agent-to-client request.
func (c *childClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	conn, err := c.proxy.clientConn()
	if err != nil {
		return acp.RequestPermissionResponse{}, err
	}
	params.SessionId = c.session.clientID
	return conn.RequestPermission(ctx, params)
}

// SessionUpdate forwards a child's session update to the client.
//
// It writes synchronously, and that is load-bearing: the SDK completes a
// response only after the notifications that preceded it have been handled, so
// a forwarded prompt answer can never overtake the updates of its own turn.
func (c *childClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	conn, err := c.proxy.clientConn()
	if err != nil {
		return err
	}
	params.SessionId = c.session.clientID
	if err := conn.SessionUpdate(ctx, params); err != nil {
		log.Warnf("acp mux session update failed session_id=%v error=%v", c.session.clientID, err)
		return err
	}
	return nil
}

// CreateTerminal forwards a child's terminal request to the client.
func (c *childClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	conn, err := c.proxy.clientConn()
	if err != nil {
		return acp.CreateTerminalResponse{}, err
	}
	params.SessionId = c.session.clientID
	return conn.CreateTerminal(ctx, params)
}

// KillTerminal forwards a child's terminal request to the client.
func (c *childClient) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	conn, err := c.proxy.clientConn()
	if err != nil {
		return acp.KillTerminalResponse{}, err
	}
	params.SessionId = c.session.clientID
	return conn.KillTerminal(ctx, params)
}

// TerminalOutput forwards a child's terminal request to the client.
func (c *childClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	conn, err := c.proxy.clientConn()
	if err != nil {
		return acp.TerminalOutputResponse{}, err
	}
	params.SessionId = c.session.clientID
	return conn.TerminalOutput(ctx, params)
}

// ReleaseTerminal forwards a child's terminal request to the client.
func (c *childClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	conn, err := c.proxy.clientConn()
	if err != nil {
		return acp.ReleaseTerminalResponse{}, err
	}
	params.SessionId = c.session.clientID
	return conn.ReleaseTerminal(ctx, params)
}

// WaitForTerminalExit forwards a child's terminal request to the client.
func (c *childClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	conn, err := c.proxy.clientConn()
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, err
	}
	params.SessionId = c.session.clientID
	return conn.WaitForTerminalExit(ctx, params)
}
