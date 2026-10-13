package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// connectionEntry is the sole authority for a server's published connection,
// complete catalog and admission generation. Manager.mu protects every field.
type connectionEntry struct {
	target      Target
	generation  uint64
	identity    ExpectedIdentity
	client      *Client
	definitions []MCPToolDef
	hashes      map[string]string
	allowed     map[string]struct{}
	report      ProbeResult
	leases      int
	retired     bool
	pending     bool
	attempt     int
}

type RetireReason string

const (
	RetireDisabled  RetireReason = "disabled"
	RetireReconnect RetireReason = "reconnect"
	RetireLogout    RetireReason = "logout"
	RetireConfig    RetireReason = "config"
	RetireSession   RetireReason = "session"
	RetireShutdown  RetireReason = "shutdown"
)

type Manager struct {
	reconcileGate      chan struct{}
	reconcileAttempts  map[string]credentialObservation
	mu                 sync.RWMutex
	entries            map[string]*connectionEntry
	retired            map[*connectionEntry]struct{}
	catalog            *Catalog
	auth               *AuthService
	clientInfo         ClientInfo
	newClientFactory   func(context.Context, ServerConfig) (*Client, error)
	ctx                context.Context
	cancel             context.CancelFunc
	closed             bool
	bindingID          string
	version            uint64
	onChange           func(RuntimeSnapshot)
	unsubscribeRefresh func()
}

func NewPendingManagerWithClientInfo(targets []Target, info ClientInfo, auth *AuthService) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	binding := make([]byte, 16)
	_, _ = rand.Read(binding)
	m := &Manager{entries: map[string]*connectionEntry{}, retired: map[*connectionEntry]struct{}{}, auth: auth, clientInfo: normalizeClientInfo(info), ctx: ctx, cancel: cancel, bindingID: hex.EncodeToString(binding), version: 1}
	for _, target := range targets {
		target = target.Snapshot()
		name := serverConfigName(target.Config)
		e := &connectionEntry{target: target, generation: 1, allowed: makeAllowedToolsSet(target.Config.AllowedTools), report: newProbeReport(target), pending: !target.Config.Manual}
		if target.Config.Manual {
			e.report.Connection = ConnectionDisabled
		}
		if target.Config.Command == "" && target.Config.URL == "" {
			e.pending = false
			e.report.Connection = ConnectionError
			e.report.Diagnostic.ErrorClass = ErrorProtocol
		}
		m.entries[name] = e
	}
	if auth != nil {
		m.unsubscribeRefresh = auth.subscribeRefresh(m.refreshOperationChanged)
	}
	return m
}

func NewManagerWithClientInfo(ctx context.Context, targets []Target, info ClientInfo, auth *AuthService) (*Manager, error) {
	m := NewPendingManagerWithClientInfo(targets, info, auth)
	m.ConnectAll(ctx, targets)
	return m, nil
}

