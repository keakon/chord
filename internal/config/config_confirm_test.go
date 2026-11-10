package config

import (
	"strings"
	"testing"
)

func TestConfirmAskPolicyLoading(t *testing.T) {
	for _, tt := range []struct {
		name, yaml, want string
		issue            bool
	}{
		{"default", "{}\n", ConfirmAskPolicyWait, false},
		{"empty", "confirm_ask_policy: ''\n", ConfirmAskPolicyWait, false},
		{"wait", "confirm_ask_policy: wait\n", ConfirmAskPolicyWait, false},
		{"deny", "confirm_ask_policy: deny\n", ConfirmAskPolicyDeny, false},
		{"invalid", "confirm_ask_policy: other\n", ConfirmAskPolicyWait, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", tt.yaml)
			rc, err := LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatal(err)
			}
			if got := rc.Config.EffectiveConfirmAskPolicy(); got != tt.want {
				t.Fatalf("policy = %q, want %q", got, tt.want)
			}
			issues, err := resolvedFileIssues(path, "")
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(strings.Join(issues, "\n"), "confirm_ask_policy"); got != tt.issue {
				t.Fatalf("issues = %v, want policy issue %v", issues, tt.issue)
			}
		})
	}
}

func TestConfirmAskPolicyProjectOverrides(t *testing.T) {
	for _, tt := range []struct {
		name, global, project, want string
		diagnostic                  bool
	}{
		{"inherit", ConfirmAskPolicyDeny, "{}\n", ConfirmAskPolicyDeny, false},
		{"deny", ConfirmAskPolicyWait, "confirm_ask_policy: deny\n", ConfirmAskPolicyDeny, false},
		{"wait", ConfirmAskPolicyDeny, "confirm_ask_policy: wait\n", ConfirmAskPolicyWait, false},
		{"empty", ConfirmAskPolicyDeny, "confirm_ask_policy: ''\n", ConfirmAskPolicyWait, false},
		{"invalid", ConfirmAskPolicyDeny, "confirm_ask_policy: other\n", ConfirmAskPolicyDeny, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			global := writeIssueTestConfig(t, dir, "global.yaml", "confirm_ask_policy: "+tt.global+"\n")
			project := writeIssueTestConfig(t, dir, "project.yaml", tt.project)
			rc, err := LoadResolvedConfig(global, project)
			if err != nil {
				t.Fatal(err)
			}
			if got := rc.Config.EffectiveConfirmAskPolicy(); got != tt.want {
				t.Fatalf("policy = %q, want %q", got, tt.want)
			}
			if tt.diagnostic {
				found := false
				for _, d := range rc.Diagnostics {
					if d.Path == "confirm_ask_policy" {
						found = true
					}
				}
				if !found || rc.Project.ConfirmAskPolicy != nil {
					t.Fatalf("invalid override retained: project=%+v diagnostics=%+v", rc.Project.ConfirmAskPolicy, rc.Diagnostics)
				}
			}
		})
	}
}
