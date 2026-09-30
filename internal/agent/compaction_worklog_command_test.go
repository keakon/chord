package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCheckpointWorklogRecognizesGitCommitInvocations(t *testing.T) {
	for _, tc := range []struct {
		command string
		commit  bool
	}{
		{`git -C repo commit -m sample`, true},
		{`git -C 'sample repo' commit -m sample`, true},
		{`git -c user.name=Sample commit -m sample`, true},
		{`git -c 'user.name=Sample User' commit -m sample`, true},
		{`git --git-dir repo/.git --work-tree repo commit -m sample`, true},
		{`git --git-dir=repo/.git --no-pager commit -m sample`, true},
		{`git -Crepo -cuser.name=Sample commit -m sample`, true},
		{`git status && git -C "sample repo" commit -m sample && false`, true},
		{`echo 'git commit -m sample'`, false},
		{`git -C repo status`, false},
		{`git -C "$repo" commit -m sample`, false},
		{`git --unknown option commit -m sample`, false},
		{`git -C`, false},
	} {
		t.Run(tc.command, func(t *testing.T) {
			head := []message.Message{
				{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call", Name: tools.NameShell, Args: checkpointWorklogShellArgs(t, tc.command)}}},
				{Role: message.RoleTool, ToolCallID: "call", ToolStatus: message.ToolStatusSuccess, Content: "[main 1234567] sample"},
			}
			worklog := buildCheckpointWorklog(head)
			if (len(worklog.commits) == 1) != tc.commit {
				t.Fatalf("commits = %#v, want commit=%v", worklog.commits, tc.commit)
			}
		})
	}
}
