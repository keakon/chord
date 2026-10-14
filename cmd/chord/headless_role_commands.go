package main

import (
	"strings"
	"time"
)

// headlessRoleBackend is the capability required by the role list/set commands.
// MainAgent satisfies it.
type headlessRoleBackend interface {
	AvailableRoles() []string
	CurrentRole() string
	SwitchRole(role string) error
}

type headlessRoleItem struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

// headlessRoleItems builds the ordered role list for a role_response, marking
// the backend's current role.
func headlessRoleItems(backend headlessRoleBackend) []headlessRoleItem {
	current := backend.CurrentRole()
	roles := backend.AvailableRoles()
	items := make([]headlessRoleItem, 0, len(roles))
	for _, name := range roles {
		items = append(items, headlessRoleItem{Name: name, Current: name == current})
	}
	return items
}

func emitHeadlessRoleResponse(out *stdoutWriter, ok bool, message string, role string, roles []headlessRoleItem) {
	payload := map[string]any{"ok": ok}
	if message != "" {
		payload["message"] = message
	}
	if role != "" {
		payload["role"] = role
	}
	if roles != nil {
		payload["roles"] = roles
	}
	out.emit(headlessEnvelope{Type: "role_response", Payload: payload})
}

// handleHeadlessRoleCommand serves the role list/set actions. A set mirrors the
// TUI Alt+R no-op protection (refusing the current role) and adds the
// headless-specific pending-handoff guard; target existence/availability is
// validated by the backend's SwitchRole so the "unknown role" and "not
// available (SubAgent-only)" answers live next to switchRole instead of being
// duplicated here.
func handleHeadlessRoleCommand(cmd headlessCommand, backend headlessRoleBackend, state *headlessState, out *stdoutWriter) {
	switch strings.TrimSpace(cmd.Action) {
	case "list":
		emitHeadlessRoleResponse(out, true, "", backend.CurrentRole(), headlessRoleItems(backend))
	case "set":
		role := strings.TrimSpace(cmd.Role)
		if role == "" {
			emitHeadlessRoleResponse(out, false, "role set requires role", "", nil)
			return
		}
		if role == backend.CurrentRole() {
			emitHeadlessRoleResponse(out, false, "already the active role: "+role, "", nil)
			return
		}
		state.mu.Lock()
		pendingHandoff := state.pendingHandoff
		state.mu.Unlock()
		if pendingHandoff != nil {
			emitHeadlessRoleResponse(out, false, "resolve the pending handoff before switching role", "", nil)
			return
		}
		// role set commits synchronously and takes effect immediately, even
		// while a turn is in flight — unlike a send message, which a busy agent
		// only processes at the next request boundary.
		if err := backend.SwitchRole(role); err != nil {
			emitHeadlessRoleResponse(out, false, err.Error(), "", nil)
			return
		}
		// Keep the state cache in sync with the backend immediately: the
		// RoleChangedEvent travels through the agent event loop and can trail
		// this response, which would leave status queries reading the previous
		// role from the warm cache. The committed switch is also announced here
		// (the response is a command reply, so the trailing event would
		// otherwise dedupe against the cache and the only role_change for the
		// switch would never be pushed); the marker update makes the trailing
		// event a no-op.
		emitHeadlessRoleResponse(out, true, "", role, headlessRoleItems(backend))
		// The cache write, seq allocation, and the role_change enqueue form one
		// ordered section: the announcement dedupe marker advances at stamp
		// time, so a push that reaches the wire out of seq order would be
		// dropped by version-comparing clients and never re-announced.
		out.ordered(func() {
			state.mu.Lock()
			if state.mcpRole != "" && state.mcpRole != role {
				clearHeadlessMCPStateLocked(state)
			}
			state.role = role
			announce := state.lastBroadcastRole != role
			if announce {
				state.lastBroadcastRole = role
			}
			state.updatedAt = time.Now()
			var change *headlessEnvelope
			if announce {
				if state.isSubscribed("role_change") {
					change = &headlessEnvelope{Type: "role_change", Payload: map[string]string{
						"role": role,
					}}
					state.stampHeadlessSeq(true, change)
				} else {
					// The role cache moved even though no envelope is emitted:
					// bump so later status_response snapshots stay ordered.
					state.stampHeadlessSeq(true)
				}
			}
			var mcpChange *headlessEnvelope
			if announce && state.mcpRole == role && state.isSubscribed(headlessMCPStatusChanged) {
				mcpChange = &headlessEnvelope{Type: headlessMCPStatusChanged, Payload: state.mcp.Clone()}
				state.stampHeadlessSeq(true, mcpChange)
			}
			state.mu.Unlock()
			if change != nil {
				out.emit(change)
			}
			if mcpChange != nil {
				out.emit(mcpChange)
			}
		})
	default:
		emitHeadlessRoleResponse(out, false, "unsupported role action: "+cmd.Action, "", nil)
	}
}

// headlessCurrentRole returns the state-cached active role, lazily filling it
// from the backend on first query. Startup chooses the role during session
// restore, which headlessState cannot know in advance, so the cache starts
// empty and converges on the first RoleChangedEvent or status query.
//
// The backfill deliberately does not bump seq: it only fills an empty cache,
// so no snapshot can have been copied "before" it (any snapshot either
// backfilled first or already saw the cached role), and the filled value is
// current at read time. Status re-reads state.role under the same lock as
// stampHeadlessSeq, so the returned value is not seq-consistent on its own —
// a RoleChangedEvent can bump seq after this returns.
func headlessCurrentRole(backend headlessBackend, state *headlessState) string {
	state.mu.Lock()
	role := state.role
	state.mu.Unlock()
	if role != "" {
		return role
	}
	roleBackend, ok := backend.(headlessRoleBackend)
	if !ok {
		return ""
	}
	role = roleBackend.CurrentRole()
	state.mu.Lock()
	if state.role == "" {
		state.role = role
	} else {
		role = state.role
	}
	state.mu.Unlock()
	return role
}
