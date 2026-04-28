package agent

import (
	"testing"

	"github.com/keakon/chord/internal/permission"
)

func TestResolveConfirmUnknownActionDenies(t *testing.T) {
	a := &MainAgent{
		confirmCh: make(map[string]chan ConfirmResponse),
	}

	ch := make(chan ConfirmResponse, 1)
	a.confirmCh["req-1"] = ch

	a.ResolveConfirm("bogus", `{"path":"x"}`, "", "", "req-1")

	select {
	case resp := <-ch:
		if resp.Approved {
			t.Fatal("expected unknown action to be denied")
		}
	default:
		t.Fatal("expected confirm response to be delivered")
	}
}

func TestResolveConfirmWithRuleIntentPassesIntent(t *testing.T) {
	a := &MainAgent{
		confirmCh: make(map[string]chan ConfirmResponse),
	}

	ch := make(chan ConfirmResponse, 1)
	a.confirmCh["req-1"] = ch
	intent := &ConfirmRuleIntent{
		Pattern: "git *",
		Scope:   int(permission.ScopeProject),
	}

	a.ResolveConfirmWithRuleIntent("allow", `{"command":"git status"}`, "", "", "req-1", intent)

	select {
	case resp := <-ch:
		if !resp.Approved {
			t.Fatal("expected response to be approved")
		}
		if resp.RuleIntent == nil {
			t.Fatal("expected rule intent to be propagated")
		}
		if resp.RuleIntent.Pattern != "git *" || resp.RuleIntent.Scope != int(permission.ScopeProject) {
			t.Fatalf("rule intent = %+v, want pattern=git *, scope=%d", resp.RuleIntent, int(permission.ScopeProject))
		}
	default:
		t.Fatal("expected confirm response to be delivered")
	}
}
