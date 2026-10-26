package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func TestHeadlessBareModelsSendMapsToStatus(t *testing.T) {
	state := &headlessState{}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "send", Content: "/models"}, backend, state, to.writer())

	backend.mu.Lock()
	msgs := append([]string(nil), backend.sentMessages...)
	backend.mu.Unlock()
	if len(msgs) != 1 || msgs[0] != "/models status" {
		t.Fatalf("sent messages = %v, want [/models status]", msgs)
	}
}

func TestHeadlessBareRoleSendMapsToStatus(t *testing.T) {
	state := &headlessState{}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "send", Content: "/role"}, backend, state, to.writer())

	backend.mu.Lock()
	msgs := append([]string(nil), backend.sentMessages...)
	backend.mu.Unlock()
	if len(msgs) != 1 || msgs[0] != "/role status" {
		t.Fatalf("sent messages = %v, want [/role status]", msgs)
	}
}

func TestHeadlessModelsCommandStatus(t *testing.T) {
	state := &headlessState{}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "models", Action: "status", RequestID: "models-status-1"}, backend, state, to.writer())

	env := findHeadlessEnvelopeValue(to.drain(), "models_response")
	if env == nil {
		t.Fatal("models_response missing")
	}
	payload := env.Payload.(map[string]any)
	if payload["ok"] != true {
		t.Fatalf("ok = %v, want true", payload["ok"])
	}
	if payload["status"] != "Model pool: thinking\n" {
		t.Fatalf("status = %q", payload["status"])
	}
	if payload["request_id"] != "models-status-1" {
		t.Fatalf("request_id = %v, want models-status-1", payload["request_id"])
	}
}

func TestHeadlessModelsCommandFailureEchoesRequestID(t *testing.T) {
	state := &headlessState{}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "models", Action: "set_current_model_pool", RequestID: "models-failed-1"}, backend, state, to.writer())

	env := findHeadlessEnvelopeValue(to.drain(), "models_response")
	if env == nil {
		t.Fatal("models_response missing")
	}
	payload := env.Payload.(map[string]any)
	if payload["ok"] != false || payload["request_id"] != "models-failed-1" {
		t.Fatalf("models_response payload = %v, want failed response correlated to models-failed-1", payload)
	}
}

func TestHeadlessModelsCommandSetCurrentModelPool(t *testing.T) {
	state := &headlessState{}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "models", Action: "set_current_model_pool", Pool: "fast", RequestID: "models-set-1"}, backend, state, to.writer())

	backend.mu.Lock()
	msgs := append([]string(nil), backend.sentMessages...)
	backend.mu.Unlock()
	want := []string{"set-current-model-pool:fast"}
	if len(msgs) != len(want) {
		t.Fatalf("sent messages = %v, want %v", msgs, want)
	}
	for i := range want {
		if msgs[i] != want[i] {
			t.Fatalf("sent messages = %v, want %v", msgs, want)
		}
	}

	responses := 0
	for _, env := range to.drain() {
		if env.Type == "models_response" {
			responses++
			payload := env.Payload.(map[string]any)
			if payload["ok"] != true {
				t.Fatalf("models_response ok = %v, want true", payload["ok"])
			}
			if payload["request_id"] != "models-set-1" {
				t.Fatalf("models_response request_id = %v, want models-set-1", payload["request_id"])
			}
		}
	}
	if responses != 1 {
		t.Fatalf("models_response count = %d, want 1", responses)
	}
}

func rolePayloadRoles(t *testing.T, env *headlessEnvelope) []map[string]any {
	t.Helper()
	rolesAny, ok := env.Payload.(map[string]any)["roles"]
	if !ok {
		t.Fatal("roles missing from role_response payload")
	}
	items, ok := rolesAny.([]any)
	if !ok {
		t.Fatalf("roles = %T, want []any", rolesAny)
	}
	roles := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("role item = %T, want map[string]any", it)
		}
		roles = append(roles, m)
	}
	return roles
}

