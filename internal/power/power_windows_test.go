//go:build windows

package power

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fakeWindowsPowerAPI struct {
	createErr, setErr, clearErr, closeErr error
	calls                                 []string
	afterCreate, afterSet                 func()
}

func (f *fakeWindowsPowerAPI) create() (windows.Handle, error) {
	f.calls = append(f.calls, "create")
	if f.afterCreate != nil {
		f.afterCreate()
	}
	return 42, f.createErr
}
func (f *fakeWindowsPowerAPI) set(windows.Handle) error {
	f.calls = append(f.calls, "set")
	if f.afterSet != nil {
		f.afterSet()
	}
	return f.setErr
}
func (f *fakeWindowsPowerAPI) clear(windows.Handle) error {
	f.calls = append(f.calls, "clear")
	return f.clearErr
}
func (f *fakeWindowsPowerAPI) close(windows.Handle) error {
	f.calls = append(f.calls, "close")
	return f.closeErr
}

func TestWindowsReasonContextABI(t *testing.T) {
	expected := uintptr(32)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		expected = 24
	}
	if unsafe.Sizeof(reasonContext{}) != expected {
		t.Fatalf("size=%d, want %d", unsafe.Sizeof(reasonContext{}), expected)
	}
	if unsafe.Offsetof(reasonContext{}.Reason) != 8 {
		t.Fatal("union offset must be 8")
	}
	if unsafe.Offsetof(reasonUnion{}.ID) != unsafe.Sizeof(uintptr(0)) || unsafe.Offsetof(reasonUnion{}.Count) != unsafe.Sizeof(uintptr(0))+4 {
		t.Fatal("invalid detailed union layout")
	}
}
func TestWindowsLeaseCleanup(t *testing.T) {
	failure := errors.New("injected")
	for _, step := range []string{"none", "create", "set", "clear", "close", "set-close", "clear-close"} {
		t.Run(step, func(t *testing.T) {
			api := &fakeWindowsPowerAPI{}
			switch step {
			case "create":
				api.createErr = failure
			case "set":
				api.setErr = failure
			case "clear":
				api.clearErr = failure
			case "close":
				api.closeErr = failure
			case "set-close":
				api.setErr = failure
				api.closeErr = failure
			case "clear-close":
				api.clearErr = failure
				api.closeErr = failure
			}
			l, err := (&windowsBackend{api: api}).Acquire(context.Background())
			if step == "create" || step == "set" || step == "set-close" {
				if l != nil || !errors.Is(err, failure) {
					t.Fatalf("Acquire=(%v,%v)", l, err)
				}
				if errors.Is(err, ErrCleanupFailed) != (step == "set-close") {
					t.Fatalf("cleanup marker: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var wg sync.WaitGroup
				for range 8 {
					wg.Go(func() {
						got := l.Close()
						if (got != nil) != (step != "none") {
							t.Errorf("Close=%v", got)
						}
					})
				}
				wg.Wait()
				select {
				case <-l.Done():
				default:
					t.Fatal("Done open")
				}
				if errors.Is(l.Err(), ErrCleanupFailed) != (step != "none") {
					t.Fatalf("Err=%v", l.Err())
				}
			}
			expected := map[string]int{"create": 1, "set": 3, "set-close": 3}[step]
			if expected == 0 {
				expected = 4
			}
			if len(api.calls) != expected {
				t.Fatalf("calls=%v", api.calls)
			}
		})
	}
}
func TestWindowsAcquireCancellation(t *testing.T) {
	for _, step := range []string{"before", "create", "set"} {
		t.Run(step, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			api := &fakeWindowsPowerAPI{}
			switch step {
			case "before":
				cancel()
			case "create":
				api.afterCreate = cancel
			case "set":
				api.afterSet = cancel
			}
			l, err := (&windowsBackend{api: api}).Acquire(ctx)
			if l != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("Acquire=(%v,%v)", l, err)
			}
			expected := map[string]int{"before": 0, "create": 2, "set": 4}[step]
			if len(api.calls) != expected {
				t.Fatalf("calls=%v", api.calls)
			}
		})
	}
}
func TestWindowsPowerCallError(t *testing.T) {
	for _, err := range []error{nil, syscall.Errno(0), syscall.Errno(5)} {
		if got := powerCallError("operation", err); got == nil {
			t.Fatal("missing failure")
		}
	}
}
