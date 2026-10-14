//go:build linux

package power

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keakon/dbus/v5"
	"github.com/keakon/golog/log"
	"golang.org/x/sys/unix"
)

const (
	busName                          = "org.freedesktop.DBus"
	busPath          dbus.ObjectPath = "/org/freedesktop/DBus"
	ownerChanged                     = busName + ".NameOwnerChanged"
	logindName                       = "org.freedesktop.login1"
	logindPath       dbus.ObjectPath = "/org/freedesktop/login1"
	logindInhibit                    = logindName + ".Manager.Inhibit"
	gnomeName                        = "org.gnome.SessionManager"
	gnomePath        dbus.ObjectPath = "/org/gnome/SessionManager"
	gnomeInhibit                     = gnomeName + ".Inhibit"
	gnomeUninhibit                   = gnomeName + ".Uninhibit"
	inhibitReason                    = "Chord is running agent work"
	gnomeSuspend     uint32          = 4
	providerTimeout                  = 2 * time.Second
	uninhibitTimeout                 = 500 * time.Millisecond
)

// Only local Unix stream sockets are accepted. PathUnescape preserves literal '+'.
func parseBusAddresses(address string) ([]string, error) {
	var paths []string
	for entry := range strings.SplitSeq(address, ";") {
		transport, options, ok := strings.Cut(entry, ":")
		if !ok || transport != "unix" {
			return nil, fmt.Errorf("unsupported D-Bus address transport %q", transport)
		}
		fields := make(map[string]string)
		for option := range strings.SplitSeq(options, ",") {
			key, value, ok := strings.Cut(option, "=")
			if !ok {
				return nil, errors.New("invalid D-Bus address option")
			}
			if _, exists := fields[key]; exists {
				return nil, errors.New("duplicate D-Bus address option")
			}
			if key != "path" && key != "abstract" && key != "guid" {
				return nil, fmt.Errorf("unsupported D-Bus address option %q", key)
			}
			decoded, err := url.PathUnescape(value)
			if err != nil || strings.ContainsRune(decoded, 0) {
				return nil, errors.New("invalid D-Bus address escape")
			}
			fields[key] = decoded
		}
		path, abstract := fields["path"], fields["abstract"]
		_, hasPath := fields["path"]
		_, hasAbstract := fields["abstract"]
		if hasPath == hasAbstract || path == "" && abstract == "" {
			return nil, errors.New("D-Bus address requires exactly one path or abstract socket")
		}
		if hasAbstract {
			paths = append(paths, "\x00"+abstract)
		} else {
			if !filepath.IsAbs(path) {
				return nil, errors.New("D-Bus socket path must be absolute")
			}
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func systemBusAddress() string {
	if address := os.Getenv("DBUS_SYSTEM_BUS_ADDRESS"); address != "" {
		return address
	}
	return "unix:path=/run/dbus/system_bus_socket"
}
func sessionBusAddress() string {
	if address := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); address != "" {
		return address
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(dir) {
		return "unix:path=" + dbus.EscapeBusAddressValue(filepath.Join(dir, "bus"))
	}
	return ""
}

type ownerWatch struct {
	name string
	lost chan struct{}
	once sync.Once
}

func (w *ownerWatch) DeliverSignal(_, _ string, s *dbus.Signal) {
	if s.Sender != busName || s.Path != busPath || s.Name != ownerChanged || len(s.Body) != 3 {
		return
	}
	name, ok := s.Body[0].(string)
	if !ok || name != w.name {
		return
	}
	if _, ok := s.Body[1].(string); !ok {
		return
	}
	if _, ok := s.Body[2].(string); !ok {
		return
	}
	w.once.Do(func() { close(w.lost) })
}

// The registry owns descriptors from recvmsg, before any message decoding.
// Closing the bus and joining its reader seals the final cleanup result.
type fdRegistry struct {
	mu      sync.Mutex
	closing bool
	fds     map[int]struct{}
	err     error
}

func (r *fdRegistry) receive(fds []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, fd := range fds {
		if r.closing {
			r.err = errors.Join(r.err, unix.Close(fd))
		} else {
			if r.fds == nil {
				r.fds = make(map[int]struct{})
			}
			r.fds[fd] = struct{}{}
		}
	}
}
func (r *fdRegistry) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closing = true
	for fd := range r.fds {
		r.err = errors.Join(r.err, unix.Close(fd))
		delete(r.fds, fd)
	}
	return r.err
}

type linuxProvider struct {
	name          string
	socket        *net.UnixConn
	conn          *dbus.Conn
	watch         *ownerWatch
	fds           fdRegistry
	owner         string
	cookie        uint32
	hasCookie     bool
	readerStarted bool
}

func (p *linuxProvider) healthy() error {
	select {
	case <-p.watch.lost:
		return fmt.Errorf("%s owner changed", p.name)
	default:
	}
	if p.conn.Context().Err() != nil {
		return fmt.Errorf("%s bus disconnected", p.name)
	}
	return nil
}
func (p *linuxProvider) close() error {
	var errs []error
	_ = p.fds.close()
	if p.hasCookie && p.healthy() == nil {
		ctx, cancel := context.WithTimeout(context.Background(), uninhibitTimeout)
		deadline, _ := ctx.Deadline()
		if err := p.socket.SetWriteDeadline(deadline); err == nil {
			if err := p.conn.Object(p.owner, gnomePath).CallWithContext(ctx, gnomeUninhibit, dbus.FlagNoAutoStart, p.cookie).Err; err != nil {
				log.Debugf("power: GNOME Uninhibit failed; disconnecting private bus: %v", err)
			}
		}
		cancel()
	}
	if p.conn != nil {
		if err := p.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("close %s connection: %w", p.name, err))
		}
	} else if p.socket != nil {
		if err := p.socket.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if p.readerStarted {
		<-p.conn.ReadDone()
	}
	if err := p.fds.close(); err != nil {
		errs = append(errs, fmt.Errorf("close received descriptors: %w", err))
	}
	if len(errs) > 0 {
		return errors.Join(append([]error{ErrCleanupFailed}, errs...)...)
	}
	return nil
}

func acquireLinuxProvider(parent context.Context, name, address string) (_ *linuxProvider, retErr error) {
	operation := "address"
	defer func() {
		if retErr != nil {
			retErr = &linuxOperationError{operation: operation, error: retErr}
		}
	}()
	ctx, cancel := context.WithTimeout(parent, providerTimeout)
	defer cancel()
	paths, err := parseBusAddresses(address)
	if err != nil {
		return nil, fmt.Errorf("%s address: %w", name, err)
	}
	p := &linuxProvider{name: name, watch: &ownerWatch{name: name, lost: make(chan struct{})}}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, p.close())
		}
	}()
	operation = "dial"
	var dialErr error
	for _, path := range paths {
		c, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
		if err != nil {
			dialErr = errors.Join(dialErr, err)
			continue
		}
		p.socket = c.(*net.UnixConn)
		break
	}
	if p.socket == nil {
		return nil, fmt.Errorf("%s dial: %w", name, dialErr)
	}
	deadline, _ := ctx.Deadline()
	if err := p.socket.SetDeadline(deadline); err != nil {
		return nil, err
	}
	// Stop and join the callback before transferring ownership to the lease.
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = p.fds.close(); _ = p.socket.Close(); close(callbackDone) })
	stopped := false
	defer func() {
		if !stopped && !stop() {
			<-callbackDone
		}
	}()
	operation = "connect"
	p.conn, err = dbus.DialUnix(p.socket, dbus.WithSignalHandler(p.watch), dbus.WithIncomingUnixFDs(p.fds.receive))
	if err != nil {
		return nil, fmt.Errorf("%s connect: %w", name, err)
	}
	operation = "auth"
	if err = p.conn.Auth(nil); err != nil {
		return nil, fmt.Errorf("%s auth: %w", name, err)
	}
	p.readerStarted = true
	operation = "hello"
	var unique string
	if err = p.conn.BusObject().CallWithContext(ctx, busName+".Hello", dbus.FlagNoAutoStart).Store(&unique); err != nil {
		return nil, fmt.Errorf("%s hello: %w", name, err)
	}
	operation = "subscribe"
	match := "type='signal',sender='" + busName + "',path='" + string(busPath) + "',interface='" + busName + "',member='NameOwnerChanged',arg0='" + name + "'"
	if err = p.conn.BusObject().CallWithContext(ctx, busName+".AddMatch", dbus.FlagNoAutoStart, match).Err; err != nil {
		return nil, fmt.Errorf("%s subscribe: %w", name, err)
	}
	operation = "owner"
	if err = p.conn.BusObject().CallWithContext(ctx, busName+".GetNameOwner", dbus.FlagNoAutoStart, name).Store(&p.owner); err != nil {
		return nil, fmt.Errorf("%s owner: %w", name, err)
	}
	if err = p.healthy(); err != nil {
		return nil, err
	}
	operation = "inhibit"
	var call *dbus.Call
	if name == logindName {
		if !p.conn.SupportsUnixFDs() {
			return nil, errors.New("logind connection does not support Unix descriptors")
		}
		call = p.conn.Object(p.owner, logindPath).CallWithContext(ctx, logindInhibit, dbus.FlagNoAutoStart, "idle", "Chord", inhibitReason, "block")
		if call.Err == nil {
			if len(call.Body) != 1 {
				return nil, errors.New("logind Inhibit returned invalid argument count")
			}
			if _, ok := call.Body[0].(dbus.UnixFD); !ok {
				return nil, errors.New("logind Inhibit did not return a Unix descriptor")
			}
		}
	} else {
		call = p.conn.Object(p.owner, gnomePath).CallWithContext(ctx, gnomeInhibit, dbus.FlagNoAutoStart, "chord", uint32(0), inhibitReason, gnomeSuspend)
		if call.Err == nil {
			if len(call.Body) != 1 {
				return nil, errors.New("GNOME Inhibit returned invalid argument count")
			}
			cookie, ok := call.Body[0].(uint32)
			if !ok {
				return nil, errors.New("GNOME Inhibit did not return a cookie")
			}
			p.cookie = cookie
			p.hasCookie = true
		}
	}
	if call.Err != nil {
		return nil, fmt.Errorf("%s Inhibit: %w", name, call.Err)
	}
	operation = "handoff"
	if !stop() {
		<-callbackDone
		stopped = true
		return nil, ctx.Err()
	}
	stopped = true
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = p.socket.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	if err = p.healthy(); err != nil {
		return nil, err
	}
	return p, nil
}

