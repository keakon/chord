package tui

import "testing"

func TestTruncateMiddleDisplay(t *testing.T) {
	got := truncateMiddleDisplay("/home/user/projects/myapp", 16)
	if got != "/home/…/myapp" {
		t.Fatalf("truncateMiddleDisplay() = %q, want %q", got, "/home/…/myapp")
	}
}
