package takeover

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
)

type Client struct {
	Record   Record
	StateDir string
}

func (c Client) Start(ctx context.Context, identity recovery.TransferIdentity) (agent.SessionPauseStatus, error) {
	if err := validateTransfer(identity, c.Record); err != nil {
		return agent.SessionPauseStatus{}, err
	}
	return c.call(ctx, ActionStart, identity, 0)
}
func (c Client) Status(ctx context.Context, transferID string) (agent.SessionPauseStatus, error) {
	return c.call(ctx, ActionStatus, recovery.TransferIdentity{TransferID: transferID}, 0)
}
func (c Client) RetrySave(ctx context.Context, transferID string) (agent.SessionPauseStatus, error) {
	return c.call(ctx, ActionRetrySave, recovery.TransferIdentity{TransferID: transferID}, 0)
}
func (c Client) StatusPage(ctx context.Context, transferID string, offset int) (Status, error) {
	reply, err := c.exchange(ctx, ActionStatus, recovery.TransferIdentity{TransferID: transferID}, offset)
	if reply.Status == nil {
		return Status{}, err
	}
	return *reply.Status, err
}
func (c Client) call(ctx context.Context, action string, id recovery.TransferIdentity, offset int) (agent.SessionPauseStatus, error) {
	reply, err := c.exchange(ctx, action, id, offset)
	if reply.Status == nil {
		return agent.SessionPauseStatus{}, err
	}
	return reply.Status.AgentStatus(), err
}

func (c Client) exchange(ctx context.Context, action string, id recovery.TransferIdentity, offset int) (response, error) {
	record, err := normalizeRecord(c.StateDir, c.Record)
	if err != nil {
		return response{}, err
	}
	if record != c.Record {
		return response{}, protocolError(CodeInvalidRequest, "source identity is not canonical")
	}
	if !ValidID(id.TransferID, 128) || offset < 0 {
		return response{}, protocolError(CodeInvalidRequest, "transfer identity or blocker offset is invalid")
	}
	secret, err := readSecret(c.StateDir, record.RuntimeID)
	if err != nil {
		if _, ok := errors.AsType[*Error](err); ok {
			return response{}, err
		}
		return response{}, unknownStatus(err)
	}
	info, err := validatePrivate(c.StateDir, record.SocketPath, 0600)
	if err != nil {
		if os.IsNotExist(err) {
			return response{}, unknownStatus(err)
		}
		return response{}, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return response{}, protocolError(CodeInvalidRequest, "endpoint is not a socket")
	}
	conn, err := (&net.Dialer{Timeout: HandshakeTimeout}).DialContext(ctx, "unix", record.SocketPath)
	if err != nil {
		return response{}, unknownStatus(err)
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return response{}, protocolError(CodeInvalidRequest, "endpoint did not provide a local socket")
	}
	if err := checkPeer(unixConn, uint32(os.Geteuid())); err != nil {
		return response{}, protocolError(CodeInvalidRequest, "source peer authentication failed")
	}
	deadline := time.Now().Add(HandshakeTimeout + WriteTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return response{}, err
	}
	stopCancel := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stopCancel()
	if err := conn.SetWriteDeadline(minDeadline(deadline, time.Now().Add(WriteTimeout))); err != nil {
		return response{}, err
	}
	if err := writeFrame(conn, request{Protocol: ProtocolVersion, Secret: secret, Source: record, Action: action, Identity: id, BlockerOffset: offset}); err != nil {
		if ctx.Err() != nil {
			return response{}, unknownStatus(ctx.Err())
		}
		return response{}, unknownStatus(err)
	}
	var reply response
	if err := readFrame(conn, &reply); err != nil {
		if ctx.Err() != nil {
			return response{}, unknownStatus(ctx.Err())
		}
		if _, ok := errors.AsType[*Error](err); ok {
			return response{}, err
		}
		return response{}, unknownStatus(err)
	}
	if reply.Protocol != ProtocolVersion {
		return response{}, protocolError(CodeInvalidRequest, "source protocol version is unsupported")
	}
	if err := validateSource(reply.Source, record); err != nil {
		return response{}, err
	}
	if reply.Status != nil {
		if len(reply.Status.Blockers) > MaxBlockers || reply.Status.BlockerOffset != offset {
			return response{}, protocolError(CodeInvalidRequest, "source status exceeds blocker bounds")
		}
		if err := validateStatusIdentity(reply.Status.AgentStatus(), record, id.TransferID); err != nil {
			return response{}, err
		}
		if action == ActionStart && (reply.Status.TargetKind != id.TargetKind || reply.Status.TargetInstanceID != id.TargetInstanceID) {
			return response{}, protocolError(CodeStaleSource, "transfer target identity changed")
		}
	}
	if reply.Error != nil {
		return reply, reply.Error
	}
	if reply.Status == nil {
		return response{}, protocolError(CodeStatusUnknown, "source returned no transfer status")
	}
	return reply, nil
}
func unknownStatus(cause error) error {
	return &Error{Code: CodeStatusUnknown, Message: "source status is unknown; query this transfer identity", cause: cause}
}
func minDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
