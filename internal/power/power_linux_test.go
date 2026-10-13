//go:build linux

package power

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/keakon/dbus/v5"
	"golang.org/x/sys/unix"
)

func TestParseBusAddresses(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []string
	}{
		{"unix:path=/tmp/a%20b+c,guid=123", []string{"/tmp/a b+c"}},
		{"unix:abstract=test%2bfoo;unix:path=/tmp/bus", []string{"\x00test+foo", "/tmp/bus"}},
	} {
		got, err := parseBusAddresses(tt.in)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(tt.want) {
			t.Fatalf("%v != %v", got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("%q != %q", got[i], tt.want[i])
			}
		}
	}
	for _, in := range []string{"", "tcp:host=localhost", "autolaunch:", "nonce-tcp:host=localhost", "unix:path=/a,abstract=b", "unix:path=", "unix:guid=a", "unix:path=/a%ZZ", "unix:path=/a%00", "unix:path=relative", "unix:path=/a,path=/b"} {
		if _, err := parseBusAddresses(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestFDRegistryClosingAndLateReply(t *testing.T) {
	for _, late := range []bool{false, true} {
		var pipe [2]int
		if err := unix.Pipe2(pipe[:], unix.O_CLOEXEC); err != nil {
			t.Fatal(err)
		}
		defer unix.Close(pipe[0])
		r := &fdRegistry{}
		if late {
			if err := r.close(); err != nil {
				t.Fatal(err)
			}
		}
		r.receive([]int{pipe[1]})
		if !late {
			flags, err := unix.FcntlInt(uintptr(pipe[1]), unix.F_GETFD, 0)
			if err != nil || flags&unix.FD_CLOEXEC == 0 {
				t.Fatalf("descriptor not CLOEXEC: %v %v", flags, err)
			}
		}
		if err := r.close(); err != nil {
			t.Fatal(err)
		}
		if err := r.close(); err != nil {
			t.Fatal(err)
		}
		var buf [1]byte
		if n, err := unix.Read(pipe[0], buf[:]); n != 0 || err != nil {
			t.Fatalf("expected pipe EOF, got %d %v", n, err)
		}
	}
}

func TestOwnerWatchExactMatch(t *testing.T) {
	w := &ownerWatch{name: logindName, lost: make(chan struct{})}
	signal := &dbus.Signal{Sender: "untrusted", Path: busPath, Name: ownerChanged, Body: []any{logindName, ":1.1", ":1.2"}}
	w.DeliverSignal("", "", signal)
	select {
	case <-w.lost:
		t.Fatal("accepted wrong sender")
	default:
	}
	signal.Sender = busName
	w.DeliverSignal("", "", signal)
	w.DeliverSignal("", "", signal)
	select {
	case <-w.lost:
	default:
		t.Fatal("owner loss was not published")
	}
}

func isolatedBus(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is required for isolated Linux D-Bus integration tests")
	}
	cmd := exec.Command(path, "--session", "--nofork", "--nopidfile", "--print-address=1", "--address=unix:path="+filepath.Join(t.TempDir(), "bus"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		if scanner.Scan() {
			address <- scanner.Text()
		}
	}()
	select {
	case addr := <-address:
		return addr
	case <-time.After(5 * time.Second):
		t.Fatal("isolated bus did not start")
		return ""
	}
}

type fakeLogind struct {
	mu          sync.Mutex
	read, write *os.File
	called      bool
}

func (f *fakeLogind) Inhibit(what, who, why, mode string) (dbus.UnixFD, *dbus.Error) {
	if what != "idle" || who != "Chord" || why != inhibitReason || mode != "block" {
		return 0, dbus.NewError("org.example.InvalidArguments", nil)
	}
	read, write, err := os.Pipe()
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.read = read
	f.write = write
	f.called = true
	return dbus.UnixFD(write.Fd()), nil
}
func (f *fakeLogind) closeOriginal(t *testing.T) *os.File {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.called {
		t.Fatal("logind Inhibit not called")
	}
	if err := f.write.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.read.Close() })
	return f.read
}

type fakeGnome struct {
	mu       sync.Mutex
	held     bool
	sender   string
	released chan struct{}
}