func (m *Manager) Target(name string) (Target, bool) {
	if m == nil {
		return Target{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.entries[name]
	if e == nil {
		return Target{}, false
	}
	return e.target.Snapshot(), true
}

func (m *Manager) desiredLocked(name string) bool {
	e := m.entries[name]
	if e == nil {
		return false
	}
	if !e.target.Config.Manual {
		return true
	}
	if m.catalog == nil {
		return false
	}
	_, enabled := m.catalog.desired[name]
	return enabled
}

func (m *Manager) retireLocked(name string, reason RetireReason) *Client {
	old := m.entries[name]
	if old == nil {
		return nil
	}
	old.retired = true
	next := &connectionEntry{target: old.target, generation: old.generation + 1, identity: old.identity, definitions: old.definitions, hashes: old.hashes, allowed: old.allowed, report: old.report}
	next.report.OK = false
	next.report.Connection = ConnectionDisconnected
	if len(old.definitions) > 0 || old.report.Catalog == CatalogReady {
		next.report.Catalog = CatalogStale
	}
	if reason == RetireDisabled && old.target.Config.Manual {
		next.report.Connection = ConnectionDisabled
	}
	m.entries[name] = next
	if old.client == nil {
		return nil
	}
	if old.leases > 0 {
		m.retired[old] = struct{}{}
		return nil
	}
	client := old.client
	old.client = nil
	return client
}

func closeMCPClient(client *Client) {
	if client != nil {
		_ = client.Close()
	}
}

func (m *Manager) RetireConnection(name string, reason RetireReason) {
	if m == nil {
		return
	}
	m.mu.Lock()
	client := m.retireLocked(strings.TrimSpace(name), reason)
	m.version++
	m.mu.Unlock()
	closeMCPClient(client)
	m.publish()
}

// BuildCandidate performs all network work outside the publication lock.
func (m *Manager) BuildCandidate(ctx context.Context, target Target, ticket ConnectionTicket) CandidateResult {
	return probeConnection(ctx, target, m.auth, m.clientInfo, ticket, ProbeOptions{UseSavedCredential: true, AllowRefresh: true}, m.newClientFactory)
}

// InstallCandidate publishes client, identity and complete tool schemas in one
// critical section. Rejected candidates cannot overwrite current status.
func (m *Manager) InstallCandidate(ticket ConnectionTicket, result CandidateResult) error {
	name := ticket.Ref.ServerName
	target, known := m.Target(name)
	if !known {
		closeMCPClient(result.Client)
		return authError(ErrorIdentityChanged)
	}
	definitions := cloneToolDefinitions(filterToolDefsByAllowed(result.Definitions, makeAllowedToolsSet(target.Config.AllowedTools)))
	hashes := make(map[string]string, len(definitions))
	for _, def := range definitions {
		hashes[def.Name] = toolSchemaHash(def)
	}
	installed := false
	install := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		entry := m.entries[name]
		if m.closed || entry == nil || entry.generation != ticket.ConnectionGeneration || entry.target.ConfigGeneration != ticket.ConfigGeneration || entry.target.Ref != ticket.Ref || !m.desiredLocked(name) {
			return
		}
		entry.identity = result.Identity
		if result.Client != nil {
			if transport, ok := result.Client.transport.(*HTTPTransport); ok {
				transport.onAuthResponse = func(ctx context.Context, stamp RequestStamp, err error) {
					m.observeTransportRejection(ctx, ticket, stamp, err)
				}
			}
		}
		entry.client = result.Client
		entry.report = result.Report
		entry.pending = errors.Is(result.Err, context.Canceled)
		entry.attempt = result.Attempts
		entry.definitions = nil
		entry.hashes = nil
		if result.Report.Catalog == CatalogReady {
			entry.definitions = definitions
			entry.hashes = hashes
		}
		m.version++
		installed = true
	}
	if managedAuth(target) && m.auth != nil {
		// File-lock -> publication-lock is the only lock order. The callback does
		// no I/O; logout cannot commit between identity validation and publication.
		err := m.auth.store.UpdateLocked(m.ctx, target, func(tx *CredentialTransaction) error {
			slot := fileSlot(tx.File, target)
			current := slotVersion(slot)
			if current.BindingID != result.Identity.BindingID || current.AuthGeneration != result.Identity.AuthGeneration {
				return authError(ErrorIdentityChanged)
			}
			view := m.auth.inspectSlot(slot)
			result.Report.Auth = view.Auth
			result.Report.ScopeRequirements = view.ScopeRequirements
			install()
			return nil
		})
		if err != nil {
			closeMCPClient(result.Client)
			return err
		}
	} else {
		install()
	}
	if !installed {
		closeMCPClient(result.Client)
		return authError(ErrorIdentityChanged)
	}
	m.publish()
	return nil
}

func (m *Manager) ConnectOne(ctx context.Context, target Target) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil {
		return nil
	}
	name := serverConfigName(target.Config)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return context.Canceled
	}
	old := m.entries[name]
	if old == nil {
		old = &connectionEntry{target: target.Snapshot(), report: newProbeReport(target)}
		m.entries[name] = old
	}
	client := m.retireLocked(name, RetireReconnect)
	e := m.entries[name]
	e.target = target.Snapshot()
	e.allowed = makeAllowedToolsSet(target.Config.AllowedTools)
	if !m.desiredLocked(name) {
		e.pending = false
		e.report.Connection = ConnectionDisabled
		m.version++
		m.mu.Unlock()
		closeMCPClient(client)
		m.publish()
		return fmt.Errorf("MCP server %q is disabled", name)
	}
	e.pending = true
	e.report.Connection = ConnectionConnecting
	e.report.Catalog = CatalogLoading
	ticket := ConnectionTicket{Ref: target.Ref, ConfigGeneration: target.ConfigGeneration, ConnectionGeneration: e.generation}
	m.version++
	m.mu.Unlock()
	closeMCPClient(client)
	m.publish()
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	defer cancel()
	result := m.BuildCandidate(callCtx, target, ticket)
	if err := m.InstallCandidate(ticket, result); err != nil {
		m.settleDiscardedCandidate(ticket, err)
		return err
	}
	return result.Err
}

