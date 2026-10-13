package mcp

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ErrorClass is the stable, secret-free classification used by MCP diagnostics.
type ErrorClass string

const (
	ErrorAuthRequired           ErrorClass = "auth_required"
	ErrorInvalidToken           ErrorClass = "invalid_token"
	ErrorInsufficientScope      ErrorClass = "insufficient_scope"
	ErrorAccessDenied           ErrorClass = "access_denied"
	ErrorDiscoveryFailed        ErrorClass = "discovery_failed"
	ErrorResourceMismatch       ErrorClass = "resource_mismatch"
	ErrorIssuerMismatch         ErrorClass = "issuer_mismatch"
	ErrorPKCEUnsupported        ErrorClass = "pkce_unsupported"
	ErrorRegistrationFailed     ErrorClass = "registration_failed"
	ErrorCallbackUnavailable    ErrorClass = "callback_unavailable"
	ErrorAuthorizationDenied    ErrorClass = "authorization_denied"
	ErrorRefreshInvalid         ErrorClass = "refresh_invalid"
	ErrorRefreshBlocked         ErrorClass = "refresh_blocked"
	ErrorRefreshOutcomeUnknown  ErrorClass = "refresh_outcome_unknown"
	ErrorCredentialStoreFailed  ErrorClass = "credential_store_failed"
	ErrorCredentialStoreCorrupt ErrorClass = "credential_store_corrupt"
	ErrorIdentityChanged        ErrorClass = "identity_changed"
	ErrorCatalogChanged         ErrorClass = "catalog_changed"
	ErrorEndpointRedirect       ErrorClass = "endpoint_redirect"
	ErrorNetworkPolicyDenied    ErrorClass = "network_policy_denied"
	ErrorNetwork                ErrorClass = "network"
	ErrorTLS                    ErrorClass = "tls"
	ErrorProtocol               ErrorClass = "protocol"
	ErrorToolDiscoveryFailed    ErrorClass = "tool_discovery_failed"
	ErrorCancelled              ErrorClass = "cancelled"
	ErrorHostLoginRequired      ErrorClass = "host_login_required"
)

type AuthState string

const (
	AuthNone            AuthState = "none"
	AuthStatic          AuthState = "static"
	AuthMissing         AuthState = "missing"
	AuthReady           AuthState = "ready"
	AuthRefreshRequired AuthState = "refresh_required"
	AuthReauthRequired  AuthState = "reauth_required"
	AuthError           AuthState = "error"
)

type ConnectionState string

const (
	ConnectionDisabled     ConnectionState = "disabled"
	ConnectionIdle         ConnectionState = "idle"
	ConnectionConnecting   ConnectionState = "connecting"
	ConnectionConnected    ConnectionState = "connected"
	ConnectionDisconnected ConnectionState = "disconnected"
	ConnectionError        ConnectionState = "error"
)

type CatalogState string

const (
	CatalogUnknown CatalogState = "unknown"
	CatalogLoading CatalogState = "loading"
	CatalogReady   CatalogState = "ready"
	CatalogStale   CatalogState = "stale"
	CatalogError   CatalogState = "error"
)

type OperationKind string

const (
	OperationLogin   OperationKind = "login"
	OperationRefresh OperationKind = "refresh"
	OperationProbe   OperationKind = "probe"
)

type OperationState string

const (
	OperationDiscovering      OperationState = "discovering"
	OperationRegistering      OperationState = "registering"
	OperationAwaitingCallback OperationState = "awaiting_callback"
	OperationExchanging       OperationState = "exchanging"
	OperationCommitting       OperationState = "committing"
	OperationRefreshing       OperationState = "refreshing"
	OperationValidating       OperationState = "validating"
)

// Diagnostic deliberately excludes raw errors, challenge URLs, and credentials.
type Diagnostic struct {
	ErrorClass               ErrorClass   `json:"error_class,omitempty"`
	SuggestedAction          string       `json:"suggested_action,omitempty"`
	SendEvidence             SendEvidence `json:"send_evidence,omitempty"`
	ControlNetworkMode       string       `json:"control_network_mode,omitempty"`
	ProxyEnvironmentDetected bool         `json:"proxy_environment_detected,omitempty"`
}

type OperationView struct {
	OperationID string         `json:"operation_id"`
	Kind        OperationKind  `json:"kind"`
	State       OperationState `json:"state"`
	StartedAt   time.Time      `json:"started_at"`
}

