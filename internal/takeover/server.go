package takeover

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/recovery"
)

type ServerOptions struct {
	StateDir  string
	Record    Record
	Start     func(context.Context, recovery.TransferIdentity) (recovery.SessionPauseStatus, error)
	Status    func(string) (recovery.SessionPauseStatus, error)
	RetrySave func(context.Context, string) error
}

type Server struct {
	options    ServerOptions
	listener   *net.UnixListener
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	workers    chan struct{}
	wg         sync.WaitGroup
	mu         sync.Mutex
	record     Record
	secret     string
	closed     bool
	conns      map[*net.UnixConn]struct{}
	ownedFiles map[string]os.FileInfo
	closeOnce  sync.Once
	closeErr   error
}

func StartServer(options ServerOptions) (*Server, error) {
	record, err := normalizeRecord(options.StateDir, options.Record)
	if err != nil {
		return nil, err
	}
	if options.Start == nil || options.Status == nil || options.RetrySave == nil {
		return nil, protocolError(CodeInvalidRequest, "session callbacks are required")
	}
	dir := discoveryDir(options.StateDir)
	if err := privatefs.EnsureDir(options.StateDir, dir); err != nil {
		return nil, fmt.Errorf("create takeover discovery directory: %w", err)
	}
	if _, err := validatePrivate(options.StateDir, dir, privatefs.DirMode); err != nil {
		return nil, err
	}
	s := &Server{options: options, record: record, done: make(chan struct{}), workers: make(chan struct{}, MaxWorkers), conns: make(map[*net.UnixConn]struct{}), ownedFiles: make(map[string]os.FileInfo)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	cleanup := func(err error) (*Server, error) {
		s.cancel()
		if s.listener != nil {
			s.listener.Close()
		}
		for path, info := range s.ownedFiles {
			removeOwned(path, info)
		}
		return nil, err
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return cleanup(err)
	}
	s.secret = hex.EncodeToString(key[:])
	path := keyPath(options.StateDir, record.RuntimeID)
	f, err := privatefs.OpenFile(options.StateDir, path, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return cleanup(fmt.Errorf("create source key: %w", err))
	}
	if info, err := f.Stat(); err == nil {
		s.ownedFiles[path] = info
	}
	n, writeErr := f.Write(key[:])
	if writeErr == nil && n != len(key) {
		writeErr = fmt.Errorf("incomplete source key write")
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return cleanup(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: record.SocketPath, Net: "unix"})
	if err != nil {
		return cleanup(fmt.Errorf("listen takeover endpoint: %w", err))
	}
	listener.SetUnlinkOnClose(false)
	s.listener = listener
	info, err := os.Lstat(record.SocketPath)
	if err != nil {
		return cleanup(err)
	}
	s.ownedFiles[record.SocketPath] = info
	if err := os.Chmod(record.SocketPath, privatefs.FileMode); err != nil {
		return cleanup(err)
	}
	path = recordPath(options.StateDir, record.RuntimeID)
	if _, err := privatefs.Stat(options.StateDir, path); err == nil {
		return cleanup(protocolError(CodeStaleSource, "runtime discovery record already exists"))
	} else if !os.IsNotExist(err) {
		return cleanup(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return cleanup(err)
	}
	info, err = writeAtomic(options.StateDir, path, data)
	if info != nil {
		s.ownedFiles[path] = info
	}
	if err != nil {
		return cleanup(err)
	}
	go s.accept()
	return s, nil
}

// UpdateRecord keeps the listener and secret while publishing a new session
// binding. Runtime and socket identity never move across discovery locations.
func (s *Server) UpdateRecord(record Record) error {
	normalized, err := normalizeRecord(s.options.StateDir, record)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	if normalized.RuntimeID != s.record.RuntimeID || normalized.SocketPath != s.record.SocketPath || normalized.OwnerKind != s.record.OwnerKind {
		return protocolError(CodeStaleSource, "runtime endpoint identity cannot change")
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	path := recordPath(s.options.StateDir, normalized.RuntimeID)
	info, err := writeAtomic(s.options.StateDir, path, data)
	if info != nil {
		s.record = normalized
		s.ownedFiles[path] = info
	}
	return err
}
func (s *Server) Record() Record { s.mu.Lock(); defer s.mu.Unlock(); return s.record }

func (s *Server) accept() {
	defer close(s.done)
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Warnf("takeover listener stopped error=%v", err)
			}
			return
		}
		select {
		case s.workers <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			<-s.workers
			return
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer func() { conn.Close(); s.mu.Lock(); delete(s.conns, conn); s.mu.Unlock(); <-s.workers }()
			s.handle(conn)
		}()
	}
}
func (s *Server) handle(conn *net.UnixConn) {
	if err := conn.SetDeadline(time.Now().Add(HandshakeTimeout)); err != nil {
		return
	}
	if err := checkPeer(conn, uint32(os.Geteuid())); err != nil {
		return
	}
	var req request
	if err := readFrame(conn, &req); err != nil {
		s.respond(conn, response{Protocol: ProtocolVersion, Source: s.Record(), Error: publicError(err)})
		return
	}
	record := s.Record()
	if req.Protocol != ProtocolVersion {
		s.respond(conn, response{Protocol: ProtocolVersion, Source: record, Error: publicError(protocolError(CodeInvalidRequest, "takeover protocol version is unsupported"))})
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Secret), []byte(s.secret)) != 1 {
		s.respond(conn, response{Protocol: ProtocolVersion, Source: record, Error: publicError(protocolError(CodeInvalidRequest, "request authentication failed"))})
		return
	}
	if err := validateSource(req.Source, record); err != nil {
		s.respond(conn, response{Protocol: ProtocolVersion, Source: record, Error: publicError(err)})
		return
	}
	if !ValidID(req.Identity.TransferID, 128) || req.BlockerOffset < 0 {
		s.respond(conn, response{Protocol: ProtocolVersion, Source: record, Error: publicError(protocolError(CodeInvalidRequest, "transfer identity or blocker offset is invalid"))})
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, HandshakeTimeout)
	defer cancel()
	var status recovery.SessionPauseStatus
	var err error
	switch req.Action {
	case ActionStart:
		if err = validateTransfer(req.Identity, record); err == nil {
			status, err = s.options.Start(ctx, req.Identity)
		}
	case ActionStatus:
		status, err = s.options.Status(req.Identity.TransferID)
	case ActionRetrySave:
		if err = s.options.RetrySave(ctx, req.Identity.TransferID); err == nil {
			status, err = s.options.Status(req.Identity.TransferID)
		}
	default:
		err = protocolError(CodeInvalidRequest, "unknown takeover action")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if status.TransferID != "" {
			status.WaitExpired = true
		}
		err = protocolError(CodeStatusUnknown, "source observation ended; query this transfer identity")
	}
	reply := response{Protocol: ProtocolVersion, Source: s.Record(), Error: publicError(err)}
	if status.TransferID != "" {
		if identityErr := validateStatusIdentity(status, record, req.Identity.TransferID); identityErr != nil {
			reply.Error = publicError(identityErr)
		} else {
			page := pageStatus(status, req.BlockerOffset)
			reply.Status = &page
		}
	}
	s.respond(conn, reply)
}
func (s *Server) respond(conn *net.UnixConn, reply response) {
	if err := conn.SetWriteDeadline(time.Now().Add(WriteTimeout)); err == nil {
		if err := writeFrame(conn, reply); err != nil {
			log.Debugf("takeover response write failed error=%v", err)
		}
	}
}

