//go:build linux

package power

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/dbus/v5"
	"golang.org/x/sys/unix"
)

// Use a raw peer: a real bus would reject malformed messages before forwarding
// them, hiding the receive/close boundary this regression needs to exercise.
func wireProvider(t *testing.T, receive func([]int)) (*linuxProvider, *net.UnixConn) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]*net.UnixConn, 0, 2)
	for _, fd := range pair {
		f := os.NewFile(uintptr(fd), "socket")
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, c.(*net.UnixConn))
		t.Cleanup(func() { _ = c.Close() })
	}
	p := &linuxProvider{name: logindName, socket: connections[0], watch: &ownerWatch{lost: make(chan struct{})}}
	p.conn, err = dbus.DialUnix(p.socket, dbus.WithIncomingUnixFDs(func(fds []int) {
		p.fds.receive(fds)
		receive(fds)
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.close() })
	peer := connections[1]
	_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
	_ = p.socket.SetDeadline(time.Now().Add(5 * time.Second))
	authDone := make(chan error, 1)
	go func() {
		r := bufio.NewReader(peer)
		for _, exchange := range [][2]string{
			{"\x00AUTH", "REJECTED EXTERNAL\r\n"},
			{"AUTH EXTERNAL", "OK 0123456789abcdef0123456789abcdef\r\n"},
			{"NEGOTIATE_UNIX_FD", "AGREE_UNIX_FD\r\n"},
			{"BEGIN", ""},
		} {
			line, err := r.ReadString('\n')
			if err == nil && !strings.HasPrefix(line, exchange[0]) {
				err = fmt.Errorf("unexpected authentication command %q", line)
			}
			if err == nil && exchange[1] != "" {
				_, err = io.WriteString(peer, exchange[1])
			}
			if err != nil {
				authDone <- err
				return
			}
		}
		authDone <- nil
	}()
	if err := p.conn.Auth(nil); err != nil {
		t.Fatal(err)
	}
	p.readerStarted = true
	if err := <-authDone; err != nil {
		t.Fatal(err)
	}
	return p, peer
}

func TestLinuxWireFDReclaimed(t *testing.T) {
	for _, mode := range []string{"partial-header-close", "partial-body-close", "partial-body-disconnect", "invalid-index", "overstated-count", "unreferenced", "late-after-close"} {
		t.Run(mode, func(t *testing.T) {
			received := make(chan struct{}, 1)
			p, peer := wireProvider(t, func([]int) { received <- struct{}{} })
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			defer write.Close()
			body := any(dbus.UnixFD(write.Fd()))
			if mode == "unreferenced" {
				body = uint32(0)
			}
			msg := &dbus.Message{Type: dbus.TypeMethodReply, Headers: map[dbus.HeaderField]dbus.Variant{
				dbus.FieldReplySerial: dbus.MakeVariant(uint32(1)),
				dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(body)),
				dbus.FieldUnixFDs:     dbus.MakeVariant(uint32(1)),
			}, Body: []any{body}}
			if mode == "overstated-count" {
				msg.Headers[dbus.FieldUnixFDs] = dbus.MakeVariant(uint32(2))
			}
			var buffer bytes.Buffer
			if _, err := msg.EncodeToWithFDs(&buffer, binary.LittleEndian); err != nil {
				t.Fatal(err)
			}
			payload := buffer.Bytes()
			switch mode {
			case "partial-header-close":
				payload = payload[:1]
			case "partial-body-close", "partial-body-disconnect":
				payload = payload[:len(payload)-1]
			case "invalid-index":
				binary.LittleEndian.PutUint32(payload[len(payload)-4:], 4)
			case "overstated-count":
				binary.LittleEndian.PutUint32(payload[len(payload)-4:], 1)
			case "late-after-close":
				if err := p.fds.close(); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := peer.WriteMsgUnix(payload, unix.UnixRights(int(write.Fd())), nil); err != nil {
				t.Fatal(err)
			}
			if err := write.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-received:
			case <-time.After(5 * time.Second):
				t.Fatal("descriptor was not registered at receive time")
			}
			if mode == "partial-body-disconnect" || mode == "overstated-count" {
				_ = peer.Close()
				<-p.conn.ReadDone()
			}
			if err := p.close(); err != nil {
				t.Fatal(err)
			}
			_ = read.SetReadDeadline(time.Now().Add(time.Second))
			var b [1]byte
			if _, err := read.Read(b[:]); err != io.EOF {
				t.Fatalf("received descriptor remains open: %v", err)
			}
		})
	}
}

func TestLinuxCloseJoinsFDReceiver(t *testing.T) {
	entered, proceed := make(chan struct{}), make(chan struct{})
	p, peer := wireProvider(t, func([]int) {
		close(entered)
		<-proceed
	})
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if _, _, err := peer.WriteMsgUnix([]byte{'l'}, unix.UnixRights(int(write.Fd())), nil); err != nil {
		t.Fatal(err)
	}
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- p.close() }()
	<-p.conn.Context().Done()
	select {
	case <-closed:
		t.Error("cleanup returned before the FD receiver completed")
	default:
	}
	close(proceed)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

type changingDeniedGnome struct{ calls atomic.Int32 }

func (s *changingDeniedGnome) Inhibit(string, uint32, string, uint32) (uint32, *dbus.Error) {
	return 0, dbus.NewError("org.example.Denied", []any{fmt.Sprintf("sender :1.%d denied", s.calls.Add(1))})
}

func TestLinuxAggregateErrorCategory(t *testing.T) {
	address := isolatedBus(t)
	exportService(t, address, gnomeName, gnomePath, gnomeName, &changingDeniedGnome{})
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", address)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
	backend := NewBackend()
	_, first := backend.Acquire(context.Background())
	_, second := backend.Acquire(context.Background())
	if first == nil || second == nil {
		t.Fatal("unexpected acquisition success")
	}
	if first.Error() == second.Error() {
		t.Fatal("service did not change its error detail")
	}
	if errorCategory(first) != errorCategory(second) {
		t.Fatal("diagnostic text changed the error category")
	}
	if _, ok := errors.AsType[*dbus.Error](second); !ok {
		if _, ok := errors.AsType[dbus.Error](second); !ok {
			t.Fatal("aggregate lost the original protocol error")
		}
	}
}
