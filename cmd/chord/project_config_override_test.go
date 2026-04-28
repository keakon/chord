package main

import (
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestApplyProjectConfigOverrides_PreferSleepAndDesktopNotification(t *testing.T) {
	ac := newTestAppContext(t)
	globalPreventSleep := false
	globalDesktopNotification := false
	ac.Cfg = &config.Config{
		DesktopNotification: &globalDesktopNotification,
		PreventSleep:        &globalPreventSleep,
	}

	projectPreventSleep := true
	projectDesktopNotification := true
	pc := &config.Config{
		DesktopNotification: &projectDesktopNotification,
		PreventSleep:        &projectPreventSleep,
	}

	applyProjectConfigOverrides(ac, pc)

	if ac.ProjectCfg != pc {
		t.Fatal("expected project config to be stored on app context")
	}
	if ac.Cfg.DesktopNotification == nil || !*ac.Cfg.DesktopNotification {
		t.Fatal("expected project desktop_notification override to apply")
	}
	if ac.Cfg.PreventSleep == nil || !*ac.Cfg.PreventSleep {
		t.Fatal("expected project prevent_sleep override to apply")
	}
}

func TestApplyProjectConfigOverrides_MergesLSP(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Cfg = &config.Config{LSP: config.LSPConfig{"global": {Command: "gopls"}}}
	pc := &config.Config{LSP: config.LSPConfig{"project": {Command: "clangd"}}}

	applyProjectConfigOverrides(ac, pc)

	if ac.Cfg.LSP == nil {
		t.Fatal("expected merged LSP config")
	}
	if _, ok := ac.Cfg.LSP["global"]; !ok {
		t.Fatal("expected global LSP entry to remain")
	}
	if _, ok := ac.Cfg.LSP["project"]; !ok {
		t.Fatal("expected project LSP entry to be added")
	}
}

func TestApplyProjectConfigOverrides_WebFetchUserAgent(t *testing.T) {
	ac := newTestAppContext(t)
	globalUA := "GlobalUA/1.0"
	projectUA := "ProjectUA/2.0"
	ac.Cfg = &config.Config{WebFetch: config.WebFetchConfig{UserAgent: &globalUA}}
	pc := &config.Config{WebFetch: config.WebFetchConfig{UserAgent: &projectUA}}

	applyProjectConfigOverrides(ac, pc)

	if ac.Cfg.WebFetch.UserAgent == nil || *ac.Cfg.WebFetch.UserAgent != projectUA {
		t.Fatalf("expected project web_fetch.user_agent override to apply, got %#v", ac.Cfg.WebFetch.UserAgent)
	}
}

func TestApplyProjectConfigOverrides_WebFetchEmptyUserAgentResetsGlobal(t *testing.T) {
	ac := newTestAppContext(t)
	globalUA := "GlobalUA/1.0"
	projectUA := ""
	ac.Cfg = &config.Config{WebFetch: config.WebFetchConfig{UserAgent: &globalUA}}
	pc := &config.Config{WebFetch: config.WebFetchConfig{UserAgent: &projectUA}}

	applyProjectConfigOverrides(ac, pc)

	if ac.Cfg.WebFetch.UserAgent == nil || *ac.Cfg.WebFetch.UserAgent != "" {
		t.Fatalf("expected empty project web_fetch.user_agent to reset global override, got %#v", ac.Cfg.WebFetch.UserAgent)
	}
}
