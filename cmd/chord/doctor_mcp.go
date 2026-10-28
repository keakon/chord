package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
)

type doctorMCPReport struct {
	SchemaVersion int               `json:"schema_version"`
	Mode          string            `json:"mode"`
	CheckedAt     time.Time         `json:"checked_at"`
	DurationMS    int64             `json:"duration_ms"`
	Targets       []doctorMCPTarget `json:"targets"`
	OK            bool              `json:"ok"`
}
type doctorMCPTarget struct {
	ControlNetworkMode       string                     `json:"control_network_mode,omitempty"`
	ProxyEnvironmentDetected bool                       `json:"proxy_environment_detected,omitempty"`
	Ref                      mcp.ServerRef              `json:"ref"`
	Auth                     mcp.AuthState              `json:"auth"`
	Operations               []mcp.OperationView        `json:"operations"`
	Connection               mcp.ConnectionState        `json:"connection"`
	Catalog                  mcp.CatalogState           `json:"catalog"`
	ScopeRequirements        []mcp.ScopeRequirementView `json:"scope_requirements"`
	Stages                   []mcp.ProbeStage           `json:"stages"`
	RedactedError            string                     `json:"redacted_error,omitempty"`
	SuggestedAction          string                     `json:"suggested_action,omitempty"`
	OK                       bool                       `json:"ok"`
}

