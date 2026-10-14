package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestPresentPermissionAggregatesEveryTarget(t *testing.T) {
	// Present resolves symlinks, so use the same canonical root for path rules.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"allow.md", "deny.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("text"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := json.RawMessage(`{"files":[{"path":"allow.md"},{"path":"deny.md"}]}`)
	rules := permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}, {Permission: tools.NamePresent, Pattern: "deny.md", Action: permission.ActionDeny}}
	result := evaluateToolPermissionInDir(rules, tools.NamePresent, args, permission.PathScope{Cwd: root})
	if result.Action != permission.ActionDeny {
		t.Fatalf("partial denial ignored: %#v", result)
	}
	rules[1].Action = permission.ActionAsk
	result = evaluateToolPermissionInDir(rules, tools.NamePresent, args, permission.PathScope{Cwd: root})
	if result.Action != permission.ActionAsk || len(result.NeedsApprovalPaths) != 1 {
		t.Fatalf("ask was not aggregated: %#v", result)
	}
	rules = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}, {Permission: tools.NameRead, Pattern: "*", Action: permission.ActionAllow}}
	result = evaluateToolPermissionInDir(rules, tools.NamePresent, args, permission.PathScope{Cwd: root})
	if result.Action != permission.ActionDeny {
		t.Fatal("read permission authorized present")
	}
}