func (m *Manager) ConnectAll(ctx context.Context, targets []Target) {
	if m == nil {
		return
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		if target.Config.Manual {
			m.mu.RLock()
			enabled := m.desiredLocked(serverConfigName(target.Config))
			m.mu.RUnlock()
			if !enabled {
				continue
			}
		}
		_ = m.ConnectOne(ctx, target)
	}
}

// HandleIdentity pins the catalog identity visible to the originating model.
type HandleIdentity struct {
	ServerName string
	TargetID   string
	ProfileID  string
	ExpectedIdentity
}

type ConnectionLease struct {
	manager *Manager
	entry   *connectionEntry
	Client  *Client
	once    sync.Once
}

func (lease *ConnectionLease) Release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		m := lease.manager
		m.mu.Lock()
		e := lease.entry
		e.leases--
		var client *Client
		if e.retired && e.leases == 0 {
			delete(m.retired, e)
			client = e.client
			e.client = nil
		}
		m.mu.Unlock()
		closeMCPClient(client)
	})
}
func (m *Manager) AcquireTool(identity HandleIdentity, toolName, schemaHash string) (*ConnectionLease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, context.Canceled
	}
	e := m.entries[identity.ServerName]
	if !m.desiredLocked(identity.ServerName) {
		return nil, fmt.Errorf("MCP server %q is disabled", identity.ServerName)
	}
	if e == nil || e.retired || e.client == nil || e.report.Catalog != CatalogReady {
		return nil, fmt.Errorf("MCP server %q is not connected", identity.ServerName)
	}
	if e.target.TargetID != identity.TargetID || e.target.ProfileID != identity.ProfileID || e.identity != identity.ExpectedIdentity {
		return nil, authError(ErrorIdentityChanged)
	}
	if hash, ok := e.hashes[toolName]; !ok || hash != schemaHash {
		return nil, authError(ErrorCatalogChanged)
	}
	if len(e.allowed) > 0 {
		if _, ok := e.allowed[toolName]; !ok {
			return nil, fmt.Errorf("MCP tool %s/%s is not allowed", identity.ServerName, toolName)
		}
	}
	e.leases++
	return &ConnectionLease{manager: m, entry: e, Client: e.client}, nil
}

