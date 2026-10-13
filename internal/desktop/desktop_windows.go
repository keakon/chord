package desktop

import (
	"context"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var shell32 = windows.NewLazySystemDLL("shell32.dll")
var shellExecuteEx = shell32.NewProc("ShellExecuteExW")
var openWithDialog = shell32.NewProc("SHOpenWithDialog")
var ole32 = windows.NewLazySystemDLL("ole32.dll")
var coInit = ole32.NewProc("CoInitializeEx")
var coUninit = ole32.NewProc("CoUninitialize")

func (s *System) Capabilities(ctx context.Context) (Capabilities, error) {
	if remote() {
		return Capabilities{Reason: ErrNoDesktop.Error()}, nil
	}
	return Capabilities{DefaultOpen: true, NativeChooser: true}, ctx.Err()
}
func (s *System) queryAssociations(context.Context, string) (Associations, error) {
	return Associations{}, ErrUnsupported
}
func (s *System) OpenWith(context.Context, string, string) error { return ErrUnsupported }

type shellExecuteInfo struct {
	Size       uint32
	Mask       uint32
	Window     uintptr
	Verb       *uint16
	File       *uint16
	Parameters *uint16
	Directory  *uint16
	Show       int32
	Instance   uintptr
	IDList     uintptr
	Class      *uint16
	ClassKey   uintptr
	HotKey     uint32
	Icon       uintptr
	Process    windows.Handle
}

func apartment(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := coInit.Call(0, 2)
	if int32(hr) < 0 {
		return fmt.Errorf("COM initialization failed: %#x", hr)
	}
	defer coUninit.Call()
	return fn()
}
func (s *System) OpenDefault(ctx context.Context, path string) error {
	return s.launch(ctx, path, func() error {
		return apartment(func() error {
			file, err := windows.UTF16PtrFromString(path)
			if err != nil {
				return err
			}
			verb, _ := windows.UTF16PtrFromString("open")
			info := shellExecuteInfo{Mask: 0x40 | 0x100, Verb: verb, File: file, Show: 1}
			info.Size = uint32(unsafe.Sizeof(info))
			ok, _, e := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
			if info.Process != 0 {
				windows.CloseHandle(info.Process)
			}
			if ok == 0 {
				return e
			}
			return nil
		})
	})
}
func (s *System) ChooseApplication(ctx context.Context, path string) error {
	return s.launch(ctx, path, func() error {
		return apartment(func() error {
			file, err := windows.UTF16PtrFromString(path)
			if err != nil {
				return err
			}
			info := struct {
				File  *uint16
				Class *uint16
				Flags uint32
			}{File: file, Flags: 4}
			hr, _, _ := openWithDialog.Call(0, uintptr(unsafe.Pointer(&info)))
			if uint32(hr) == 0x800704c7 {
				return nil
			}
			if int32(hr) < 0 {
				return fmt.Errorf("application chooser failed: %#x", hr)
			}
			return nil
		})
	})
}
