package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

func TestHostedProbeFailureDoesNotExtendCapabilityCooldown(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"rate", &llm.APIError{StatusCode: 429}},
		{"transport", io.ErrUnexpectedEOF},
		{"cancelled", context.Canceled},
		{"missing execution", &llm.HostedCallNotObservedError{Tool: tools.NameWebSearch}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a := newTestMainAgent(t, t.TempDir())
				b := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
				caller, err := b.resolveCaller("", nil)
				if err != nil {
					t.Fatal(err)
				}
				key := hostedHealthKey{tool: tools.NameWebSearch}
				generation, err := b.acquireHostedProbe(key)
				if err != nil {
					t.Fatal(err)
				}
				for range hostedNoCallThreshold {
					b.finishHostedProbe(caller, key, generation, &llm.HostedCallNotObservedError{Tool: tools.NameWebSearch})
				}
				_, err = b.acquireHostedProbe(key)
				cooling, ok := errors.AsType[*llm.HostedTargetCoolingError](err)
				if !ok || cooling.ProbeInFlight || !cooling.Until.After(time.Now()) {
					t.Fatalf("cooldown error=%v", err)
				}
				time.Sleep(hostedNoCallCooldown)
				generation, err = b.acquireHostedProbe(key)
				if err != nil {
					t.Fatal(err)
				}
				_, err = b.acquireHostedProbe(key)
				probing, ok := errors.AsType[*llm.HostedTargetCoolingError](err)
				if !ok || !probing.ProbeInFlight || !strings.Contains(err.Error(), "no request was sent") {
					t.Fatalf("probe error=%v", err)
				}
				b.finishHostedProbe(caller, key, generation, tc.err)
				_, err = b.acquireHostedProbe(key)
				wantCooldown := llm.ClassifyHostedFailure(tc.err) == llm.HostedFailureNotObserved
				if (err != nil) != wantCooldown {
					t.Fatalf("acquire after probe: err=%v want cooldown=%t", err, wantCooldown)
				}
			})
		})
	}
}

func TestHostedNamedPoolHealthSurvivesSessionReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := newTestMainAgent(t, t.TempDir())
		b := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
		a.hostedBackend = b
		caller, err := b.resolveCaller("", nil)
		if err != nil {
			t.Fatal(err)
		}
		poolKey := hostedHealthKey{source: hostedRouteSource{kind: hostedRouteSourcePool, id: "tools"}, tool: tools.NameWebSearch}
		callerKey := hostedHealthKey{source: caller.source, tool: tools.NameWebSearch}
		generation, err := b.acquireHostedProbe(poolKey)
		if err != nil {
			t.Fatal(err)
		}
		failure := &llm.HostedCallNotObservedError{Tool: tools.NameWebSearch}
		for range hostedNoCallThreshold {
			b.finishHostedProbe(caller, poolKey, generation, failure)
		}
		if _, err := b.acquireHostedProbe(callerKey); err != nil {
			t.Fatal(err)
		}
		a.resetHostedCallers()
		if _, exists := b.health[callerKey]; exists {
			t.Fatal("session retained caller health")
		}
		if _, err := b.acquireHostedProbe(poolKey); err == nil {
			t.Fatal("session reset bypassed shared pool cooldown")
		}
		time.Sleep(hostedNoCallCooldown)
		generation, err = b.acquireHostedProbe(poolKey)
		if err != nil {
			t.Fatal(err)
		}
		a.resetHostedCallers()
		if _, err := b.acquireHostedProbe(poolKey); err == nil {
			t.Fatal("session reset admitted a second shared probe")
		}
		b.finishHostedProbe(caller, poolKey, generation, context.Canceled)
		next, err := b.acquireHostedProbe(poolKey)
		if err != nil {
			t.Fatal("retired caller did not release its shared probe")
		}
		b.finishHostedProbe(caller, poolKey, generation, nil)
		if _, err := b.acquireHostedProbe(poolKey); err == nil {
			t.Fatal("stale completion released a successor probe")
		}
		b.finishHostedProbe(caller, poolKey, next, context.Canceled)
	})
}
