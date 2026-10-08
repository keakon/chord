package tui

import (
	"testing"

	tea "github.com/keakon/bubbletea/v2"
)

func newMouseConfirmModel(toolName, argsJSON string) *Model {
	m := NewModelWithSize(nil, 100, 30)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: toolName, ArgsJSON: argsJSON}
	m.layout = m.generateLayout(m.width, m.height)
	return &m
}

func confirmHitboxForTest(t *testing.T, m *Model, action confirmDialogAction) confirmOptionHitbox {
	t.Helper()
	for _, hitbox := range m.confirmOptionHitboxes() {
		if hitbox.action == action {
			return hitbox
		}
	}
	t.Fatalf("no hitbox for confirmation action %d", action)
	return confirmOptionHitbox{}
}

func clickConfirmActionForTest(t *testing.T, m *Model, action confirmDialogAction) tea.Cmd {
	t.Helper()
	hitbox := confirmHitboxForTest(t, m, action)
	updated, cmd := m.Update(tea.MouseClickMsg{
		X:      (hitbox.minX + hitbox.maxX) / 2,
		Y:      hitbox.minY,
		Button: tea.MouseLeft,
	})
	if updated != m {
		t.Fatalf("mouse update returned %T, want the same model", updated)
	}
	return cmd
}

func TestConfirmMouseClickSharesAllowDecisionWithKeyboard(t *testing.T) {
	mouseModel := newMouseConfirmModel("shell", `{"command":"git status"}`)
	clickConfirmActionForTest(t, mouseModel, confirmDialogAllow)

	var mouseResult ConfirmResult
	select {
	case mouseResult = <-mouseModel.confirmResultCh:
	default:
		t.Fatal("mouse Allow click did not resolve confirmation")
	}
	if mouseResult.Action != ConfirmAllow {
		t.Fatalf("mouse action = %v, want ConfirmAllow", mouseResult.Action)
	}

	keyModel := newMouseConfirmModel("shell", `{"command":"git status"}`)
	keyModel.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "a", Code: 'a'}))
	var keyResult ConfirmResult
	select {
	case keyResult = <-keyModel.confirmResultCh:
	default:
		t.Fatal("keyboard Allow shortcut did not resolve confirmation")
	}
	if keyResult != mouseResult {
		t.Fatalf("keyboard result = %#v, mouse result = %#v", keyResult, mouseResult)
	}
}

func TestConfirmMouseClickActions(t *testing.T) {
	tests := []struct {
		name   string
		action confirmDialogAction
		check  func(*testing.T, *Model)
	}{
		{
			name:   "deny",
			action: confirmDialogDeny,
			check: func(t *testing.T, m *Model) {
				t.Helper()
				select {
				case result := <-m.confirmResultCh:
					if result.Action != ConfirmDeny {
						t.Fatalf("action = %v, want ConfirmDeny", result.Action)
					}
				default:
					t.Fatal("mouse Deny click did not resolve confirmation")
				}
			},
		},
		{
			name:   "deny with reason",
			action: confirmDialogDenyReason,
			check: func(t *testing.T, m *Model) {
				t.Helper()
				if !m.confirm.denyingWithReason {
					t.Fatal("mouse Deny+Reason click did not enter reason mode")
				}
			},
		},
		{
			name:   "edit arguments",
			action: confirmDialogEdit,
			check: func(t *testing.T, m *Model) {
				t.Helper()
				if !m.confirm.editing {
					t.Fatal("mouse Edit args click did not enter edit mode")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := newMouseConfirmModel("shell", `{"command":"git status"}`)
			clickConfirmActionForTest(t, m, test.action)
			test.check(t, m)
		})
	}
}

func TestConfirmMouseClickIgnoresDialogBody(t *testing.T) {
	m := newMouseConfirmModel("shell", `{"command":"git status"}`)
	dialog := m.renderConfirmDialog()
	dialogRect := m.overlayRect(dialog)
	x := dialogRect.Min.X + DirectoryBorderStyle.GetBorderLeftSize()
	y := dialogRect.Min.Y + DirectoryBorderStyle.GetBorderTopSize()

	if _, ok := m.confirmOptionAt(x, y); ok {
		t.Fatal("dialog border/body point should not hit an action")
	}
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.confirm.request == nil {
		t.Fatal("clicking dialog body should not resolve confirmation")
	}
}

func TestConfirmMouseClickHonorsDoneRestrictions(t *testing.T) {
	m := newMouseConfirmModel("done", `{}`)
	m.confirm.request.ForceDenyReason = true

	for _, hitbox := range m.confirmOptionHitboxes() {
		if hitbox.action == confirmDialogAllow || hitbox.action == confirmDialogDeny {
			t.Fatalf("force-deny Done dialog exposed forbidden action %d", hitbox.action)
		}
	}

	clickConfirmActionForTest(t, m, confirmDialogDenyReason)
	if !m.confirm.denyingWithReason {
		t.Fatal("force-deny Done click should enter required reason mode")
	}
}
