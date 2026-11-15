package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// promptProjectTrustAuthorization is the interactive disclosure and consent
// step for project-level configuration. It reuses the setup wizard's
// terminal handling so it can run before the TUI program owns the screen.
// There is no default consent: an empty answer re-prompts, EOF and errors
// never authorize.
func promptProjectTrustAuthorization(discovery *projectTrustDiscovery, contentRoot string) (bool, error) {
	termIO, err := openSetupTerminal(SetupWizardOptions{})
	if err != nil {
		return false, fmt.Errorf("project configuration needs authorization but no interactive terminal is available: %w", err)
	}
	defer func() {
		if termIO.closer != nil {
			_ = termIO.closer()
		}
	}()
	out := termIO.out
	if out == nil {
		out = io.Discard
	}

	fmt.Fprintln(out, "This directory contains project-level Chord configuration.")
	fmt.Fprintln(out, "Project configuration can start MCP servers (local commands), run")
	fmt.Fprintln(out, "shell hooks, redirect model providers to other endpoints, and add")
	fmt.Fprintln(out, "agents, commands, and skills. It applies only if you authorize it.")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Project configuration found in %s:\n", contentRoot)
	for _, source := range discovery.Sources {
		fmt.Fprintf(out, "  %s\n", source.File)
		for _, item := range source.Items {
			fmt.Fprintf(out, "    - %s\n", item)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Chord records what it authorized for this project; later changes ask again.")

	for {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  1) Authorize and apply this project configuration")
		fmt.Fprintln(out, "  2) Start without the project layer")
		line, err := promptLine(termIO, "Choice: ")
		if err != nil {
			if errors.Is(err, io.EOF) {
				// EOF is a cancellation, never an authorization.
				return false, nil
			}
			return false, err
		}
		switch strings.TrimSpace(line) {
		case "1":
			return true, nil
		case "2":
			return false, nil
		default:
			fmt.Fprintln(out, "Enter 1 to authorize or 2 to skip the project layer.")
		}
	}
}

// renderProjectTrustSources renders the disclosure as plain lines; the
// doctor command reuses it for the authorization status view.
func renderProjectTrustSources(out io.Writer, sources []projectTrustSource) {
	for _, source := range sources {
		fmt.Fprintf(out, "  %s\n", source.File)
		for _, item := range source.Items {
			fmt.Fprintf(out, "    - %s\n", item)
		}
	}
}

// projectTrustStatus describes the authorization state of one project root
// for doctor output.
type projectTrustStatus struct {
	ContentRoot string
	Canonical   string
	State       string // "no project inputs", "authorized", "not authorized"
	Fingerprint string
	GrantedAt   string
	StorePath   string
	Sources     []projectTrustSource
}

// inspectProjectTrustStatus re-runs static discovery and compares the
// fingerprint against the trust store. Read-only; used by doctor config.
func inspectProjectTrustStatus(contentRoot, workDir, globalConfigPath string) (*projectTrustStatus, error) {
	discovery, err := discoverProjectTrust(contentRoot, workDir)
	if err != nil {
		return nil, err
	}
	status := &projectTrustStatus{
		ContentRoot: contentRoot,
		Canonical:   canonicalProjectTrustRoot(contentRoot),
		Sources:     discovery.Sources,
	}
	if discovery.Inputs.Empty() {
		status.State = "no project inputs"
		return status, nil
	}
	status.Fingerprint = shortProjectTrustFingerprint(discovery.Inputs.Fingerprint())
	storePath, hasRecord, record, err := loadProjectTrustRecord(globalConfigPath, contentRoot)
	if err != nil {
		return nil, err
	}
	status.StorePath = storePath
	if hasRecord && record.Fingerprint == discovery.Inputs.Fingerprint() {
		status.State = "authorized"
		status.GrantedAt = formatProjectTrustTime(record.GrantedAtNs)
		return status, nil
	}
	if hasRecord {
		status.State = "not authorized (configuration changed since authorization)"
	} else {
		status.State = "not authorized"
	}
	return status, nil
}

func shortProjectTrustFingerprint(fingerprint string) string {
	if len(fingerprint) > 12 {
		return fingerprint[:12]
	}
	return fingerprint
}

func formatProjectTrustTime(unixNano int64) string {
	if unixNano <= 0 {
		return ""
	}
	return time.Unix(0, unixNano).Format("2006-01-02 15:04:05")
}
