//go:build darwin

package power

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestDarwinPowerHelper(t *testing.T) {
	if os.Getenv("CHORD_POWER_TEST_HELPER") != "1" {
		return
	}
	if os.Getenv("CHORD_POWER_TEST_EXIT") == "1" {
		os.Exit(0)
	}
	time.Sleep(time.Hour)
	os.Exit(0)
}
func darwinTestCommand(exit bool) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestDarwinPowerHelper$")
	cmd.Env = append(os.Environ(), "CHORD_POWER_TEST_HELPER=1")
	if exit {
		cmd.Env = append(cmd.Env, "CHORD_POWER_TEST_EXIT=1")
	}
	return cmd
}
func TestCaffeinateArguments(t *testing.T) {
	cmd := caffeinateCommand()
	want := []string{"/usr/bin/caffeinate", "-i", "-w", strconv.Itoa(os.Getpid())}
	for i, arg := range want {
		if cmd.Args[i] != arg {
			t.Fatalf("args=%v", cmd.Args)
		}
	}
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Fatal("unexpected process streams")
	}
}
func TestDarwinLeaseClose(t *testing.T) {
	var cmd *exec.Cmd
	b := &DarwinBackend{command: func() *exec.Cmd { cmd = darwinTestCommand(false); return cmd }}
	ctx, cancel := context.WithCancel(context.Background())
	l, err := b.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-l.Done():
		t.Fatal("acquisition context owns lease")
	default:
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := l.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	select {
	case <-l.Done():
	default:
		t.Fatal("Done open")
	}
	if l.Err() != nil {
		t.Fatal(l.Err())
	}
	if cmd.ProcessState == nil {
		t.Fatal("process not reaped")
	}
}
func TestDarwinLeaseUnexpectedExit(t *testing.T) {
	b := &DarwinBackend{command: func() *exec.Cmd { return darwinTestCommand(true) }}
	l, err := b.Acquire(context.Background())
	if err != nil {
		return
	} // An immediate exit may be observed before handoff.
	select {
	case <-l.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("exit not observed")
	}
	if l.Err() == nil {
		t.Fatal("successful exit must still signal protection loss")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestDarwinAcquireCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cmd *exec.Cmd
	b := &DarwinBackend{command: func() *exec.Cmd { cancel(); cmd = darwinTestCommand(false); return cmd }}
	l, err := b.Acquire(ctx)
	if l != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire=(%v,%v)", l, err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("cancelled acquisition did not reap process")
	}
	called := false
	b.command = func() *exec.Cmd { called = true; return darwinTestCommand(false) }
	_, _ = b.Acquire(ctx)
	if called {
		t.Fatal("started command after cancellation")
	}
}

func TestDarwinLeaseDelayedExit(t *testing.T) {
	var cmd *exec.Cmd
	b := &DarwinBackend{command: func() *exec.Cmd { cmd = darwinTestCommand(false); return cmd }}
	l, err := b.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("exit not observed")
	}
	if l.Err() == nil {
		t.Fatal("unexpected termination was not reported")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinAcquireStartFailure(t *testing.T) {
	b := &DarwinBackend{command: func() *exec.Cmd { return exec.Command("/nonexistent/chord-power-test") }}
	l, err := b.Acquire(context.Background())
	if l != nil || err == nil {
		t.Fatalf("Acquire=(%v,%v)", l, err)
	}
}