func TestHeadlessRoleListCommand(t *testing.T) {
	state := &headlessState{}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "role", Action: "list"}, backend, state, to.writer())

	env := findHeadlessEnvelopeValue(to.drain(), "role_response")
	if env == nil {
		t.Fatal("role_response missing")
	}
	payload := env.Payload.(map[string]any)
	if payload["ok"] != true {
		t.Fatalf("ok = %v, want true", payload["ok"])
	}
	if payload["role"] != "builder" {
		t.Fatalf("role = %v, want builder", payload["role"])
	}
	roles := rolePayloadRoles(t, env)
	if len(roles) != 2 {
		t.Fatalf("roles count = %d, want 2", len(roles))
	}
	if roles[0]["name"] != "builder" || roles[0]["current"] != true {
		t.Fatalf("roles[0] = %v, want builder/current", roles[0])
	}
	if roles[1]["name"] != "planner" || roles[1]["current"] != false {
		t.Fatalf("roles[1] = %v, want planner/not current", roles[1])
	}
}

func TestHeadlessRoleListCommandPreservesBackendOrder(t *testing.T) {
	state := &headlessState{}
	to := newTestOut(t)
	backend := &mockBackend{availableRoles: []string{"zeta", "builder"}, currentRole: "zeta"}

	handleHeadlessCommand(headlessCommand{Type: "role", Action: "list"}, backend, state, to.writer())

	env := findHeadlessEnvelopeValue(to.drain(), "role_response")
	if env == nil {
		t.Fatal("role_response missing")
	}
	payload := env.Payload.(map[string]any)
	if payload["role"] != "zeta" {
		t.Fatalf("role = %v, want zeta", payload["role"])
	}
	roles := rolePayloadRoles(t, env)
	if len(roles) != 2 {
		t.Fatalf("roles count = %d, want 2", len(roles))
	}
	if roles[0]["name"] != "zeta" || roles[0]["current"] != true {
		t.Fatalf("roles[0] = %v, want zeta/current", roles[0])
	}
	if roles[1]["name"] != "builder" || roles[1]["current"] != false {
		t.Fatalf("roles[1] = %v, want builder/not current", roles[1])
	}
}

func TestHeadlessRoleSetSwitchesRoleAndUpdatesStatus(t *testing.T) {
	state := &headlessState{}
	backend := &mockBackend{}

	// role set switches the backend's active role.
	to := newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "role", Action: "set", Role: "planner"}, backend, state, to.writer())

	env := findHeadlessEnvelopeValue(to.drain(), "role_response")
	if env == nil {
		t.Fatal("role_response missing for role set")
	}
	payload := env.Payload.(map[string]any)
	if payload["ok"] != true {
		t.Fatalf("ok = %v, want true", payload["ok"])
	}
	if payload["role"] != "planner" {
		t.Fatalf("role = %v, want planner", payload["role"])
	}
	roles := rolePayloadRoles(t, env)
	if roles[0]["current"] != false || roles[1]["current"] != true {
		t.Fatalf("roles after set = %v, want planner marked current", roles)
	}
	backend.mu.Lock()
	calls := append([]string(nil), backend.switchRoleCalls...)
	backend.mu.Unlock()
	if len(calls) != 1 || calls[0] != "planner" {
		t.Fatalf("switchRoleCalls = %v, want [planner]", calls)
	}

	// A later list reports the new active role.
	to = newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "role", Action: "list"}, backend, state, to.writer())
	env = findHeadlessEnvelopeValue(to.drain(), "role_response")
	payload = env.Payload.(map[string]any)
	if payload["role"] != "planner" {
		t.Fatalf("list role after set = %v, want planner", payload["role"])
	}

	// status_response.current_role reflects the backend role before any
	// RoleChangedEvent has flowed through the event filter.
	to = newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	env = findHeadlessEnvelopeValue(to.drain(), "status_response")
	payload = env.Payload.(map[string]any)
	if payload["current_role"] != "planner" {
		t.Fatalf("current_role = %v, want planner", payload["current_role"])
	}
}

