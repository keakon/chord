package permission

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/toolname"
)

func TestRulesetMayWiden(t *testing.T) {
	parse := func(text string) Ruleset {
		t.Helper()
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(text), &node); err != nil {
			t.Fatal(err)
		}
		return ParsePermission(node.Content[0])
	}
	for _, tc := range []struct {
		name    string
		base    string
		project string
		want    bool
	}{
		{"identical", `"*": allow
shell: deny`, `"*": allow
shell: deny`, false},
		{"remove restriction", `"*": allow
delete: ask`, `"*": allow`, true},
		{"reorder overlapping rules", `"*": allow
shell: deny`, `shell: deny
"*": allow`, true},
		{"deny to ask", `delete: deny`, `delete: ask`, true},
		{"ask to allow", `read: ask`, `read: allow`, true},
		{"allow to deny", `shell: allow`, `shell: deny`, false},
		{"allow to ask", `read: allow`, `read: ask`, false},
		{"narrow shell allow to ask", `shell:
  "*": deny
  "git *": allow`, `shell:
  "*": deny
  "git *": ask`, true},
		{"tool glob includes shell", `"*":
  "*": deny
  "git *": allow`, `"*":
  "*": deny
  "git *": ask`, true},
		{"path glob broadened", `write:
  "docs/*.md": allow`, `write:
  "*": allow`, true},
		{"tool glob broadened", `read: allow`, `"*": allow`, true},
		{"edit counterpart broadened", `edit: deny
apply_patch: deny`, `edit: allow
apply_patch: deny`, true},
		{"same selector denies counterpart", `edit: allow
apply_patch: allow`, `edit: deny
apply_patch: allow`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, project := parse(tc.base), parse(tc.project)
			if got := project.MayWiden(base); got != tc.want {
				t.Fatalf("MayWiden=%v, want %v", got, tc.want)
			}
			if !tc.want {
				for _, prefix := range []Ruleset{
					nil, {{Permission: "*", Pattern: "*", Action: ActionAllow}},
					{{Permission: "*", Pattern: "*", Action: ActionDeny}},
				} {
					before, after := Merge(prefix, base), Merge(prefix, project)
					for _, tool := range []string{toolname.Read, toolname.Shell, toolname.Write, toolname.Edit, toolname.ApplyPatch} {
						for _, arg := range []string{"", "*", "file.md", "docs/file.md", "git status", "git status; echo ok"} {
							oldAction, newAction := before.Evaluate(tool, arg), after.Evaluate(tool, arg)
							if StricterAction(oldAction, newAction) != newAction {
								t.Fatalf("%s %q widened %s -> %s", tool, arg, oldAction, newAction)
							}
						}
					}
				}
			}
		})
	}
}
