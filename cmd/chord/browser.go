package main

import (
	"io"
	"os/exec"
	"runtime"
)

func openBrowser(target string) error {
	name, args := openBrowserCommand(runtime.GOOS, target)
	cmd := exec.Command(name, args...)
	if cmd.Stdout == nil {
		cmd.Stdout = io.Discard
	}
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	return cmd.Start()
}

func openBrowserCommand(goos, target string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{target}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		return "xdg-open", []string{target}
	}
}