func TestHeadlessRoleSetRefreshesWarmRoleCache(t *testing.T) {
	backend := &mockBackend{availableRoles: []string{"builder", "planner"}, currentRole: "builder"}
	// Warm cache: state.role is already populated (by an earlier RoleChangedEvent
	// or a status backfill), so headlessCurrentRole would serve it as-is instead
	// of asking the backend again.
	state := &headlessState{role: "builder"}

	to := newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "role", Action: "set", Role: "planner"}, backend, state, to.writer())

	env := findHeadlessEnvelopeValue(to.drain(), "role_response")
	if env == nil {
		t.Fatal("role_response missing for role set")
	}
	payload := env.Payload.(map[string]any)
	if payload["ok"] != true || payload["role"] != "planner" {
		t.Fatalf("role_response payload = %v, want ok with role planner", payload)
	}

	// No RoleChangedEvent has been pumped through the event filter; the cache
	// write on the set path must make an immediate status report the new role
	// instead of the stale cached one.
	to = newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	env = findHeadlessEnvelopeValue(to.drain(), "status_response")
	if env == nil {
		t.Fatal("status_response missing")
	}
	statusPayload := env.Payload.(map[string]any)
	if statusPayload["current_role"] != "planner" {
		t.Fatalf("current_role = %v, want planner", statusPayload["current_role"])
	}
}

func TestHeadlessRoleSetFailureBranches(t *testing.T) {
	missingRoleErr := errors.New(`unknown role "ghost"`)
	notAvailableErr := errors.New(`role "worker" is not available`)
	tests := []struct {
		name        string
		cmd         headlessCommand
		backend     *mockBackend
		state       *headlessState
		wantMessage string
		wantCalls   []string
	}{
		{
			name:        "missing role",
			cmd:         headlessCommand{Type: "role", Action: "set"},
			backend:     &mockBackend{},
			state:       &headlessState{},
			wantMessage: "role set requires role",
		},
		{
			name:        "already active role",
			cmd:         headlessCommand{Type: "role", Action: "set", Role: "builder"},
			backend:     &mockBackend{},
			state:       &headlessState{},
			wantMessage: "already the active role: builder",
		},
		{
			name:        "pending handoff blocks set",
			cmd:         headlessCommand{Type: "role", Action: "set", Role: "planner"},
			backend:     &mockBackend{},
			state:       &headlessState{pendingHandoff: &headlessHandoffPayload{RequestID: "handoff-1"}},
			wantMessage: "resolve the pending handoff before switching role",
		},
		{
			name:        "unknown role passthrough",
			cmd:         headlessCommand{Type: "role", Action: "set", Role: "ghost"},
			backend:     &mockBackend{switchRoleErr: missingRoleErr},
			state:       &headlessState{},
			wantMessage: `unknown role "ghost"`,
			wantCalls:   []string{"ghost"},
		},
		{
			name:        "non-main role not available",
			cmd:         headlessCommand{Type: "role", Action: "set", Role: "worker"},
			backend:     &mockBackend{switchRoleErr: notAvailableErr},
			state:       &headlessState{},
			wantMessage: `role "worker" is not available`,
			wantCalls:   []string{"worker"},
		},
		{
			name:        "unsupported action",
			cmd:         headlessCommand{Type: "role", Action: "frobnicate"},
			backend:     &mockBackend{},
			state:       &headlessState{},
			wantMessage: "unsupported role action: frobnicate",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			to := newTestOut(t)
			handleHeadlessCommand(tc.cmd, tc.backend, tc.state, to.writer())
			env := findHeadlessEnvelopeValue(to.drain(), "role_response")
			if env == nil {
				t.Fatal("role_response missing")
			}
			payload := env.Payload.(map[string]any)
			if payload["ok"] != false {
				t.Fatalf("ok = %v, want false", payload["ok"])
			}
			if payload["message"] != tc.wantMessage {
				t.Fatalf("message = %v, want %q", payload["message"], tc.wantMessage)
			}
			tc.backend.mu.Lock()
			calls := append([]string(nil), tc.backend.switchRoleCalls...)
			tc.backend.mu.Unlock()
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("switchRoleCalls = %v, want %v", calls, tc.wantCalls)
			}
		})
	}
}

