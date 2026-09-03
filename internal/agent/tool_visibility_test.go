package agent

import (
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestMainToolVisibleMirrorsLiveSurface(t *testing.T) {
	// The exported visibility gate exists so descriptions and prompt blocks
	// can reference a sibling tool without pushing one the model cannot
	// call. It must mirror the live surface exactly: registered and not
	// denied → visible; missing, denied, or a nil agent → not visible.
	a := &MainAgent{}
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewTodoWriteTool(nil))

	if !a.MainToolVisible(tools.NameTodoWrite) {
		t.Fatal("registered, non-denied todo_write must be visible")
	}
	if !a.MainToolVisible(" " + tools.NameTodoWrite + " ") {
		t.Fatal("lookup must trim the queried name")
	}
	if a.MainToolVisible(tools.NameCompactContext) {
		t.Fatal("an unregistered tool must not be visible")
	}
	if (&MainAgent{}).MainToolVisible(tools.NameTodoWrite) {
		t.Fatal("a nil tool registry must yield no visible tools")
	}
}

func TestMainToolVisibleFalseWhenToolDenied(t *testing.T) {
	a := &MainAgent{}
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.ruleset = permission.Ruleset{{Permission: tools.NameTodoWrite, Pattern: "*", Action: permission.ActionDeny}}

	if a.MainToolVisible(tools.NameTodoWrite) {
		t.Fatal("a permission-deny ruleset must hide todo_write from the visible surface")
	}
}
