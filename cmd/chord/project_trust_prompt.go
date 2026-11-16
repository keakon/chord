package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
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
	return promptProjectTrustOnTerminal(termIO, discovery, contentRoot)
}

func promptProjectTrustOnTerminal(termIO *setupTerminal, discovery *projectTrustDiscovery, contentRoot string) (bool, error) {
	out := termIO.out
	if out == nil {
		out = io.Discard
	}

	renderProjectTrustAuthorization(out, discovery, contentRoot)

	showInspect := projectTrustHasMoreDetails(discovery)
	allowed := map[byte]struct{}{'1': {}, '2': {}}
	invalidMessage := "Enter 1 to apply, 2 to skip."
	if showInspect {
		allowed['3'] = struct{}{}
		invalidMessage = "Enter 1 to apply, 2 to skip, or 3 to inspect the files."
	}

	for {
		// Single-key fast path on real terminals: "1"/"2"/"3" submit without
		// Enter. Esc is ignored here (unlike the setup wizard, which reboots).
		value, handled, err := withSingleKeyTerminal(termIO, func(reader *bufio.Reader, w io.Writer) (string, error) {
			if _, err := fmt.Fprint(w, "Choice: "); err != nil {
				return "", err
			}
			return selectChoiceBySingleKey(reader, w, "Choice: ", allowed, 0, invalidMessage, false, func(timeout time.Duration) (bool, error) {
				return waitSetupInput(termIO.in, timeout)
			})
		})
		if handled {
			if err != nil {
				if errors.Is(err, io.EOF) {
					// EOF is a cancellation, never an authorization.
					return false, nil
				}
				return false, err
			}
			switch value {
			case "1":
				return true, nil
			case "2":
				return false, nil
			case "3":
				renderProjectTrustSources(out, discovery.Sources)
				renderProjectTrustConfig(out, discovery.ConfigData)
				continue
			}
		}

		// Line-based fallback: piped input, tests, and terminals without raw mode.
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
		case "3":
			if showInspect {
				renderProjectTrustSources(out, discovery.Sources)
				renderProjectTrustConfig(out, discovery.ConfigData)
			} else {
				fmt.Fprintln(out, invalidMessage)
			}
		default:
			fmt.Fprintln(out, invalidMessage)
		}
	}
}

const projectTrustPreviewLimit = 4

func renderProjectTrustAuthorization(out io.Writer, discovery *projectTrustDiscovery, contentRoot string) {
	fmt.Fprintf(out, "Project configuration found in %s:\n", projectTrustDisplayText(contentRoot))
	total := 0
	for _, source := range discovery.Sources {
		for _, item := range source.Items {
			if total < projectTrustPreviewLimit {
				fmt.Fprintf(out, "  - %s\n", projectTrustDisplayText(item))
			}
			total++
		}
	}
	if total > projectTrustPreviewLimit {
		fmt.Fprintf(out, "  ... and %d more entries (choose 3 to inspect all files).\n", total-projectTrustPreviewLimit)
	}

	if effects := projectTrustEffects(discovery.Sources); len(effects) > 0 {
		fmt.Fprintln(out)
		for _, effect := range effects {
			fmt.Fprintf(out, "  - %s\n", projectTrustDisplayText(effect))
		}
	}
	if discovery.Risk != nil && len(discovery.Risk.Hazards) > 0 {
		fmt.Fprintln(out, "\nChanges that need your approval:")
		for _, hazard := range discovery.Risk.Hazards {
			fmt.Fprintf(out, "  - %s\n", projectTrustDisplayText(hazard))
		}
	}
	if discovery.Risk != nil && discovery.Risk.Unreadable {
		fmt.Fprintln(out, "\nSome declarations could not be read and may hide changes; they require approval.")
	}

	fmt.Fprintln(out, "\nRepository prompt files such as AGENTS.md are shown to the model as guidance and are not covered by this gate; review them like source code.")
	fmt.Fprintln(out, "Apply only if you trust this project's configuration.")
	fmt.Fprintln(out, "  1) Apply and remember this configuration")
	fmt.Fprintln(out, "     Changes to these declaration files will ask again.")
	fmt.Fprintln(out, "  2) Skip project configuration for this run")
	fmt.Fprintln(out, "     Start with global settings; project agents, commands, and skills will not load.")
	if projectTrustHasMoreDetails(discovery) {
		fmt.Fprintln(out, "  3) Inspect all files and configuration details")
	}
}

// projectTrustHasMoreDetails reports whether choice 3 would reveal anything
// the preview above did not already show. Hiding it in the common
// single-source, few-items case avoids offering a no-op "inspect" step.
func projectTrustHasMoreDetails(discovery *projectTrustDiscovery) bool {
	if len(discovery.ConfigData) > 0 || len(discovery.Sources) > 1 {
		return true
	}
	total := 0
	for _, source := range discovery.Sources {
		total += len(source.Items)
	}
	return total > projectTrustPreviewLimit
}

// renderProjectTrustConfig retains configuration structure while hiding
// credentials and executable text before writing to the terminal.
func renderProjectTrustConfig(out io.Writer, data []byte) {
	if len(data) == 0 {
		return
	}
	fmt.Fprintln(out, "Project config declaration:")
	for line := range strings.SplitSeq(strings.TrimRight(redactedProjectConfig(data), "\n"), "\n") {
		fmt.Fprintf(out, "    %s\n", projectTrustDisplayText(line))
	}
}

func projectTrustEffects(sources []projectTrustSource) []string {
	seen := make(map[string]struct{})
	var effects []string
	for _, source := range sources {
		for _, effect := range source.Effects {
			if _, ok := seen[effect]; ok {
				continue
			}
			seen[effect] = struct{}{}
			effects = append(effects, effect)
		}
	}
	return effects
}

// renderProjectTrustSources renders the disclosure as plain lines; the
// doctor command reuses it for the authorization status view.
func renderProjectTrustSources(out io.Writer, sources []projectTrustSource) {
	for _, source := range sources {
		fmt.Fprintf(out, "  %s\n", projectTrustDisplayText(source.File))
		for _, item := range source.Items {
			fmt.Fprintf(out, "    - %s\n", projectTrustDisplayText(item))
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
	globalData, readErr := os.ReadFile(globalConfigPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read global config: %w", readErr)
	}
	if readErr != nil {
		globalData = nil
	}
	resolved, err := config.LoadResolvedConfigData(globalConfigPath, globalData, "", nil)
	if err != nil {
		return nil, err
	}
	storePath, hasRecord, record, err := loadProjectTrustRecord(resolved.Global, contentRoot)
	if err != nil {
		return nil, err
	}
	status.StorePath = storePath
	if hasRecord && record.Fingerprint == discovery.Inputs.Fingerprint() {
		status.State = "authorized"
		status.GrantedAt = formatProjectTrustTime(record.GrantedAtNs)
		return status, nil
	}

	// A hazard-free delta starts without an authorization record. Report that
	// accurately instead of showing a project that runs as "not authorized".
	risk, err := assessProjectTrustRisk(globalConfigPath, globalData, contentRoot, workDir, discovery)
	if err != nil {
		return nil, err
	}
	if !risk.requiresTrust() {
		status.State = "applied (low-risk, no confirmation required)"
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