func cloneToolDefinitions(defs []MCPToolDef) []MCPToolDef {
	if defs == nil {
		return nil
	}
	data, err := json.Marshal(defs)
	if err != nil {
		return nil
	}
	var copy []MCPToolDef
	if json.Unmarshal(data, &copy) != nil {
		return nil
	}
	return copy
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.cancel()
	var clients []*Client
	for name := range m.entries {
		if c := m.retireLocked(name, RetireShutdown); c != nil {
			clients = append(clients, c)
		}
	}
	for e := range m.retired {
		if e.client != nil {
			clients = append(clients, e.client)
			e.client = nil
		}
	}
	m.version++
	m.mu.Unlock()
	if m.unsubscribeRefresh != nil {
		m.unsubscribeRefresh()
	}
	for _, client := range clients {
		closeMCPClient(client)
	}
	m.publish()
}

func (m *Manager) serverNamesLocked() []string {
	names := make([]string, 0, len(m.entries))
	for name := range m.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (m *Manager) IsManualServer(name string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.entries[name]
	return e != nil && e.target.Config.Manual
}
func (m *Manager) IsDeferredServer(name string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.entries[name]
	return e != nil && e.target.Config.Deferred
}
func (m *Manager) CachedToolDefs(name string) []MCPToolDef {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.entries[name]
	if e == nil {
		return nil
	}
	return cloneToolDefinitions(e.definitions)
}
func (m *Manager) KnownRegisteredToolNames(name string) []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.entries[name]
	if e == nil {
		return nil
	}
	var names []string
	if len(e.definitions) > 0 {
		for _, def := range e.definitions {
			if def.Name != "" {
				names = append(names, RegisteredMCPToolName(name, def.Name))
			}
		}
	} else {
		for tool := range e.allowed {
			names = append(names, RegisteredMCPToolName(name, tool))
		}
	}
	slices.Sort(names)
	return names
}
func (m *Manager) Client(name string) *Client {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.entries[name]
	if e == nil || e.retired {
		return nil
	}
	return e.client
}
func (m *Manager) Clients() map[string]*Client {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := map[string]*Client{}
	for name, e := range m.entries {
		if e.client != nil && !e.retired {
			result[name] = e.client
		}
	}
	return result
}
func (m *Manager) ServerNames() []string {
	clients := m.Clients()
	var names []string
	for name := range clients {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func toolSchemaHash(def MCPToolDef) string {
	data, err := json.Marshal(def)
	if err != nil {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var declaration any
	if decoder.Decode(&declaration) != nil {
		return ""
	}
	return identityHash(declaration)
}

// InvalidateCandidates prevents work started in a previous session from
// publishing while leaving already admitted connections untouched.
func (m *Manager) InvalidateCandidates() {
	if m == nil {
		return
	}
	m.mu.Lock()
	changed := false
	for _, entry := range m.entries {
		if !entry.pending {
			continue
		}
		entry.generation++
		entry.pending = false
		entry.report.Connection = ConnectionIdle
		entry.report.Catalog = CatalogUnknown
		if len(entry.definitions) > 0 {
			entry.report.Catalog = CatalogStale
		}
		changed = true
	}
	if changed {
		m.version++
	}
	m.mu.Unlock()
	if changed {
		m.publish()
	}
}

// A credential identity can change in another process without changing the
// local ticket. Release only this ticket's pending state; newer attempts own
// their readiness independently.
func (m *Manager) settleDiscardedCandidate(ticket ConnectionTicket, err error) {
	m.mu.Lock()
	entry := m.entries[ticket.Ref.ServerName]
	changed := entry != nil && entry.pending && entry.generation == ticket.ConnectionGeneration && entry.target.ConfigGeneration == ticket.ConfigGeneration && entry.target.Ref == ticket.Ref
	if changed {
		entry.pending = false
		entry.report.Diagnostic = diagnoseAuth(err)
		entry.report.Connection = ConnectionDisconnected
		if entry.report.Diagnostic.ErrorClass != ErrorIdentityChanged {
			entry.report.Connection = ConnectionError
		}
		entry.report.Catalog = CatalogUnknown
		if len(entry.definitions) > 0 {
			entry.report.Catalog = CatalogStale
		}
		m.version++
	}
	m.mu.Unlock()
	if changed {
		m.publish()
	}
}