func TestHeadlessRoleChangeEventRequiresSubscription(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{"role_change": true}}
	// The event reflects a switch the backend has already committed.
	backend := &mockBackend{currentRole: "planner"}

	out := filterHeadlessEvent(agent.RoleChangedEvent{Role: "planner"}, state, backend)
	if len(out) != 1 {
		t.Fatalf("filtered events = %d, want 1", len(out))
	}
	if out[0].Type != "role_change" {
		t.Fatalf("event type = %q, want role_change", out[0].Type)
	}
	payload := out[0].Payload.(map[string]string)
	if payload["role"] != "planner" {
		t.Fatalf("role_change role = %v, want planner", payload["role"])
	}
	state.mu.Lock()
	role := state.role
	state.mu.Unlock()
	if role != "planner" {
		t.Fatalf("state.role = %q, want planner", role)
	}
}

func TestHeadlessRoleChangeEventFilteredWithoutSubscriptionButStateUpdated(t *testing.T) {
	// An explicit subscribe allowlist that omits role_change.
	state := &headlessState{subscriptions: map[string]bool{"activity": true}}
	// The event reflects a switch the backend has already committed.
	backend := &mockBackend{currentRole: "planner"}

	out := filterHeadlessEvent(agent.RoleChangedEvent{Role: "planner"}, state, backend)
	if len(out) != 0 {
		t.Fatalf("filtered events = %d, want 0 without subscription", len(out))
	}
	state.mu.Lock()
	role := state.role
	state.mu.Unlock()
	if role != "planner" {
		t.Fatalf("state.role = %q, want planner", role)
	}

	// A later status query answers current_role from the cached state.
	to := newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	env := findHeadlessEnvelopeValue(to.drain(), "status_response")
	payload := env.Payload.(map[string]any)
	if payload["current_role"] != "planner" {
		t.Fatalf("current_role = %v, want planner from cached state", payload["current_role"])
	}
}

// headlessSendOnlyBackend satisfies headlessBackend without any optional
// capability (models/role/handoff), for testing the capability error envelope.
type headlessSendOnlyBackend struct{}

func (headlessSendOnlyBackend) SendUserMessage(content string)                        {}
func (headlessSendOnlyBackend) CancelCurrentTurn() bool                               { return false }
func (headlessSendOnlyBackend) ResolveConfirm(string, string, string, string, string) {}
func (headlessSendOnlyBackend) ResolveQuestion([]string, string, string) (string, bool) {
	return "", true
}
func (headlessSendOnlyBackend) SupersedeQuestion(string) bool { return false }

func TestHeadlessRoleCommandRejectedByNonRoleBackend(t *testing.T) {
	state := &headlessState{}
	to := newTestOut(t)
	var backend headlessBackend = &headlessSendOnlyBackend{}

	handleHeadlessCommand(headlessCommand{Type: "role", Action: "list"}, backend, state, to.writer())

	env := findHeadlessEnvelopeValue(to.drain(), "error")
	if env == nil {
		t.Fatal("error envelope missing for non-role backend")
	}
	payload := env.Payload.(map[string]any)
	if payload["message"] != "role command is not supported by this backend" {
		t.Fatalf("message = %v, want role-not-supported", payload["message"])
	}
}

