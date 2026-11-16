package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

func openSetupTestPTY() (*os.File, *os.File, error) {
	symbol, err := purego.Dlsym(purego.RTLD_DEFAULT, "openpty")
	if err != nil {
		return nil, nil, err
	}
	var openPTY func(*int32, *int32, *byte, uintptr, uintptr) int32
	purego.RegisterFunc(&openPTY, symbol)
	var masterFD, slaveFD int32
	var name [128]byte
	if openPTY(&masterFD, &slaveFD, &name[0], 0, 0) != 0 {
		return nil, nil, fmt.Errorf("open PTY via libSystem failed")
	}
	path := strings.TrimRight(string(name[:]), "\x00")
	return os.NewFile(uintptr(masterFD), "PTY master"), os.NewFile(uintptr(slaveFD), path), nil
}

func setupTestTerminalSettings(fd uintptr) (*unix.Termios, error) {
	settings, err := unix.IoctlGetTermios(int(fd), unix.TIOCGETA)
	if err != nil {
		return nil, err
	}
	// Returning to canonical mode sets PENDIN for kernel input reprocessing,
	// including a direct MakeRaw/Restore pair with no Chord code involved.
	settings.Lflag &^= unix.PENDIN
	return settings, nil
}
