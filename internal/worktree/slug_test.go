package worktree

import (
	"strings"
	"testing"
	"time"
)

func TestValidateSlug_Valid(t *testing.T) {
	cases := []string{
		"feat-auth",
		"task-1.2",
		"abc_def",
		"a",
		"A1",
		"task-20260507-091245",
		strings.Repeat("a", MaxSlugLen),
	}
	for _, s := range cases {
		if err := ValidateSlug(s); err != nil {
			t.Errorf("ValidateSlug(%q) returned %v, want nil", s, err)
		}
	}
}

func TestValidateSlug_Invalid(t *testing.T) {
	cases := []struct {
		in       string
		contains string
	}{
		{"", "empty"},
		{strings.Repeat("a", MaxSlugLen+1), "longer than"},
		{".", "forbidden"},
		{"..", "forbidden"},
		{"a..b", "forbidden"},
		{".hidden", "must not start with"},
		{"-leading", "must not start with"},
		{"foo/bar", "forbidden character"},
		{"foo bar", "forbidden character"},
		{"foo:bar", "forbidden character"},
		{"foo+bar", "forbidden character"},
		{"中文", "forbidden character"},
	}
	for _, tc := range cases {
		err := ValidateSlug(tc.in)
		if err == nil {
			t.Errorf("ValidateSlug(%q) returned nil, want error", tc.in)
			continue
		}
		if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("ValidateSlug(%q) error = %q, want substring %q", tc.in, err.Error(), tc.contains)
		}
	}
}

func TestGenerateAutoSlug(t *testing.T) {
	tm := time.Date(2026, time.May, 7, 9, 12, 45, 0, time.UTC)
	got := GenerateAutoSlug(tm)
	want := "task-20260507-091245"
	if got != want {
		t.Errorf("GenerateAutoSlug(%v) = %q, want %q", tm, got, want)
	}
	if err := ValidateSlug(got); err != nil {
		t.Errorf("auto-generated slug failed validation: %v", err)
	}
}

func TestGenerateAutoSlug_UTC(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	tm := time.Date(2026, time.May, 7, 9, 12, 45, 0, loc)
	got := GenerateAutoSlug(tm)
	// 09:12:45 -07:00 -> 16:12:45 UTC
	want := "task-20260507-161245"
	if got != want {
		t.Errorf("GenerateAutoSlug(local) = %q, want %q (always UTC)", got, want)
	}
}
