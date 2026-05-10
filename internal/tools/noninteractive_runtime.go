package tools

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type NonInteractiveRuntimeFinding struct {
	Reason string
}

func nonInteractiveRuntimeAdvice() string {
	return nonInteractiveRuntimeAdviceForGOOS(runtime.GOOS)
}

func nonInteractiveRuntimeAdviceForGOOS(goos string) string {
	advice := "Shell and Spawn are non-interactive: stdin is closed and no controlling TTY is available. Use non-interactive flags, provide input through files/arguments/pipes, choose a non-interactive command, or ask the user to run it in a real terminal."
	if goos == "windows" {
		advice += " On Windows, timeout/cancellation cleanup uses process termination rather than Unix-style session/process-group control, so child-process cleanup may be less complete than on Unix."
	}
	return advice
}

func ClassifyNonInteractiveRuntimeFailure(command string, err error, output string) *NonInteractiveRuntimeFinding {
	if err == nil {
		return nil
	}
	text := strings.ToLower(output)
	if text == "" {
		text = strings.ToLower(err.Error())
	}
	if text == "" {
		return nil
	}

	for _, pattern := range []string{
		"terminal prompts disabled",
		"could not read username",
		"could not read password",
		"cannot open /dev/tty",
		"no tty present and no askpass program specified",
		"a terminal is required to read the password",
		"the input device is not a tty",
		"inappropriate ioctl for device",
		"must be connected to a terminal",
		"requires a tty",
		"requires an interactive terminal",
		"stdin is not a terminal",
		"standard input is not a terminal",
		"not a tty",
		"not a terminal",
		"terminal is dumb",
		"terminal is dumb, but editor requires a terminal",
		"there was a problem with the editor",
	} {
		if strings.Contains(text, pattern) {
			return &NonInteractiveRuntimeFinding{Reason: runtimeFailureReason(pattern)}
		}
	}

	if strings.Contains(text, "sudo") && strings.Contains(text, "password") && strings.Contains(text, "required") && !commandHasSudoNonInteractive(command) {
		return &NonInteractiveRuntimeFinding{Reason: "sudo could not prompt for a password"}
	}
	return nil
}

func FormatNonInteractiveRuntimeError(toolName, command string, err error, output string) error {
	finding := ClassifyNonInteractiveRuntimeFailure(command, err, output)
	if finding == nil {
		return err
	}
	prefix := "command failed"
	if exitErr, ok := err.(*exec.ExitError); ok {
		prefix = fmt.Sprintf("exit code %d", exitErr.ExitCode())
	}
	msg := fmt.Sprintf("%s: non-interactive %s failure: %s. %s", prefix, toolName, finding.Reason, nonInteractiveRuntimeAdvice())
	if strings.TrimSpace(output) != "" {
		msg += " Relevant output:\n" + truncateForError(strings.TrimSpace(output), 500)
	}
	return fmt.Errorf("%s", msg)
}

func runtimeFailureReason(pattern string) string {
	switch pattern {
	case "terminal prompts disabled", "could not read username", "could not read password":
		return "the command attempted an interactive credential prompt"
	case "cannot open /dev/tty", "no tty present and no askpass program specified", "the input device is not a tty", "inappropriate ioctl for device", "must be connected to a terminal", "requires a tty", "requires an interactive terminal", "stdin is not a terminal", "standard input is not a terminal", "not a tty", "not a terminal":
		return "the command requires a terminal/TTY"
	case "a terminal is required to read the password":
		return "the command attempted a password prompt without a terminal"
	case "terminal is dumb", "terminal is dumb, but editor requires a terminal", "there was a problem with the editor":
		return "the command attempted to launch an interactive editor"
	default:
		return "the command attempted terminal interaction"
	}
}

func commandHasSudoNonInteractive(command string) bool {
	tokens := shellTokens(command)
	commands := splitShellCommandTokens(tokens)
	for _, cmd := range commands {
		if len(cmd) == 0 || commandBase(cmd[0]) != "sudo" {
			continue
		}
		if hasOption(cmd[1:], "-n", "") || hasOption(cmd[1:], "", "--non-interactive") {
			return true
		}
	}
	return false
}
