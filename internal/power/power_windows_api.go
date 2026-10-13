//go:build windows

package power

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	powerRequestContextVersion      = 0
	powerRequestContextSimpleString = 1
	powerRequestSystemRequired      = 1
	powerRequestReason              = "Chord is running agent work"
)

// The union must occupy the full Detailed layout, including on 386.
type reasonContext struct {
	Version uint32
	Flags   uint32
	Reason  reasonUnion
}
type reasonUnion struct {
	Module  uintptr
	ID      uint32
	Count   uint32
	Strings uintptr
}

type windowsPowerAPI interface {
	create() (windows.Handle, error)
	set(windows.Handle) error
	clear(windows.Handle) error
	close(windows.Handle) error
}
type nativeWindowsPowerAPI struct{}

var (
	powerDLL           = windows.NewLazySystemDLL("kernel32.dll")
	powerCreateRequest = powerDLL.NewProc("PowerCreateRequest")
	powerSetRequest    = powerDLL.NewProc("PowerSetRequest")
	powerClearRequest  = powerDLL.NewProc("PowerClearRequest")
)

func powerCallError(operation string, err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return fmt.Errorf("%s failed without an error code", operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
func (nativeWindowsPowerAPI) create() (windows.Handle, error) {
	for _, proc := range []*windows.LazyProc{powerCreateRequest, powerSetRequest, powerClearRequest} {
		if err := proc.Find(); err != nil {
			return 0, errors.Join(ErrUnsupported, fmt.Errorf("%s: %w", proc.Name, err))
		}
	}
	reason, err := windows.UTF16FromString(powerRequestReason)
	if err != nil {
		return 0, err
	}
	ctx := reasonContext{Version: powerRequestContextVersion, Flags: powerRequestContextSimpleString, Reason: reasonUnion{Module: uintptr(unsafe.Pointer(&reason[0]))}}
	handle, _, callErr := powerCreateRequest.Call(uintptr(unsafe.Pointer(&ctx)))
	runtime.KeepAlive(ctx)
	runtime.KeepAlive(reason)
	if handle == 0 || windows.Handle(handle) == windows.InvalidHandle {
		return 0, powerCallError("PowerCreateRequest", callErr)
	}
	return windows.Handle(handle), nil
}
func (nativeWindowsPowerAPI) set(handle windows.Handle) error {
	ok, _, err := powerSetRequest.Call(uintptr(handle), powerRequestSystemRequired)
	if ok == 0 {
		return powerCallError("PowerSetRequest", err)
	}
	return nil
}
func (nativeWindowsPowerAPI) clear(handle windows.Handle) error {
	ok, _, err := powerClearRequest.Call(uintptr(handle), powerRequestSystemRequired)
	if ok == 0 {
		return powerCallError("PowerClearRequest", err)
	}
	return nil
}
func (nativeWindowsPowerAPI) close(handle windows.Handle) error {
	if err := windows.CloseHandle(handle); err != nil {
		return fmt.Errorf("CloseHandle: %w", err)
	}
	return nil
}
