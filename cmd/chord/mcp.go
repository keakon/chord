package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
)

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: "Inspect and authorize MCP connections"}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return mcpCommandError(err, 2) })
	cmd.AddCommand(newMCPListCmd(), newMCPLoginCmd(), newMCPLogoutCmd())
	return cmd
}

func mcpCommandArgs(count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(count)(cmd, args); err != nil {
			return cliExitError{code: 2, err: err}
		}
		return nil
	}
}

func newMCPListCmd() *cobra.Command {
	var agentName string
	var jsonOutput bool
	cmd := &cobra.Command{Use: "list", Short: "List visible MCP servers and saved credential status (offline)", Args: mcpCommandArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		targets, home, err := loadMCPCommandConfig(cmd.Context(), agentName, "")
		if err != nil {
			return mcpCommandError(err, 2)
		}
		auth := mcp.NewAuthService(mcp.AuthOptions{ConfigHome: home, ClientInfo: mcp.ClientInfo{Name: "chord", Version: Version}})
		defer auth.Close()
		entries := make([]mcpListEntry, 0, len(targets))
		failed := false
		for _, target := range targets {
			view, err := auth.Inspect(cmd.Context(), target)
			if err != nil {
				failed = true
			}
			entries = append(entries, mcpListEntry{Ref: target.Ref, Auth: view.Auth, Credential: view, Aliases: mcpTargetAliases(target, targets)})
		}
		if jsonOutput {
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				SchemaVersion int            `json:"schema_version"`
				Mode          string         `json:"mode"`
				Targets       []mcpListEntry `json:"targets"`
			}{1, "offline", entries}); err != nil {
				return mcpCommandError(err, 1)
			}
		} else {
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No MCP servers are configured for this agent.")
			}
			for _, entry := range entries {
				fmt.Fprintf(cmd.OutOrStdout(), "%s/%s  auth=%s", entry.Ref.Owner, entry.Ref.ServerName, entry.Auth)
				if entry.Credential.Diagnostic.ErrorClass != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s", entry.Credential.Diagnostic.ErrorClass)
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			if len(entries) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Offline credential status; live connection state is not checked.")
			}
		}
		if failed {
			return mcpCommandError(fmt.Errorf("one or more MCP credential records could not be inspected"), 1)
		}
		return nil
	}}
	cmd.Flags().StringVar(&agentName, "agent", "", "Select an agent definition and its visible servers")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")
	return cmd
}

type mcpListEntry struct {
	Ref        mcp.ServerRef      `json:"ref"`
	Auth       mcp.AuthState      `json:"auth"`
	Credential mcp.CredentialView `json:"credential"`
	Aliases    []mcp.ServerRef    `json:"aliases"`
}

func mcpTargetAliases(target mcp.Target, targets []mcp.Target) []mcp.ServerRef {
	result := []mcp.ServerRef{}
	if target.TargetID == "" {
		return result
	}
	for _, other := range targets {
		if other.Ref != target.Ref && other.TargetID == target.TargetID && other.ProfileID == target.ProfileID {
			result = append(result, other.Ref)
		}
	}
	return result
}

func newMCPLoginCmd() *cobra.Command {
	var agentName string
	var noBrowser, reauth bool
	cmd := &cobra.Command{Use: "login <server>", Short: "Authorize an HTTP MCP server on this host", Args: mcpCommandArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		targets, home, err := loadMCPCommandConfig(cmd.Context(), agentName, args[0])
		if err != nil {
			return mcpCommandError(err, 2)
		}
		target := targets[0]
		if err := requireManagedMCP(target); err != nil {
			return mcpCommandError(err, 2)
		}
		auth := mcp.NewAuthService(mcp.AuthOptions{ConfigHome: home, ClientInfo: mcp.ClientInfo{Name: "chord", Version: Version}})
		defer auth.Close()
		result, err := auth.Login(cmd.Context(), target, mcp.LoginOptions{Reauth: reauth}, func(progress mcp.LoginProgress) {
			if progress.AuthorizationURL == "" {
				return
			}
			if noBrowser {
				fmt.Fprintf(cmd.OutOrStdout(), "Open this URL in a browser on a host that can reach the callback listener:\n%s\n", progress.AuthorizationURL)
			} else if openBrowser(progress.AuthorizationURL) != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Could not open the browser. Open this URL to continue:\n%s\n", progress.AuthorizationURL)
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Browser opened; waiting for authorization.")
			}
		})
		if result.CredentialSaved {
			fmt.Fprintln(cmd.OutOrStdout(), "credential_saved: true")
		}
		if result.Validation.OK {
			fmt.Fprintln(cmd.OutOrStdout(), "Connection and tool catalog verified.")
		} else if result.CredentialSaved {
			fmt.Fprintf(cmd.OutOrStdout(), "Credentials are saved; connection validation failed: %s\n", result.Validation.Diagnostic.ErrorClass)
		}
		if err != nil {
			return mcpCommandError(err, 1)
		}
		if !result.Validation.OK {
			return mcpCommandError(fmt.Errorf("MCP connection validation failed"), 1)
		}
		return nil
	}}
	cmd.Flags().StringVar(&agentName, "agent", "", "Select an agent definition and its visible servers")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the authorization URL without opening a browser; still wait for local callback")
	cmd.Flags().BoolVar(&reauth, "reauth", false, "Start a new authorization even when credentials are already saved")
	return cmd
}

func newMCPLogoutCmd() *cobra.Command {
	var agentName string
	cmd := &cobra.Command{Use: "logout <server>", Short: "Remove a saved MCP login from this host", Args: mcpCommandArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		targets, home, err := loadMCPCommandConfig(cmd.Context(), agentName, args[0])
		if err != nil {
			return mcpCommandError(err, 2)
		}
		target := targets[0]
		if err := requireManagedMCP(target); err != nil {
			return mcpCommandError(err, 2)
		}
		auth := mcp.NewAuthService(mcp.AuthOptions{ConfigHome: home, ClientInfo: mcp.ClientInfo{Name: "chord", Version: Version}})
		defer auth.Close()
		if _, err := auth.Logout(cmd.Context(), target); err != nil {
			return mcpCommandError(err, 1)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Saved MCP credentials removed. Server enablement is unchanged.")
		return nil
	}}
	cmd.Flags().StringVar(&agentName, "agent", "", "Select an agent definition and its visible servers")
	return cmd
}

func requireManagedMCP(target mcp.Target) error {
	if target.Config.Command != "" || target.Config.URL == "" {
		return fmt.Errorf("OAuth login and logout require an HTTP MCP server")
	}
	if target.Config.Auth == config.MCPAuthNone {
		return fmt.Errorf("managed OAuth is disabled by auth: none")
	}
	for key := range target.Config.Headers {
		if strings.EqualFold(key, "Authorization") {
			return fmt.Errorf("this server uses a static Authorization header; remove it before using managed OAuth")
		}
	}
	return nil
}
func mcpCommandError(err error, code int) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return cliExitError{code: 130, err: context.Canceled}
	}
	return cliExitError{code: code, err: err}
}