func newDoctorMCPCmd() *cobra.Command {
	var agentName string
	var connect, jsonOutput bool
	cmd := &cobra.Command{Use: "mcp [server]", Short: "Inspect MCP configuration and credentials; optionally connect", Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.MaximumNArgs(1)(cmd, args); err != nil {
			return mcpCommandError(err, 2)
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		serverName := ""
		if len(args) > 0 {
			serverName = args[0]
		}
		started := time.Now()
		targets, stateDir, err := loadMCPCommandConfig(cmd.Context(), agentName, serverName)
		if err != nil {
			return mcpCommandError(err, 2)
		}
		auth := mcp.NewAuthService(mcp.AuthOptions{StateDir: stateDir, ClientInfo: mcp.ClientInfo{Name: "chord", Version: Version}})
		defer auth.Close()
		report := doctorMCPReport{SchemaVersion: 1, Mode: "offline", CheckedAt: started.UTC(), Targets: make([]doctorMCPTarget, 0, len(targets)), OK: true}
		if connect {
			report.Mode = "connect"
		}
		exitCode := 0
		for _, target := range targets {
			checkStarted := time.Now()
			entry := doctorMCPTarget{Ref: target.Ref, Operations: []mcp.OperationView{}, Connection: mcp.ConnectionIdle, Catalog: mcp.CatalogUnknown, ScopeRequirements: []mcp.ScopeRequirementView{}, OK: true}
			for _, stage := range []string{mcp.StageConfig, mcp.StageCredential, mcp.StageTransport, mcp.StageDiscovery, mcp.StageRefresh, mcp.StageInitialize, mcp.StageToolsList} {
				entry.Stages = append(entry.Stages, mcp.ProbeStage{Stage: stage, State: mcp.ProbeSkipped})
			}
			entry.Stages[0] = mcp.ProbeStage{Stage: mcp.StageConfig, State: mcp.ProbeOK, StartedAt: checkStarted.UTC(), FinishedAt: time.Now().UTC()}
			missing := missingMCPHeaderEnvironment(target)
			view, inspectErr := auth.Inspect(cmd.Context(), target)
			entry.Auth = view.Auth
			entry.ScopeRequirements = view.ScopeRequirements
			credentialFinished := time.Now().UTC()
			entry.Stages[1] = mcp.ProbeStage{Stage: mcp.StageCredential, State: mcp.ProbeOK, StartedAt: checkStarted.UTC(), FinishedAt: credentialFinished, DurationMS: time.Since(checkStarted).Milliseconds()}
			diagnostic := view.Diagnostic
			if inspectErr != nil {
				entry.OK = false
				entry.Stages[1].State = mcp.ProbeFailed
				exitCode = max(exitCode, 1)
			}
			if target.Config.Auth == config.MCPAuthOAuth && view.Auth == mcp.AuthMissing {
				entry.OK = false
				diagnostic = mcp.Diagnostic{ErrorClass: mcp.ErrorAuthRequired, SuggestedAction: mcp.ActionLoginOnHost}
				entry.Stages[1].State = mcp.ProbeFailed
				exitCode = max(exitCode, 1)
			}
			if view.Auth == mcp.AuthReauthRequired || view.Auth == mcp.AuthError || view.Auth == mcp.AuthRefreshRequired {
				entry.OK = false
				entry.Stages[1].State = mcp.ProbeFailed
				exitCode = max(exitCode, 1)
				if diagnostic.ErrorClass == "" {
					diagnostic.ErrorClass = mcp.ErrorAuthRequired
					diagnostic.SuggestedAction = mcp.ActionLoginOnHost
				}
			}
			entry.Stages[1].ErrorClass = diagnostic.ErrorClass
			entry.Stages[1].SuggestedAction = diagnostic.SuggestedAction
			if len(missing) > 0 {
				entry.OK = false
				exitCode = 2
				diagnostic = mcp.Diagnostic{ErrorClass: mcp.ErrorProtocol, SuggestedAction: "check_header_environment"}
				entry.Stages[0].State = mcp.ProbeFailed
				entry.Stages[0].ErrorClass = diagnostic.ErrorClass
				entry.Stages[0].SuggestedAction = diagnostic.SuggestedAction
			} else if connect {
				result := auth.Probe(cmd.Context(), target, mcp.ProbeOptions{UseSavedCredential: true, AllowRefresh: true})
				entry.OK = result.OK
				entry.Auth = result.Auth
				entry.Connection = result.Connection
				entry.Catalog = result.Catalog
				entry.ScopeRequirements = result.ScopeRequirements
				entry.Stages = result.Stages
				diagnostic = result.Diagnostic
				if !entry.OK {
					exitCode = max(exitCode, 1)
				}
			}
			entry.ControlNetworkMode = diagnostic.ControlNetworkMode
			entry.ProxyEnvironmentDetected = diagnostic.ProxyEnvironmentDetected
			entry.RedactedError = string(diagnostic.ErrorClass)
			entry.SuggestedAction = diagnostic.SuggestedAction
			if !entry.OK {
				report.OK = false
			}
			report.Targets = append(report.Targets, entry)
		}
		report.DurationMS = time.Since(started).Milliseconds()
		if jsonOutput {
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
				return mcpCommandError(err, 1)
			}
		} else {
			if len(report.Targets) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No MCP servers are configured for this agent.")
			}
			for _, entry := range report.Targets {
				status := "ok"
				if !entry.OK {
					status = "failed"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s/%s: %s; auth=%s; connection=%s; catalog=%s\n", entry.Ref.Owner, entry.Ref.ServerName, status, entry.Auth, entry.Connection, entry.Catalog)
				if entry.RedactedError != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s", entry.RedactedError)
					if entry.SuggestedAction != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "; %s", entry.SuggestedAction)
					}
					fmt.Fprintln(cmd.OutOrStdout())
				}
			}
			if !connect && len(report.Targets) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Offline checks do not verify server acceptance. Use --connect to test initialize and tools/list; this may refresh saved credentials.")
			}
		}
		if err := cmd.Context().Err(); err != nil {
			return mcpCommandError(err, 1)
		}
		if !report.OK {
			return mcpCommandError(fmt.Errorf("one or more MCP diagnostics failed"), max(exitCode, 1))
		}
		return nil
	}}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return mcpCommandError(err, 2) })
	cmd.Flags().StringVar(&agentName, "agent", "", "Select an agent definition and its visible servers")
	cmd.Flags().BoolVar(&connect, "connect", false, "Connect and discover tools; may refresh saved credentials, never opens a browser")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print a structured diagnostic report")
	return cmd
}

// Match the runtime's dollar-prefixed header expansion without exposing values.
// The environment variable's name is diagnostic data; its value is never returned.
func missingMCPHeaderEnvironment(target mcp.Target) []string {
	missing := []string{}
	seen := map[string]bool{}
	for _, value := range target.Config.Headers {
		if !strings.HasPrefix(value, "$") {
			continue
		}
		os.Expand(value, func(name string) string {
			if os.Getenv(name) == "" && !seen[name] {
				seen[name] = true
				missing = append(missing, name)
			}
			return ""
		})
	}
	return missing
}