// Operation and protocol error names form stable diagnostic categories.
type linuxOperationError struct {
	operation string
	error
}

func (e *linuxOperationError) Unwrap() error { return e.error }

type linuxCategorizedError struct{ error }

func (e linuxCategorizedError) Unwrap() error { return e.error }
func (e linuxCategorizedError) PowerErrorCategory() string {
	operation := "acquire"
	if op, ok := errors.AsType[*linuxOperationError](e.error); ok {
		operation = op.operation
	}
	if busErr, ok := errors.AsType[dbus.Error](e.error); ok {
		return operation + ":" + busErr.Name
	}
	if busErrPtr, ok := errors.AsType[*dbus.Error](e.error); ok {
		return operation + ":" + busErrPtr.Name
	}
	if errno, ok := errors.AsType[syscall.Errno](e.error); ok {
		return fmt.Sprintf("%s:errno:%d", operation, errno)
	}
	if errors.Is(e.error, context.Canceled) {
		return operation + ":canceled"
	}
	if errors.Is(e.error, context.DeadlineExceeded) {
		return operation + ":deadline"
	}
	return operation + ":unavailable"
}

// linuxAcquireError keeps aggregation metadata separate from diagnostic text.
type linuxAcquireError struct {
	error
	category string
}

func (e linuxAcquireError) Unwrap() error              { return e.error }
func (e linuxAcquireError) PowerErrorCategory() string { return e.category }
