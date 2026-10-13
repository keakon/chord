package mcp

import (
	"fmt"
	"time"

	"github.com/keakon/chord/internal/atomicfile"
)

const (
	CredentialEmpty                 = "empty"
	CredentialReady                 = "ready"
	CredentialReauthRequired        = "reauth_required"
	CredentialRefreshOutcomeUnknown = "refresh_outcome_unknown"
	CredentialLoggedOut             = "logged_out"
)

type CredentialFile struct {
	SchemaVersion int                        `json:"schema_version"`
	Target        CredentialTarget           `json:"target"`
	Slots         map[string]*CredentialSlot `json:"slots"`
}

type CredentialTarget struct {
	WorkspaceID string `json:"workspace_id"`
	Owner       string `json:"owner"`
	Endpoint    string `json:"endpoint"`
}

type CredentialSlot struct {
	Profile             credentialProfile          `json:"profile"`
	AuthGeneration      uint64                     `json:"auth_generation"`
	TokenRevision       uint64                     `json:"token_revision"`
	Status              string                     `json:"status"`
	Binding             *CredentialBinding         `json:"binding,omitempty"`
	Registration        *CredentialRegistration    `json:"registration,omitempty"`
	PendingRegistration *CredentialRegistration    `json:"pending_registration,omitempty"`
	Token               *CredentialToken           `json:"token,omitempty"`
	RejectedRevision    uint64                     `json:"rejected_revision,omitempty"`
	RefreshBlocked      *CredentialRefreshBlocked  `json:"refresh_blocked,omitempty"`
	RefreshInflight     *CredentialRefreshInflight `json:"refresh_inflight,omitempty"`
	ScopeRequirements   []ScopeRequirement         `json:"scope_requirements,omitempty"`
}

type CredentialBinding struct {
	BindingID       string `json:"binding_id"`
	Resource        string `json:"resource"`
	Issuer          string `json:"issuer"`
	ClientID        string `json:"client_id"`
	TokenAuthMethod string `json:"token_auth_method"`
	RedirectURI     string `json:"redirect_uri"`
}

type CredentialRegistration struct {
	Issuer          string    `json:"issuer"`
	ClientID        string    `json:"client_id"`
	ClientSecret    string    `json:"client_secret,omitempty"`
	SecretExpiresAt time.Time `json:"secret_expires_at,omitzero"`
	RedirectURI     string    `json:"redirect_uri"`
	TokenAuthMethod string    `json:"token_auth_method"`
	GrantTypes      []string  `json:"grant_types"`
	ResponseTypes   []string  `json:"response_types"`
	AuthGeneration  uint64    `json:"auth_generation,omitempty"`
}

type CredentialToken struct {
	AccessToken     string    `json:"access_token"`
	RefreshToken    string    `json:"refresh_token,omitempty"`
	IssuedAt        time.Time `json:"issued_at"`
	ExpiresAt       time.Time `json:"expires_at,omitzero"`
	LifetimeSeconds int64     `json:"lifetime_seconds,omitempty"`
	RequestedScopes []string  `json:"requested_scopes"`
	GrantedScopes   []string  `json:"granted_scopes"`
}

type CredentialRefreshBlocked struct {
	BlockID        string     `json:"block_id"`
	BindingID      string     `json:"binding_id"`
	AuthGeneration uint64     `json:"auth_generation"`
	TokenRevision  uint64     `json:"token_revision"`
	OAuthError     string     `json:"oauth_error"`
	ErrorClass     ErrorClass `json:"error_class"`
	BlockedAt      time.Time  `json:"blocked_at"`
}

type CredentialRefreshInflight struct {
	OperationID    string    `json:"operation_id"`
	AuthGeneration uint64    `json:"auth_generation"`
	TokenRevision  uint64    `json:"token_revision"`
	StartedAt      time.Time `json:"started_at"`
}

type ScopeRequirement struct {
	BindingID      string     `json:"binding_id"`
	AuthGeneration uint64     `json:"auth_generation"`
	TokenRevision  uint64     `json:"token_revision"`
	RemoteTool     string     `json:"remote_tool,omitempty"`
	Stage          string     `json:"stage,omitempty"`
	ErrorClass     ErrorClass `json:"error_class"`
	Scopes         []string   `json:"scopes"`
	ObservedAt     time.Time  `json:"observed_at"`
}

// CredentialStoreError deliberately contains no filesystem paths or raw bytes.
type CredentialStoreError struct {
	Class   ErrorClass
	Outcome atomicfile.ReplaceOutcome
}

func (e *CredentialStoreError) Error() string {
	return fmt.Sprintf("MCP credential store: %s (%s)", e.Class, e.Outcome)
}
