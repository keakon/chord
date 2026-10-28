package mcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/config"
)

type CredentialVersion struct {
	BindingID      string
	AuthGeneration uint64
	TokenRevision  uint64
}
type ExpectedIdentity struct {
	BindingID      string
	AuthGeneration uint64
}
type BlockedRefreshStamp struct {
	CredentialVersion
	BlockID string
}
type RequestStamp struct {
	CredentialVersion
	ConnectionGeneration uint64
}

// CredentialLease contains a secret only in an unexported field. It must never
// be used as a diagnostic or marshalled as a credential record.
type CredentialLease struct {
	token   string
	version CredentialVersion
}

type CredentialView struct {
	Auth              AuthState              `json:"auth"`
	ExpiresAt         time.Time              `json:"expires_at,omitzero"`
	ScopeRequirements []ScopeRequirementView `json:"scope_requirements"`
	Diagnostic        Diagnostic             `json:"diagnostic"`
	Version           CredentialVersion      `json:"-"`
	Blocked           *BlockedRefreshStamp   `json:"-"`
}

type authFailure struct{ class ErrorClass }

func (e *authFailure) Error() string   { return "MCP authorization: " + string(e.class) }
func authError(class ErrorClass) error { return &authFailure{class: class} }

type AuthOptions struct {
	ClientInfo ClientInfo
	// StateDir roots the credential store; credentials live under <StateDir>/mcp/oauth.
	StateDir      string
	MCPHTTPClient *http.Client
	Clock         func() time.Time
	Random        io.Reader
	Store         *CredentialStore
	HTTPClient    *OAuthHTTPClient
	Discoverer    *OAuthDiscoverer
}

type refreshFlight struct {
	operation OperationView
	trace     *probeTrace
	done      chan struct{}
	err       error
}
type loginOperation struct {
	target     Target
	cancel     context.CancelFunc
	committing bool
}

type AuthService struct {
	clientInfo         ClientInfo
	targetGenerations  map[ServerRef]uint64
	mcpHTTPClient      *http.Client
	store              *CredentialStore
	http               *OAuthHTTPClient
	discoverer         *OAuthDiscoverer
	now                func() time.Time
	random             io.Reader
	ctx                context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	randomMu           sync.Mutex
	refreshObservers   map[uint64]func(Target, bool)
	refreshObserverSeq uint64
	flights            map[string]*refreshFlight
	logins             map[string]*loginOperation
	closed             bool
	wg                 sync.WaitGroup
}

