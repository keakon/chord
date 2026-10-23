package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

// An external backend owns its model, context, usage, timing, and skills
// outside Chord. Viewing its task must report those as unavailable instead of
// falling back to the main role's numbers, whether the task is parked or has
// settled without ever parking.
func TestExternalTaskFocusHidesChordOwnedAgentData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parked bool
	}{
		{name: "parked", parked: true},
		{name: "settled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newTestMainAgent(t, t.TempDir())
			a.modelPoolPolicy = NewRuntimeModelPoolPolicy()
			a.SetAgentConfigs(map[string]*config.AgentConfig{
				"builder":       {Name: "builder", Mode: config.AgentModeMain, Models: map[string][]string{"main": {"test/main"}}},
				"claude-worker": {Name: "claude-worker", Mode: config.AgentModeSubAgent},
			})
			a.activeConfig = a.agentConfigs["builder"]
			a.ruleset = permission.Ruleset{{Permission: "skill", Pattern: "*", Action: permission.ActionAllow}}
			a.SetSkills([]*skill.Meta{{Name: "go-expert", Description: "Go language development expert"}})
			a.MarkSkillInvoked(&skill.Meta{Name: "go-expert", Description: "Go language development expert"})
			a.usageTracker.RecordForAgent(identity.MainAgentID, "test/main", nil, message.TokenUsage{InputTokens: 100, OutputTokens: 10})
			a.walltime.recordTarget(a.walltime.captureAt(identity.MainAgentID, "builder", 1), analytics.WalltimePurposeModel, time.Second)
			a.ctxMgr.Append(message.Message{Role: "user", Content: "main-msg"})

			// The main role has data in every facade, so a zero reading below
			// proves the external guard rather than an empty fixture.
			if got := a.GetTokenUsage(); got.InputTokens != 100 || got.OutputTokens != 10 {
				t.Fatalf("main token usage = %+v, want 100/10", got)
			}
			if got := a.GetSidebarWalltimeStats(); got.Model != time.Second {
				t.Fatalf("main walltime = %+v, want 1s model time", got)
			}
			if got := a.GetContextMessageCount(); got != 1 {
				t.Fatalf("main context messages = %d, want 1", got)
			}
			if got := a.GetContextBytes(); got <= 0 {
				t.Fatalf("main context bytes = %d, want > 0", got)
			}
			if got := a.CurrentPoolName(); got != "main" {
				t.Fatalf("main pool = %q, want main", got)
			}
			if got := a.FocusedSkills(); len(got) != 1 {
				t.Fatalf("main focused skills = %d, want 1", len(got))
			}
			if got := a.InvokedSkills(); len(got) != 1 {
				t.Fatalf("main invoked skills = %d, want 1", len(got))
			}

			const taskID = "external-facade-task"
			const instanceID = "external-facade-worker-1"
			a.setTaskRecords(map[string]*DurableTaskRecord{
				taskID: {
					TaskID:            taskID,
					AgentDefName:      "claude-worker",
					LatestInstanceID:  instanceID,
					InstanceHistory:   []string{instanceID},
					State:             string(SubAgentStateCompleted),
					ResumePolicy:      taskResumePolicyNotify,
					RuntimeParked:     tc.parked,
					ExternalSession:   &ExternalTaskSession{Backend: config.BackendKindClaudeCode, SessionID: "external-session"},
					InvokedSkillNames: []string{"go-expert"},
					UpdatedAt:         time.Now(),
				},
			})
			a.SwitchFocus(instanceID)

			if got := externalBackendForTarget(a.focusedAgentSnapshot()); got != config.BackendKindClaudeCode {
				t.Fatalf("focused external backend = %q, want %q", got, config.BackendKindClaudeCode)
			}

			modelState := a.FocusedModelState()
			if modelState.Backend != config.BackendKindClaudeCode {
				t.Fatalf("focused model state backend = %q, want %q", modelState.Backend, config.BackendKindClaudeCode)
			}
			if modelState.SelectedRef != "" || modelState.RunningRef != "" || modelState.DisplayRef != "" {
				t.Fatalf("focused model refs = %#v, want none for an external backend", modelState)
			}
			if modelState.PoolName != "" || modelState.PoolNames != nil {
				t.Fatalf("focused pools = %q/%v, want none for an external backend", modelState.PoolName, modelState.PoolNames)
			}
			if modelState.KeysConfirmed != 0 || modelState.KeysTotal != 0 || modelState.RateLimit != nil {
				t.Fatalf("focused key/rate state = %#v, want no provider data for an external backend", modelState)
			}
			if modelState.ServiceTier != "" || modelState.EffectiveTier != "" {
				t.Fatalf("focused tiers = %q/%q, want none for an external backend", modelState.ServiceTier, modelState.EffectiveTier)
			}

			if got := a.GetSidebarUsageStats(); got.InputTokens != 0 || got.OutputTokens != 0 || got.LLMCalls != 0 {
				t.Fatalf("external usage = %#v, want zeros", got)
			}
			if got := a.GetTokenUsage(); got != (message.TokenUsage{}) {
				t.Fatalf("external token usage = %#v, want zeros", got)
			}
			if got := a.GetSidebarWalltimeStats(); !got.IsZero() {
				t.Fatalf("external walltime = %#v, want zeros", got)
			}
			if current, limit := a.GetContextStats(); current != 0 || limit != 0 {
				t.Fatalf("external context stats = (%d, %d), want zeros", current, limit)
			}
			if got := a.GetContextUsageState(); got != ctxmgr.ContextUsageUnknown {
				t.Fatalf("external context usage state = %q, want unknown", got)
			}
			if got := a.GetContextMessageCount(); got != 0 {
				t.Fatalf("external context messages = %d, want 0", got)
			}
			if got := a.GetContextBytes(); got != 0 {
				t.Fatalf("external context bytes = %d, want 0", got)
			}
			if got := a.NextRequestModelRef(); got != "" {
				t.Fatalf("external next request model = %q, want empty", got)
			}
			if got := a.CurrentPoolName(); got != "" {
				t.Fatalf("external current pool = %q, want empty", got)
			}
			if got := a.PoolNames(); got != nil {
				t.Fatalf("external pool names = %v, want none", got)
			}
			if confirmed, total := a.KeyStats(); confirmed != 0 || total != 0 {
				t.Fatalf("external key stats = %d/%d, want 0/0", confirmed, total)
			}
			if got := a.FocusedSkills(); got != nil {
				t.Fatalf("external focused skills = %#v, want none", got)
			}
			if got := a.FocusedSkillInvocationStates(); got != nil {
				t.Fatalf("external skill states = %#v, want none", got)
			}
			if got := a.InvokedSkills(); got != nil {
				t.Fatalf("external invoked skills = %#v, want none", got)
			}

			err := a.SetCurrentModelPool("fast")
			if err == nil || !strings.Contains(err.Error(), "external agents") {
				t.Fatalf("external SetCurrentModelPool error = %v, want the external refusal", err)
			}
			for {
				select {
				case <-a.Events():
					continue
				default:
				}
				break
			}
			a.handleModelsSetCurrentView("fast")
			select {
			case evt := <-a.Events():
				if _, ok := evt.(ErrorEvent); !ok {
					t.Fatalf("external /models event = %T, want ErrorEvent", evt)
				}
			case <-time.After(time.Second):
				t.Fatal("external /models emitted no error event")
			}
			if got := a.modelPoolPolicy.CurrentModelPool(); got != "" {
				t.Fatalf("main current pool = %q after external refusals, want unchanged", got)
			}
			if got, ok := a.AgentOverridePoolName("claude-worker"); ok {
				t.Fatalf("external role pool override = %q after refusals, want none", got)
			}
		})
	}
}

// A live external runtime is identified by the worker itself, before its task
// record exists; both paths must name the backend so facades never treat the
// external process as a Chord-native agent.
func TestExternalBackendForTargetNamesLiveWorker(t *testing.T) {
	live := focusedAgentSnapshot{external: &externalWorker{backend: config.BackendConfig{Kind: config.BackendKindClaudeCode}}}
	if got := externalBackendForTarget(live); got != config.BackendKindClaudeCode {
		t.Fatalf("live external backend = %q, want %q", got, config.BackendKindClaudeCode)
	}
	unset := focusedAgentSnapshot{external: &externalWorker{}}
	if got := externalBackendForTarget(unset); got != config.BackendKindClaudeCode {
		t.Fatalf("live external backend without kind = %q, want the default %q", got, config.BackendKindClaudeCode)
	}
	if got := externalBackendForTarget(focusedAgentSnapshot{}); got != "" {
		t.Fatalf("main target backend = %q, want empty", got)
	}
}