func validateSource(actual, expected Record) error {
	if actual.Workspace != expected.Workspace {
		return protocolError(CodeWrongWorkspace, "source workspace changed")
	}
	if actual.OwnerKind != expected.OwnerKind {
		return protocolError(CodeWrongOwnerKind, "source owner kind changed")
	}
	if actual != expected {
		return protocolError(CodeStaleSource, "source runtime or session binding changed")
	}
	return nil
}
func validateTransfer(id recovery.TransferIdentity, record Record) error {
	if id.Workspace != record.Workspace {
		return protocolError(CodeWrongWorkspace, "transfer workspace does not match source")
	}
	if id.SessionID != record.SessionID || id.SourceRuntimeID != record.RuntimeID || id.SourceSessionEpoch != record.SessionEpoch {
		return protocolError(CodeStaleSource, "transfer source identity does not match runtime")
	}
	if (id.TargetKind != OwnerTUI && id.TargetKind != OwnerGateway) || !ValidID(id.TargetInstanceID, 128) {
		return protocolError(CodeInvalidRequest, "transfer target identity is invalid")
	}
	if id.TargetKind == record.OwnerKind {
		return protocolError(CodeWrongOwnerKind, "source and target owner kinds must differ")
	}
	return nil
}
func validateStatusIdentity(status recovery.SessionPauseStatus, record Record, transferID string) error {
	if status.TransferID != transferID || status.SessionID != record.SessionID || status.SourceRuntimeID != record.RuntimeID || status.SourceSessionEpoch != record.SessionEpoch || status.Workspace != record.Workspace {
		return protocolError(CodeStaleSource, "transfer status does not match source identity")
	}
	return nil
}

// Close cancels observation waits, closes descriptors, waits for admitted
// callbacks, and removes only the files this listener still owns.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.closeErr = s.listener.Close()
		for conn := range s.conns {
			conn.Close()
		}
		s.mu.Unlock()
		<-s.done
		s.wg.Wait()
		s.mu.Lock()
		defer s.mu.Unlock()
		for path, info := range s.ownedFiles {
			s.closeErr = errors.Join(s.closeErr, removeOwned(path, info))
		}
		s.closeErr = errors.Join(s.closeErr, privatefs.SyncDir(discoveryDir(s.options.StateDir)))
	})
	return s.closeErr
}
func removeOwned(path string, expected os.FileInfo) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(info, expected) {
		return protocolError(CodeStaleSource, "discovery file ownership changed")
	}
	return os.Remove(path)
}

func checkPeer(conn *net.UnixConn, expectedUID uint32) error {
	uid, err := peerUID(conn)
	if err != nil || uid != expectedUID {
		return protocolError(CodeInvalidRequest, "peer authentication failed")
	}
	return nil
}