func NewAuthService(opts AuthOptions) *AuthService {
	if opts.Store == nil {
		opts.Store = NewCredentialStore(opts.StateDir)
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = NewOAuthHTTPClient(OAuthHTTPOptions{})
	}
	defaultDiscovery := opts.Discoverer == nil
	if defaultDiscovery {
		opts.Discoverer = NewOAuthDiscoverer(opts.HTTPClient)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &AuthService{clientInfo: normalizeClientInfo(opts.ClientInfo), mcpHTTPClient: opts.MCPHTTPClient, store: opts.Store, http: opts.HTTPClient, discoverer: opts.Discoverer, now: opts.Clock, random: opts.Random, ctx: ctx, cancel: cancel, flights: map[string]*refreshFlight{}, logins: map[string]*loginOperation{}}
	if defaultDiscovery {
		service.discoverer.challenge = service.resourceChallenge
	}
	return service
}

func (s *AuthService) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	for _, op := range s.logins {
		op.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func targetKey(t Target) string { return t.TargetID + ":" + t.ProfileID }
func (s *AuthService) randomID() (string, error) {
	s.randomMu.Lock()
	defer s.randomMu.Unlock()
	b := make([]byte, 16)
	if _, err := io.ReadFull(s.random, b); err != nil {
		return "", authError(ErrorProtocol)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func slotVersion(slot *CredentialSlot) CredentialVersion {
	if slot == nil {
		return CredentialVersion{}
	}
	v := CredentialVersion{AuthGeneration: slot.AuthGeneration, TokenRevision: slot.TokenRevision}
	if slot.Binding != nil {
		v.BindingID = slot.Binding.BindingID
	}
	return v
}
func identityMatches(slot *CredentialSlot, expected ExpectedIdentity) bool {
	return slot != nil && slot.Binding != nil && slot.AuthGeneration == expected.AuthGeneration && slot.Binding.BindingID == expected.BindingID
}
func versionMatches(slot *CredentialSlot, v CredentialVersion) bool { return slotVersion(slot) == v }
func fileSlot(file *CredentialFile, t Target) *CredentialSlot {
	if file == nil {
		return nil
	}
	return file.Slots[t.ProfileID]
}

func managedAuth(t Target) bool {
	if t.Config.URL == "" || t.Config.Auth == config.MCPAuthNone {
		return false
	}
	for key := range t.Config.Headers {
		if strings.EqualFold(key, "Authorization") {
			return false
		}
	}
	return true
}
func (s *AuthService) Inspect(ctx context.Context, t Target) (CredentialView, error) {
	view := CredentialView{Auth: AuthNone, ScopeRequirements: []ScopeRequirementView{}}
	if t.Config.URL == "" {
		return view, nil
	}
	for key := range t.Config.Headers {
		if strings.EqualFold(key, "Authorization") {
			view.Auth = AuthStatic
			return view, nil
		}
	}
	if !managedAuth(t) {
		return view, nil
	}
	file, err := s.store.InspectFile(ctx, t)
	if err != nil {
		view.Auth = AuthError
		view.Diagnostic = diagnoseAuth(err)
		return view, err
	}
	return s.inspectSlot(fileSlot(file, t)), nil
}
func (s *AuthService) inspectSlot(slot *CredentialSlot) CredentialView {
	v := CredentialView{Auth: AuthMissing, ScopeRequirements: []ScopeRequirementView{}, Version: slotVersion(slot)}
	if slot == nil {
		return v
	}
	for _, r := range slot.ScopeRequirements {
		v.ScopeRequirements = append(v.ScopeRequirements, ScopeRequirementView{BindingID: r.BindingID, TokenRevision: r.TokenRevision, RemoteTool: r.RemoteTool, Stage: r.Stage, Scopes: slices.Clone(r.Scopes), ErrorClass: r.ErrorClass})
	}
	if slot.RefreshBlocked != nil {
		v.Blocked = &BlockedRefreshStamp{CredentialVersion: v.Version, BlockID: slot.RefreshBlocked.BlockID}
	}
	switch slot.Status {
	case CredentialReauthRequired:
		v.Auth = AuthReauthRequired
		return v
	case CredentialRefreshOutcomeUnknown:
		v.Auth = AuthReauthRequired
		v.Diagnostic.ErrorClass = ErrorRefreshOutcomeUnknown
		return v
	}
	if slot.RefreshInflight != nil {
		// A read-only snapshot can observe a live refresh holding the file
		// lock. Only Acquire can distinguish it from an interrupted writer.
		v.Auth = AuthRefreshRequired
		return v
	}
	if slot.Token == nil {
		return v
	}
	v.ExpiresAt = slot.Token.ExpiresAt
	v.Auth = AuthReady
	rejected := slot.RejectedRevision == slot.TokenRevision
	expired := !slot.Token.ExpiresAt.IsZero() && !s.now().Before(slot.Token.ExpiresAt)
	if rejected || expired {
		if slot.RefreshBlocked != nil {
			v.Auth = AuthError
			v.Diagnostic.ErrorClass = ErrorRefreshBlocked
		} else if slot.Token.RefreshToken == "" {
			v.Auth = AuthReauthRequired
		} else {
			v.Auth = AuthRefreshRequired
		}
	} else if needsRefresh(slot, s.now()) && slot.RefreshBlocked == nil && slot.Token.RefreshToken != "" {
		v.Auth = AuthRefreshRequired
	}
	return v
}

func diagnoseAuth(err error) (diagnostic Diagnostic) {
	defer func() {
		if transport, ok := errors.AsType[*HTTPError](err); ok && transport.SendEvidence != "" {
			diagnostic.SendEvidence = transport.SendEvidence
		}
		if diagnostic.SuggestedAction == "" {
			diagnostic.SuggestedAction = suggestedAuthAction(diagnostic.ErrorClass)
		}
	}()
	if e, ok := errors.AsType[*OAuthHTTPError](err); ok {
		return controlNetworkDiagnostic(Diagnostic{ErrorClass: e.ErrorClass, SendEvidence: e.SendEvidence})
	}
	if e, ok := errors.AsType[*authFailure](err); ok {
		return Diagnostic{ErrorClass: e.class}
	}
	if e, ok := errors.AsType[*CredentialStoreError](err); ok {
		return Diagnostic{ErrorClass: e.Class}
	}
	if e, ok := errors.AsType[*DiscoveryError](err); ok {
		return Diagnostic{ErrorClass: e.Class}
	}
	return DiagnoseHTTPError(err)
}

func (s *AuthService) Logout(ctx context.Context, t Target) (CredentialVersion, error) {
	if !managedAuth(t) {
		return CredentialVersion{}, authError(ErrorAuthRequired)
	}
	s.mu.Lock()
	if op := s.logins[targetKey(t)]; op != nil {
		op.cancel()
	}
	s.mu.Unlock()
	var version CredentialVersion
	err := s.store.UpdateLocked(ctx, t, func(tx *CredentialTransaction) error {
		old := fileSlot(tx.File, t)
		generation := uint64(1)
		if old != nil {
			if old.AuthGeneration == math.MaxUint64 {
				return authError(ErrorCredentialStoreFailed)
			}
			generation = old.AuthGeneration + 1
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		tx.File.Slots[t.ProfileID] = &CredentialSlot{Profile: profileForTarget(t), AuthGeneration: generation, Status: CredentialLoggedOut}
		_, err := tx.Commit()
		if err == nil {
			version = CredentialVersion{AuthGeneration: generation}
		}
		return err
	})
	return version, err
}

func (s *AuthService) RetryBlockedRefresh(ctx context.Context, t Target, stamp BlockedRefreshStamp) (bool, error) {
	changed := false
	err := s.store.UpdateLocked(ctx, t, func(tx *CredentialTransaction) error {
		slot := fileSlot(tx.File, t)
		if slot == nil || !versionMatches(slot, stamp.CredentialVersion) || slot.RefreshBlocked == nil || slot.RefreshBlocked.BlockID != stamp.BlockID || slot.RefreshInflight != nil || slot.Status == CredentialRefreshOutcomeUnknown {
			return nil
		}
		slot.RefreshBlocked = nil
		if _, err := tx.Commit(); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

type Challenge struct {
	StatusCode int
	Error      string
	Scope      string
	RemoteTool string
	Stage      string
}

func (s *AuthService) ObserveRejection(ctx context.Context, t Target, stamp RequestStamp, challenge Challenge) error {
	if challenge.StatusCode != 401 && (challenge.StatusCode != 403 || challenge.Error != "insufficient_scope") {
		return nil
	}
	return s.store.UpdateLocked(ctx, t, func(tx *CredentialTransaction) error {
		slot := fileSlot(tx.File, t)
		if slot == nil || !versionMatches(slot, stamp.CredentialVersion) || slot.Token == nil {
			return nil
		}
		if challenge.StatusCode == 401 {
			slot.RejectedRevision = slot.TokenRevision
		} else {
			scopes, err := splitOAuthScope(challenge.Scope)
			if err != nil {
				return authError(ErrorProtocol)
			}
			if len(scopes) > 64 {
				return authError(ErrorInsufficientScope)
			}
			found := -1
			for i, r := range slot.ScopeRequirements {
				if r.BindingID == stamp.BindingID && r.TokenRevision == stamp.TokenRevision && r.RemoteTool == challenge.RemoteTool && r.Stage == challenge.Stage {
					found = i
					break
				}
			}
			if found >= 0 {
				scopes = append(scopes, slot.ScopeRequirements[found].Scopes...)
				slices.Sort(scopes)
				scopes = slices.Compact(scopes)
			}
			bytes := 0
			for i, r := range slot.ScopeRequirements {
				if i != found {
					for _, v := range r.Scopes {
						bytes += len(v)
					}
				}
			}
			for _, v := range scopes {
				bytes += len(v)
			}
			if len(scopes) > 64 || bytes > 8<<10 || (found < 0 && len(slot.ScopeRequirements) >= 32) {
				return authError(ErrorInsufficientScope)
			}
			req := ScopeRequirement{BindingID: stamp.BindingID, AuthGeneration: stamp.AuthGeneration, TokenRevision: stamp.TokenRevision, RemoteTool: challenge.RemoteTool, Stage: challenge.Stage, Scopes: scopes, ErrorClass: ErrorInsufficientScope, ObservedAt: s.now().UTC()}
			if found >= 0 {
				slot.ScopeRequirements[found] = req
			} else {
				slot.ScopeRequirements = append(slot.ScopeRequirements, req)
			}
		}
		_, err := tx.Commit()
		if err == nil && challenge.StatusCode == 401 {
			s.discoverer.Invalidate(t)
		}
		return err
	})
}

func needsRefresh(slot *CredentialSlot, now time.Time) bool {
	if slot == nil || slot.Token == nil {
		return false
	}
	if slot.RejectedRevision == slot.TokenRevision {
		return true
	}
	if slot.Token.ExpiresAt.IsZero() {
		return false
	}
	margin := min(time.Duration(slot.Token.LifetimeSeconds)*time.Second/10, 60*time.Second)
	return !now.Before(slot.Token.ExpiresAt.Add(-margin))
}

func (s *AuthService) Acquire(ctx context.Context, t Target, expected ExpectedIdentity) (CredentialLease, error) {
	file, err := s.store.ReadForUse(ctx, t)
	if err != nil {
		return CredentialLease{}, err
	}
	slot := fileSlot(file, t)
	if !identityMatches(slot, expected) {
		return CredentialLease{}, authError(ErrorIdentityChanged)
	}
	if slot.RefreshInflight != nil || slot.Status == CredentialRefreshOutcomeUnknown {
		err := s.store.UpdateLocked(ctx, t, func(tx *CredentialTransaction) error {
			current := fileSlot(tx.File, t)
			if !identityMatches(current, expected) {
				return authError(ErrorIdentityChanged)
			}
			return markUnknownRefresh(tx, current)
		})
		return CredentialLease{}, err
	}
	if slot.Token == nil || slot.Status != CredentialReady {
		return CredentialLease{}, authError(ErrorAuthRequired)
	}
	if slot.RefreshBlocked != nil {
		if slot.RejectedRevision == slot.TokenRevision || (!slot.Token.ExpiresAt.IsZero() && !s.now().Before(slot.Token.ExpiresAt)) {
			return CredentialLease{}, authError(ErrorRefreshBlocked)
		}
	} else if needsRefresh(slot, s.now()) && (slot.Token.RefreshToken != "" || slot.RejectedRevision == slot.TokenRevision || (!slot.Token.ExpiresAt.IsZero() && !s.now().Before(slot.Token.ExpiresAt))) {
		if err := s.waitRefresh(ctx, t, expected, slotVersion(slot)); err != nil {
			return CredentialLease{}, err
		}
		file, err = s.store.ReadForUse(ctx, t)
		if err != nil {
			return CredentialLease{}, err
		}
		slot = fileSlot(file, t)
		if !identityMatches(slot, expected) {
			return CredentialLease{}, authError(ErrorIdentityChanged)
		}
		if slot.Token == nil || slot.Status != CredentialReady || slot.RefreshInflight != nil {
			return CredentialLease{}, authError(ErrorAuthRequired)
		}
	}
	// A different request may have rejected or blocked the refreshed revision
	// before this waiter reacquired the file lock. Never refresh twice here.
	if slot.RejectedRevision == slot.TokenRevision || (!slot.Token.ExpiresAt.IsZero() && !s.now().Before(slot.Token.ExpiresAt)) {
		if slot.RefreshBlocked != nil {
			return CredentialLease{}, authError(ErrorRefreshBlocked)
		}
		return CredentialLease{}, authError(ErrorInvalidToken)
	}
	return s.acquireVerifiedLease(ctx, t, expected, slot.Binding.Issuer)
}

func (s *AuthService) waitRefresh(ctx context.Context, t Target, expected ExpectedIdentity, version CredentialVersion) error {
	key := targetKey(t)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return context.Canceled
	}
	flight := s.flights[key]
	if flight == nil {
		flight = &refreshFlight{done: make(chan struct{}), trace: newProbeTrace(), operation: OperationView{OperationID: "refresh-" + rand.Text(), Kind: OperationRefresh, State: OperationRefreshing, StartedAt: time.Now().UTC()}}
		s.flights[key] = flight
		s.wg.Go(func() {
			refreshCtx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
			defer cancel()
			refreshCtx = context.WithValue(refreshCtx, probeTraceKey{}, flight.trace)
			s.notifyRefresh(t, false)
			started := time.Now()
			err := s.refresh(refreshCtx, t, expected, version)
			flight.trace.record(StageRefresh, started, err)
			s.mu.Lock()
			flight.err = err
			delete(s.flights, key)
			close(flight.done)
			s.mu.Unlock()
			s.notifyRefresh(t, true)
		})
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-flight.done:
		traceFromContext(ctx).merge(flight.trace)
		return flight.err
	}
}

// UpdateTargets installs the configuration generations owned by one runtime
// owner and cancels interactive operations whose configuration was replaced.
// CLI services need no installation: each command already owns one snapshot.
func (s *AuthService) UpdateTargets(owner string, targets []Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.targetGenerations == nil {
		s.targetGenerations = map[ServerRef]uint64{}
	}
	for ref := range s.targetGenerations {
		if ref.Owner == owner {
			delete(s.targetGenerations, ref)
		}
	}
	for _, target := range targets {
		if target.Ref.Owner == owner {
			s.targetGenerations[target.Ref] = target.ConfigGeneration
		}
	}
	for _, op := range s.logins {
		if op.target.Ref.Owner == owner && !s.targetCurrentLocked(op.target) {
			op.cancel()
		}
	}
}
func (s *AuthService) targetCurrentLocked(t Target) bool {
	if s.targetGenerations == nil {
		return true
	}
	generation, ok := s.targetGenerations[t.Ref]
	return ok && generation == t.ConfigGeneration
}