func (f *fakeGnome) Inhibit(sender dbus.Sender, app string, window uint32, reason string, flags uint32) (uint32, *dbus.Error) {
	if app != "chord" || window != 0 || reason != inhibitReason || flags != gnomeSuspend {
		return 0, dbus.NewError("org.example.InvalidArguments", nil)
	}
	f.mu.Lock()
	f.held = true
	f.sender = string(sender)
	f.mu.Unlock()
	return 42, nil
}
func (f *fakeGnome) Uninhibit(cookie uint32) *dbus.Error {
	if cookie != 42 {
		return dbus.NewError("org.example.InvalidCookie", nil)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held {
		f.held = false
		close(f.released)
	}
	return nil
}
func exportService(t *testing.T, address, name string, path dbus.ObjectPath, iface string, service any) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err = conn.Export(service, path, iface); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestLinuxIsolatedBusLease(t *testing.T) {
	for _, which := range []string{"both", "logind", "gnome", "neither"} {
		t.Run(which, func(t *testing.T) {
			address := isolatedBus(t)
			t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", address)
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
			logind := &fakeLogind{}
			gnome := &fakeGnome{released: make(chan struct{})}
			if which == "both" || which == "logind" {
				exportService(t, address, logindName, logindPath, logindName+".Manager", logind)
			}
			if which == "both" || which == "gnome" {
				exportService(t, address, gnomeName, gnomePath, gnomeName, gnome)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			lease, err := NewBackend().Acquire(ctx)
			if which == "neither" {
				if err == nil || lease != nil {
					t.Fatal("empty success")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range lease.(*linuxLease).providers {
				p.fds.mu.Lock()
				for fd := range p.fds.fds {
					flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
					if err != nil || flags&unix.FD_CLOEXEC == 0 {
						t.Errorf("received descriptor lacks CLOEXEC: %v %v", flags, err)
					}
				}
				p.fds.mu.Unlock()
			}
			var read *os.File
			if which == "both" || which == "logind" {
				read = logind.closeOriginal(t)
			}
			cancel() // The request context must not own the delivered lease.
			select {
			case <-lease.Done():
				t.Fatal("request cancellation invalidated delivered lease")
			default:
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					if err := lease.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			if lease.Err() != nil {
				t.Fatal(lease.Err())
			}
			if read != nil {
				if err := read.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				var b [1]byte
				if _, err := read.Read(b[:]); err != io.EOF {
					t.Fatalf("FD retained: %v", err)
				}
			}
			if which == "both" || which == "gnome" {
				select {
				case <-gnome.released:
				default:
					t.Fatal("cookie not released")
				}
			}
		})
	}
}

func TestLinuxOwnerLoss(t *testing.T) {
	address := isolatedBus(t)
	service := &fakeGnome{released: make(chan struct{})}
	conn := exportService(t, address, gnomeName, gnomePath, gnomeName, service)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing"))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
	lease, err := NewBackend().Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err = conn.ReleaseName(gnomeName); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lease.Done():
		if lease.Err() == nil {
			t.Fatal("loss had no reason")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner loss not observed")
	}
}

func TestLinuxAuthCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socket")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := listener.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := acquireLinuxProvider(ctx, logindName, "unix:path="+path); result <- err }()
	conn := <-accepted
	defer conn.Close()
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Auth cancellation blocked")
	}
}

func TestLinuxCanceledAcquire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lease, err := NewBackend().Acquire(ctx)
	if lease != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v %v", lease, err)
	}
}

func TestFDRegistryConcurrentClose(t *testing.T) {
	for range 100 {
		var pipe [2]int
		if err := unix.Pipe2(pipe[:], unix.O_CLOEXEC); err != nil {
			t.Fatal(err)
		}
		r := &fdRegistry{}
		var wg sync.WaitGroup
		wg.Go(func() { r.receive([]int{pipe[1]}) })
		wg.Go(func() {
			if err := r.close(); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()
		if err := r.close(); err != nil {
			t.Fatal(err)
		}
		var b [1]byte
		if n, err := unix.Read(pipe[0], b[:]); n != 0 || err != nil {
			t.Fatalf("FD retained: %d %v", n, err)
		}
		_ = unix.Close(pipe[0])
	}
}

type deniedGnome struct{}

func (deniedGnome) Inhibit(string, uint32, string, uint32) (uint32, *dbus.Error) {
	return 0, dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"denied"})
}

type malformedLogind struct{}

func (malformedLogind) Inhibit(string, string, string, string) (uint32, *dbus.Error) { return 1, nil }
func TestLinuxProviderFailure(t *testing.T) {
	t.Run("denied", func(t *testing.T) {
		address := isolatedBus(t)
		exportService(t, address, gnomeName, gnomePath, gnomeName, deniedGnome{})
		p, err := acquireLinuxProvider(context.Background(), gnomeName, address)
		if p != nil || err == nil {
			t.Fatal("denial succeeded")
		}
		if got := (linuxCategorizedError{err}).PowerErrorCategory(); got != "inhibit:org.freedesktop.DBus.Error.AccessDenied" {
			t.Fatalf("category: %s", got)
		}
	})
	t.Run("wrong type", func(t *testing.T) {
		address := isolatedBus(t)
		exportService(t, address, logindName, logindPath, logindName+".Manager", malformedLogind{})
		p, err := acquireLinuxProvider(context.Background(), logindName, address)
		if p != nil || err == nil {
			t.Fatal("wire index accepted as Unix FD")
		}
	})
}

func TestLinuxDisconnectedLease(t *testing.T) {
	address := isolatedBus(t)
	exportService(t, address, gnomeName, gnomePath, gnomeName, &fakeGnome{released: make(chan struct{})})
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing"))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
	lease, err := NewBackend().Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err = lease.(*linuxLease).providers[0].conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lease.Done():
		if lease.Err() == nil {
			t.Fatal("disconnect has no reason")
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect not observed")
	}
}

// This helper deliberately outlives its caller until SIGKILL. The ordinary tool
// it launches must not inherit either the inhibitor FD or private bus sockets.
func TestLinuxProcessLifetimeHelper(t *testing.T) {
	if os.Getenv("CHORD_POWER_LIFETIME_HELPER") != "1" {
		return
	}
	lease, err := NewBackend().Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	tool := exec.Command("sleep", "30")
	if err = tool.Start(); err != nil {
		t.Fatal(err)
	}
	fmt.Println(tool.Process.Pid)
	select {}
}

func TestLinuxSIGKILLDoesNotLeaveFDInTool(t *testing.T) {
	address := isolatedBus(t)
	service := &fakeLogind{}
	gnome := &fakeGnome{released: make(chan struct{})}
	gnomeConn := exportService(t, address, gnomeName, gnomePath, gnomeName, gnome)
	exportService(t, address, logindName, logindPath, logindName+".Manager", service)
	cmd := exec.Command(os.Args[0], "-test.run=^TestLinuxProcessLifetimeHelper$")
	cmd.Env = append(os.Environ(), "CHORD_POWER_LIFETIME_HELPER=1", "DBUS_SYSTEM_BUS_ADDRESS="+address, "DBUS_SESSION_BUS_ADDRESS="+address)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		if scanner.Scan() {
			ready <- scanner.Text()
		}
	}()
	var pid int
	select {
	case line := <-ready:
		pid, err = strconv.Atoi(line)
		if err != nil {
			t.Fatalf("invalid helper ready response: %q", line)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("helper did not acquire lease")
	}
	tool, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tool.Kill() })

	gnome.mu.Lock()
	sender := gnome.sender
	gnome.mu.Unlock()
	if sender == "" {
		t.Fatal("GNOME inhibitor has no sender")
	}
	lost := make(chan *dbus.Signal, 1)
	gnomeConn.Signal(lost)
	defer gnomeConn.RemoveSignal(lost)
	if err = gnomeConn.AddMatchSignal(dbus.WithMatchSender(busName), dbus.WithMatchObjectPath(busPath), dbus.WithMatchInterface(busName), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, sender)); err != nil {
		t.Fatal(err)
	}
	read := service.closeOriginal(t)
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true
	if err = unix.Kill(pid, 0); err != nil {
		t.Fatalf("ordinary tool did not survive helper: %v", err)
	}
	if err = read.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err = read.Read(b[:]); err != io.EOF {
		t.Fatalf("tool inherited inhibitor FD: %v", err)
	}
	select {
	case signal := <-lost:
		if len(signal.Body) != 3 || signal.Body[0] != sender || signal.Body[2] != "" {
			t.Fatalf("unexpected owner signal: %v", signal.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tool inherited private GNOME connection")
	}
}

type blockedGnome struct{ entered, proceed chan struct{} }

func (f *blockedGnome) Inhibit(string, uint32, string, uint32) (uint32, *dbus.Error) {
	close(f.entered)
	<-f.proceed
	return 1, nil
}
func TestLinuxOwnerChangesDuringInhibit(t *testing.T) {
	address := isolatedBus(t)
	service := &blockedGnome{entered: make(chan struct{}), proceed: make(chan struct{})}
	conn := exportService(t, address, gnomeName, gnomePath, gnomeName, service)
	result := make(chan error, 1)
	go func() {
		p, err := acquireLinuxProvider(context.Background(), gnomeName, address)
		if p != nil {
			_ = p.close()
		}
		result <- err
	}()
	select {
	case <-service.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Inhibit not entered")
	}
	if _, err := conn.ReleaseName(gnomeName); err != nil {
		t.Fatal(err)
	}
	close(service.proceed)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("owner replacement during Inhibit succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("acquire did not finish")
	}
}
func TestLinuxInhibitCancellation(t *testing.T) {
	address := isolatedBus(t)
	service := &blockedGnome{entered: make(chan struct{}), proceed: make(chan struct{})}
	exportService(t, address, gnomeName, gnomePath, gnomeName, service)
	defer close(service.proceed)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		p, err := acquireLinuxProvider(ctx, gnomeName, address)
		if p != nil {
			_ = p.close()
		}
		result <- err
	}()
	select {
	case <-service.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Inhibit not entered")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("method cancellation blocked")
	}
}