const (
	ActionLoginOnHost              = "login_on_host"
	ActionCheckServerConfiguration = "check_server_configuration"
	ActionCheckCredentialStore     = "check_credential_store"
	ActionCheckCallbackPort        = "check_callback_port"
	ActionCheckDirectConnectivity  = "check_direct_connectivity"
	ActionCheckAccess              = "check_access"
	ActionGrantScope               = "grant_scope"
	ControlNetworkDirect           = "direct"
)

// DiagnoseHTTPError never infers an HTTP status from text or a JSON-RPC code.
func DiagnoseHTTPError(err error) Diagnostic {
	if err == nil {
		return Diagnostic{}
	}
	if errors.Is(err, context.Canceled) {
		return Diagnostic{ErrorClass: ErrorCancelled}
	}
	d := Diagnostic{ErrorClass: ErrorNetwork}
	e, ok := errors.AsType[*HTTPError](err)
	if !ok {
		return d
	}
	d.SendEvidence = e.SendEvidence
	switch {
	case e.SafeSummary == httpInvalidChallengeSummary || e.SafeSummary == httpInvalidProtocolSummary:
		d.ErrorClass = ErrorProtocol
	case e.StatusCode == http.StatusUnauthorized:
		d.ErrorClass = ErrorAuthRequired
		d.SuggestedAction = ActionLoginOnHost
		for _, c := range e.BearerChallenges {
			if c.Error == "invalid_token" {
				d.ErrorClass = ErrorInvalidToken
				break
			}
		}
	case e.StatusCode == http.StatusForbidden:
		d.ErrorClass = ErrorAccessDenied
		d.SuggestedAction = ActionCheckAccess
		for _, c := range e.BearerChallenges {
			if c.Error == "insufficient_scope" {
				d.ErrorClass = ErrorInsufficientScope
				d.SuggestedAction = ActionGrantScope
				break
			}
		}
	case e.StatusCode >= 300 && e.StatusCode < 400:
		d.ErrorClass = ErrorEndpointRedirect
	case e.StatusCode != 0:
		d.ErrorClass = ErrorProtocol
	}
	return d
}

// ScopeRequirementView contains only explicit authorization requirements, never
// the business arguments which caused the server to return them.
type ScopeRequirementView struct {
	BindingID     string     `json:"binding_id"`
	TokenRevision uint64     `json:"token_revision"`
	RemoteTool    string     `json:"remote_tool,omitempty"`
	Stage         string     `json:"stage,omitempty"`
	Scopes        []string   `json:"scopes"`
	ErrorClass    ErrorClass `json:"error_class"`
}

// ServerView is the public status of one owner-visible MCP server.
type ServerView struct {
	Stages            []ProbeStage           `json:"stages"`
	Aliases           []string               `json:"aliases"`
	Ref               ServerRef              `json:"ref"`
	DesiredEnabled    bool                   `json:"desired_enabled"`
	Auth              AuthState              `json:"auth"`
	Operations        []OperationView        `json:"operations"`
	Connection        ConnectionState        `json:"connection"`
	Catalog           CatalogState           `json:"catalog"`
	ScopeRequirements []ScopeRequirementView `json:"scope_requirements"`
	Diagnostic        Diagnostic             `json:"diagnostic"`
}

const RuntimeSnapshotSchemaVersion = 1

// RuntimeSnapshot is a complete immutable wire view, not a credential snapshot.
type RuntimeSnapshot struct {
	SchemaVersion int          `json:"schema_version"`
	BindingID     string       `json:"binding_id"`
	Version       uint64       `json:"version"`
	Servers       []ServerView `json:"servers"`
}

// Clone keeps caller-owned caches independent of the runtime status slices.
func (s RuntimeSnapshot) Clone() RuntimeSnapshot {
	out := s
	out.Servers = append([]ServerView{}, s.Servers...)
	for i := range out.Servers {
		server := &out.Servers[i]
		server.Stages = append([]ProbeStage{}, server.Stages...)
		server.Aliases = append([]string{}, server.Aliases...)
		server.Operations = append([]OperationView{}, server.Operations...)
		server.ScopeRequirements = append([]ScopeRequirementView{}, server.ScopeRequirements...)
		for j := range server.ScopeRequirements {
			req := &server.ScopeRequirements[j]
			req.Scopes = append([]string{}, req.Scopes...)
		}
	}
	return out
}