// A RoleChangedEvent that drains after a newer role set (the agent event
// stream and the role command run on different goroutines) must neither
// regress the warm cache nor emit a stale role_change envelope.
func TestHeadlessRoleChangeEventDoesNotRegressRoleCacheAfterRoleSet(t *testing.T) {
	backend := &mockBackend{availableRoles: []string{"builder", "planner", "executor"}, currentRole: "builder"}
	state := &headlessState{role: "builder", subscriptions: map[string]bool{"role_change": true}}

	// Two rapid sets (executor, then planner). Each commits synchronously on the
	// backend and refreshes the cache on the set path.
	to := newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "role", Action: "set", Role: "executor"}, backend, state, to.writer())
	if env := findHeadlessEnvelopeValue(to.drain(), "role_response"); env == nil {
		t.Fatal("role_response missing for set executor")
	}
	to = newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "role", Action: "set", Role: "planner"}, backend, state, to.writer())
	if env := findHeadlessEnvelopeValue(to.drain(), "role_response"); env == nil {
		t.Fatal("role_response missing for set planner")
	}

	// The trailing RoleChangedEvent of the first set (executor) now drains after
	// the cache already shows planner; it must not overwrite the newer role.
	if envs := filterHeadlessEvent(agent.RoleChangedEvent{Role: "executor"}, state, backend); len(envs) != 0 {
		t.Fatalf("stale role_change envelope emitted: %#v", envs)
	}
	state.mu.Lock()
	role := state.role
	state.mu.Unlock()
	if role != "planner" {
		t.Fatalf("state.role = %q, want planner (unchanged by stale event)", role)
	}

	// Status still reports the latest committed role.
	to = newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	env := findHeadlessEnvelopeValue(to.drain(), "status_response")
	payload := env.Payload.(map[string]any)
	if payload["current_role"] != "planner" {
		t.Fatalf("current_role = %v, want planner", payload["current_role"])
	}
}

// A successful role set announces the switch itself: the role_response is a
// command reply, so if the set only refreshed the cache the trailing
// RoleChangedEvent would dedupe against that cache and the only role_change for
// the switch would never be pushed. Subscribers must see exactly one
// role_change per successful switch, whether the event drains before or after
// the set response.
func TestHeadlessRoleSetAnnouncesRoleChangeOnceDespiteDelayedEvent(t *testing.T) {
	backend := &mockBackend{availableRoles: []string{"builder", "planner"}, currentRole: "builder"}
	state := &headlessState{role: "builder", subscriptions: map[string]bool{"role_change": true}}

	to := newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "role", Action: "set", Role: "planner"}, backend, state, to.writer())
	envs := to.drain()
	if findHeadlessEnvelopeValue(envs, "role_response") == nil {
		t.Fatal("role_response missing for role set")
	}
	announcements := 0
	for _, env := range envs {
		if env.Type == "role_change" {
			announcements++
		}
	}
	if announcements != 1 {
		t.Fatalf("role_change announcements at set = %d, want exactly 1", announcements)
	}

	// The RoleChangedEvent that trails the set is resolved against the backend's
	// committed role and dedupes against the announced role, never emitting a
	// duplicate envelope.
	if envs := filterHeadlessEvent(agent.RoleChangedEvent{Role: "planner"}, state, backend); len(envs) != 0 {
		t.Fatalf("trailing RoleChangedEvent duplicated the announcement: %#v", envs)
	}
	state.mu.Lock()
	role := state.role
	state.mu.Unlock()
	if role != "planner" {
		t.Fatalf("state.role = %q, want planner", role)
	}
}

// A warm state.role that was filled by a status backfill (which is not an
// announcement) must not swallow the RoleChangedEvent for the switch it
// describes: the event is compared against the last announced role, not the
// current cache.
func TestHeadlessRoleChangeEventNotSwallowedByStatusBackfilledCache(t *testing.T) {
	backend := &mockBackend{currentRole: "planner"}
	// state.role was warmed by a status query before any role_change was ever
	// announced.
	state := &headlessState{role: "planner", subscriptions: map[string]bool{"role_change": true}}

	envs := filterHeadlessEvent(agent.RoleChangedEvent{Role: "planner"}, state, backend)
	if len(envs) != 1 || envs[0].Type != "role_change" {
		t.Fatalf("envelopes = %#v, want exactly one role_change", envs)
	}
	if payload := envs[0].Payload.(map[string]string); payload["role"] != "planner" {
		t.Fatalf("role_change role = %v, want planner", payload["role"])
	}
	// A repeated delivery of the same event stays quiet.
	if envs := filterHeadlessEvent(agent.RoleChangedEvent{Role: "planner"}, state, backend); len(envs) != 0 {
		t.Fatalf("repeated RoleChangedEvent duplicated the announcement: %#v", envs)
	}
}
